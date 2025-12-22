package workflow

import (
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/core_modules/txinput_queue"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util"
)

type (
	txInOptions struct {
		txMetadata       txmetadata.TransactionMetadata
		receivedFromPeer *peer.ID
	}

	TxInOption func(options *txInOptions)
)

const (
	TraceTagTxInput = "txinput"
)

func (w *Workflow) TxFromStoreIn(txid base.TransactionID) (err error) {
	_, err = w.TxBytesFromStoreIn(w.TxBytesStore().GetTxBytesWithMetadata(&txid))
	return
}

func (w *Workflow) TxBytesFromStoreIn(txBytesWithMetadata []byte) (base.TransactionID, error) {
	nowis := time.Now()
	txBytes, meta, err := txmetadata.ParseTxMetadata(txBytesWithMetadata)
	if err != nil {
		return base.TransactionID{}, err
	}
	if meta == nil {
		meta = &txmetadata.TransactionMetadata{}
	}
	meta.TxBytesReceived = &nowis
	return w.TxBytesIn(txBytes,
		WithMetadata(meta),
		WithSourceType(txmetadata.SourceTypeTxStore),
	)
}

func (w *Workflow) TxBytesIn(txBytes []byte, opts ...TxInOption) (base.TransactionID, error) {
	// base validation
	tx, err := transaction.FromBytes(txBytes)
	if err != nil {
		// any malformed data chunk will be rejected immediately before all the advanced validations
		return base.TransactionID{}, err
	}
	return tx.ID(), w.TxIn(tx, opts...)
}

func (w *Workflow) TxBytesInFromAPIQueued(txBytes []byte) {
	w.txInputQueue.Push(txinput_queue.Input{
		Cmd:     txinput_queue.CmdFromAPI,
		TxBytes: txBytes,
		TxMetaData: &txmetadata.TransactionMetadata{
			SourceTypeNonPersistent: txmetadata.SourceTypeAPI,
			TxBytesReceived:         util.Ref(time.Now()),
		},
	})
}

func (w *Workflow) TxBytesInFromPeerQueued(txBytesReceived []byte, metaData *txmetadata.TransactionMetadata, from peer.ID, txidPrefix base.TransactionID) {
	if metaData == nil {
		metaData = &txmetadata.TransactionMetadata{}
	}
	metaData.TxBytesReceived = util.Ref(time.Now())
	w.txInputQueue.Push(txinput_queue.Input{
		Cmd:        txinput_queue.CmdFromPeer,
		TxIDPrefix: txidPrefix,
		TxBytes:    txBytesReceived,
		TxMetaData: metaData,
		FromPeer:   from,
	})
}

func (w *Workflow) TxInFromAPI(tx *transaction.Transaction) error {
	return w.TxIn(tx, WithSourceType(txmetadata.SourceTypeAPI))
}

func (w *Workflow) TxInFromPeer(tx *transaction.Transaction, metaData *txmetadata.TransactionMetadata, from peer.ID) error {
	return w.TxIn(tx, WithPeerMetadata(from, metaData))
}

func (w *Workflow) TxIn(tx *transaction.Transaction, opts ...TxInOption) error {
	options := &txInOptions{}
	for _, opt := range opts {
		opt(options)
	}
	// base validation
	txid := tx.ID()

	if !txid.IsSequencerMilestone() {
		w.EvidenceNonSequencerTx()
	}

	w.Tracef(TraceTagTxInput, "-> %s, meta: %s", txid.StringShort, options.txMetadata.String())
	// bytes are identifiable as transaction

	// check time bounds
	// TODO revisit checking lower time bounds

	enforceTimeBounds := options.txMetadata.SourceTypeNonPersistent == txmetadata.SourceTypeAPI || options.txMetadata.SourceTypeNonPersistent == txmetadata.SourceTypePeer
	// transaction is rejected if it is too far in the future wrt the local clock
	nowis := time.Now()

	timeUpperBound := nowis.Add(w.MaxDurationInTheFuture())
	err := tx.Validate(transaction.CheckTimestampUpperBound(timeUpperBound))
	if err != nil {
		if enforceTimeBounds {
			w.Tracef(TraceTagTxInput, "invalidate %s: time bounds validation failed", txid.StringShort)
			err = fmt.Errorf("%w (MaxDurationInTheFuture = %v)", err, w.MaxDurationInTheFuture())
			attacher.InvalidateTxID(txid, w, err)

			return err
		}
		w.Log().Warnf("checking time bounds of %s: '%v'", txid.StringShort(), err)
	}

	// run remaining pre-validations on the transaction
	if err = tx.Validate(transaction.MainTxValidationOptions...); err != nil {
		err = fmt.Errorf("error while pre-validating transaction %s: '%w'", txid.StringShort(), err)
		w.Tracef(TraceTagTxInput, "%v", err)
		attacher.InvalidateTxID(txid, w, err)
		return err
	}

	w.EvidenceNumberOfTxDependencies(tx.NumInputs() + tx.NumEndorsements())

	if options.txMetadata.SourceTypeNonPersistent != txmetadata.SourceTypeTxStore {
		// persisting all raw transactions which pass pre-validation
		w.MustPersistTxBytesWithMetadata(tx.Bytes(), &options.txMetadata, tx.ID())
	}

	// passes transaction to attacher
	// - immediately if timestamp is in the past
	// - with delay if timestamp is in the future
	txTime := ledger.ClockTime(txid.Timestamp())

	attachOpts := []attacher.AttachTxOption{
		attacher.WithTransactionMetadata(&options.txMetadata),
		attacher.WithInvokedBy("txInput"),
		attacher.WithEnforceTimestampBeforeRealTime,
	}

	if time.Until(txTime) <= 0 {
		// timestamp is in the past -> attach immediately
		w._attach(tx, attachOpts...)
	} else {
		// timestamp is in the future: let clock catch up before attaching
		go func() {
			w.IncCounter("wait")
			defer w.DecCounter("wait")

			w.ClockCatchUpWithLedgerTime(txid.Timestamp())

			w.Tracef(TraceTagTxInput, "%s -> release", txid.StringShort)

			w._attach(tx, attachOpts...)
		}()
	}
	return nil
}

func (w *Workflow) _attach(tx *transaction.Transaction, opts ...attacher.AttachTxOption) {
	// enforcing ledger time of the transaction cannot be ahead of the clock
	nowis := time.Now()
	tsTime := tx.TimestampTime()
	util.Assertf(nowis.After(tsTime), "nowis(%d).After(tsTime(%d))", nowis.UnixNano(), tsTime.UnixNano())

	w.Tracef(TraceTagTxInput, "-> attach tx %s", tx.IDShortString)
	attacher.AttachTransaction(tx, w, opts...)
}

func (w *Workflow) OwnSequencerMilestoneIn(txBytes []byte, meta *txmetadata.TransactionMetadata, txid base.TransactionID) {
	w.TxBytesInFromPeerQueued(txBytes, meta, w.SelfPeerID(), txid)
}

func (w *Workflow) CheckTxSender(tx *transaction.Transaction, txIDPrefix base.TransactionID, meta *txmetadata.TransactionMetadata, fromPeer peer.ID, wanted bool) {
	w.txSenders.CheckTxSender(tx, txIDPrefix, meta, fromPeer, wanted)
}

func WithMetadata(metadata *txmetadata.TransactionMetadata) TxInOption {
	return func(opts *txInOptions) {
		if metadata != nil {
			opts.txMetadata = *metadata
		}
	}
}

func WithSourceType(sourceType txmetadata.SourceType) TxInOption {
	return func(opts *txInOptions) {
		opts.txMetadata.SourceTypeNonPersistent = sourceType
	}
}

func WithPeerMetadata(peerID peer.ID, metadata *txmetadata.TransactionMetadata) TxInOption {
	return func(opts *txInOptions) {
		if metadata != nil {
			opts.txMetadata = *metadata
		}
		opts.receivedFromPeer = &peerID
	}
}
