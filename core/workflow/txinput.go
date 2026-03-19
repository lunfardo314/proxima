package workflow

import (
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/core_modules/nonseq_attach"
	"github.com/lunfardo314/proxima/core/core_modules/seq_attach"
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
	TraceTagTxInput       = "txinput"
	TraceTagTxInputNonSeq = "txinput-non-seq"
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
	tx, err := transaction.Parse(txBytes)
	if err != nil {
		// any malformed data chunk will be rejected immediately before all the advanced validations
		return base.TransactionID{}, err
	}
	return tx.ID(), w.attachTx(tx, opts...)
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
		PrefixTxID: txidPrefix,
		TxBytes:    txBytesReceived,
		TxMetaData: metaData,
		FromPeer:   from,
	})
}

func (w *Workflow) AttachTxFromAPI(tx *transaction.Transaction) error {
	return w.attachTx(tx, WithSourceType(txmetadata.SourceTypeAPI))
}

func (w *Workflow) AttachTxFromPeer(tx *transaction.Transaction, metaData *txmetadata.TransactionMetadata, from peer.ID) error {
	return w.attachTx(tx, WithPeerMetadata(from, metaData))
}

const maxSlotsInTheFuture = 6

func (w *Workflow) checkTimestampUpperBound(tx *transaction.Transaction) error {
	ts := ledger.ClockTime(tx.Timestamp())
	upperBound := time.Now().Add(maxSlotsInTheFuture * ledger.SlotDuration())
	if ts.After(upperBound) {
		return fmt.Errorf("transaction is %d msec too far in the future", int64(ts.Sub(upperBound))/int64(time.Millisecond))
	}
	return nil
}

func (w *Workflow) attachTx(tx *transaction.Transaction, opts ...TxInOption) error {
	options := &txInOptions{}
	for _, opt := range opts {
		opt(options)
	}
	// base validation
	txid := tx.ID()

	if !txid.IsSequencerTransaction() {
		w.EvidenceNonSequencerTx()
		w.Tracef(TraceTagTxInputNonSeq, "-> non-seq-tx %s, meta: %s", txid.StringShort, options.txMetadata.String())
	}

	w.Tracef(TraceTagTxInput, "-> %s, meta: %s", txid.StringShort, options.txMetadata.String())

	// check time bounds for external transactions
	// transaction is rejected if it is too far in the future wrt the local clock
	enforceTimeBounds := options.txMetadata.SourceTypeNonPersistent == txmetadata.SourceTypeAPI ||
		options.txMetadata.SourceTypeNonPersistent == txmetadata.SourceTypePeer

	if err := w.checkTimestampUpperBound(tx); err != nil {
		if enforceTimeBounds {
			msg := fmt.Sprintf("enforcing time bounds: %v", err)
			w.LogTx(time.Now(), msg, txid)
			w.Log().Warnf("%s -- %s", msg, txid.StringShort())
			attacher.InvalidateTxID(txid, w, err)
			return err
		}
		w.LogTx(time.Now(), err.Error(), txid)
		w.Log().Warnf("%v -- %s", err, txid.StringShort())
	}

	// run remaining pre-validations on the transaction (including signature checks)
	if err := tx.ValidatePartialContext(); err != nil {
		err = fmt.Errorf("error while pre-validating transaction %s: '%w'", txid.StringShort(), err)
		w.LogTx(time.Now(), err.Error(), txid)
		attacher.InvalidateTxID(txid, w, err)
		return err
	}

	w.EvidenceNumberOfTxDependencies(tx.NumInputs() + tx.NumEndorsements())

	if options.txMetadata.SourceTypeNonPersistent != txmetadata.SourceTypeTxStore {
		// persisting all raw transactions which pass pre-validation
		w.MustPersistTxBytesWithMetadata(tx.Bytes(), &options.txMetadata, tx.ID())
	}

	// passes transaction to the appropriate attach queue
	// - immediately if timestamp is in the past
	// - with delay if timestamp is in the future
	txTime := ledger.ClockTime(txid.Timestamp())

	attachOpts := []attacher.AttachTxOption{
		attacher.WithTransactionMetadata(&options.txMetadata),
		attacher.WithInvokedBy("txInput"),
		attacher.WithEnforceTimestampBeforeRealTime,
	}
	pulled := options.txMetadata.SourceTypeNonPersistent == txmetadata.SourceTypePulled

	if time.Until(txTime) <= 0 {
		w.pushToAttachQueue(tx, attachOpts, pulled)
	} else {
		// timestamp is in the future: let clock catch up before attaching
		go func() {
			w.IncCounter("wait")
			defer w.DecCounter("wait")

			if !w.ClockCatchUpWithLedgerTime(txid.Timestamp()) {
				// interrupted by shutdown
				return
			}

			w.Tracef(TraceTagTxInput, "%s -> release", txid.StringShort)

			w.pushToAttachQueue(tx, attachOpts, pulled)
		}()
	}
	return nil
}

// pushToAttachQueue routes the transaction to the sequencer or non-sequencer attach queue.
// Pulled transactions are pushed with priority so they are processed first.
func (w *Workflow) pushToAttachQueue(tx *transaction.Transaction, opts []attacher.AttachTxOption, pulled bool) {
	txid := tx.ID()
	if txid.IsSequencerTransaction() {
		w.seqAttach.Push(&seq_attach.Input{
			Tx:     tx,
			Opts:   opts,
			Pulled: pulled,
		}, pulled)
	} else {
		w.nonSeqAttach.Push(&nonseq_attach.Input{
			Tx:     tx,
			Opts:   opts,
			Pulled: pulled,
		}, pulled)
	}
}

func (w *Workflow) _attach(tx *transaction.Transaction, opts ...attacher.AttachTxOption) {
	// enforcing ledger time of the transaction cannot be ahead of the clock
	nowis := time.Now()
	tsTime := tx.TimestampTime()
	util.Assertf(nowis.After(tsTime), "nowis(%d).After(tsTime(%d))", nowis.UnixNano(), tsTime.UnixNano())

	w.Tracef(TraceTagTxInput, "-> attachTx tx %s", tx.IDShortString)
	attacher.AttachTransaction(tx, w, opts...)
}

func (w *Workflow) OwnSequencerMilestoneIn(txBytes []byte, meta *txmetadata.TransactionMetadata, txid base.TransactionID) {
	w.TxBytesInFromPeerQueued(txBytes, meta, w.SelfPeerID(), txid)
}

func (w *Workflow) CheckTxSender(tx *transaction.Transaction, meta *txmetadata.TransactionMetadata, fromPeer peer.ID, wanted bool) {
	w.txSenders.CheckTxSender(tx, meta, fromPeer, wanted)
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
