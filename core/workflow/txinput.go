package workflow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/core/work_process/txinput_queue"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util"
)

type (
	txInOptions struct {
		txMetadata       txmetadata.TransactionMetadata
		receivedFromPeer *peer.ID
		callback         func(vid *vertex.WrappedTx, err error)
		txTrace          bool
		ctx              context.Context
	}

	TxInOption func(options *txInOptions)
)

const (
	TraceTagTxInput = "txinput"
)

func (w *Workflow) TxFromStoreIn(txid *ledger.TransactionID) (err error) {
	_, err = w.TxBytesFromStoreIn(w.TxBytesStore().GetTxBytesWithMetadata(txid))
	return
}

func (w *Workflow) TxBytesFromStoreIn(txBytesWithMetadata []byte) (*ledger.TransactionID, error) {
	nowis := time.Now()
	txBytes, meta, err := txmetadata.ParseTxMetadata(txBytesWithMetadata)
	if err != nil {
		return nil, err
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

func (w *Workflow) TxBytesIn(txBytes []byte, opts ...TxInOption) (*ledger.TransactionID, error) {
	// base validation
	tx, err := transaction.FromBytes(txBytes)
	if err != nil {
		// any malformed data chunk will be rejected immediately before all the advanced validations
		return nil, err
	}
	return tx.ID(), w.TxIn(tx, opts...)
}

func (w *Workflow) TxInFromAPI(tx *transaction.Transaction, trace bool) error {
	return w.TxIn(tx,
		WithSourceType(txmetadata.SourceTypeAPI),
		WithTxTraceFlag(trace),
	)
}

func (w *Workflow) TxBytesInFromAPIQueued(txBytes []byte, trace bool) {
	w.txInputQueue.Push(txinput_queue.Input{
		Cmd:        txinput_queue.CmdFromAPI,
		TxBytes:    txBytes,
		TraceFlag:  trace,
		TxMetaData: &txmetadata.TransactionMetadata{TxBytesReceived: util.Ref(time.Now())},
	})
}

func (w *Workflow) TxBytesInFromPeerQueued(txBytes []byte, metaData *txmetadata.TransactionMetadata, from peer.ID) {
	if metaData == nil {
		metaData = &txmetadata.TransactionMetadata{}
	}
	metaData.TxBytesReceived = util.Ref(time.Now())
	w.txInputQueue.Push(txinput_queue.Input{
		Cmd:        txinput_queue.CmdFromPeer,
		TxBytes:    txBytes,
		TxMetaData: metaData,
		FromPeer:   from,
	})
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

	if options.txTrace {
		w.StartTracingTx(*txid)
	}
	w.Tracef(TraceTagTxInput, "-> %s, meta: %s", txid.StringShort, options.txMetadata.String())
	// bytes are identifiable as transaction

	if !tx.IsSequencerMilestone() {
		// callback is only possible when tx is sequencer milestone
		options.callback = func(_ *vertex.WrappedTx, _ error) {}
	}

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
			attacher.InvalidateTxID(*txid, w, err)

			return err
		}
		w.Log().Warnf("checking time bounds of %s: '%v'", txid.StringShort(), err)
		w.TraceTx(txid, "TxBytesIn: checking time bounds: '%v'", err)
	}

	// run remaining pre-validations on the transaction
	if err = tx.Validate(transaction.MainTxValidationOptions...); err != nil {
		err = fmt.Errorf("error while pre-validating transaction %s: '%w'", txid.StringShort(), err)
		w.Tracef(TraceTagTxInput, "%v", err)
		w.TraceTx(txid, "TxBytesIn: %v", err)
		attacher.InvalidateTxID(*txid, w, err)
		return err
	}

	w.EvidenceNumberOfTxDependencies(tx.NumInputs() + tx.NumEndorsements())

	if options.txMetadata.SourceTypeNonPersistent != txmetadata.SourceTypeTxStore {
		// persisting all raw transactions which pass pre-validation
		w.MustPersistTxBytesWithMetadata(tx.Bytes(), &options.txMetadata)
	}

	// passes transaction to attacher
	// - immediately if timestamp is in the past
	// - with delay if timestamp is in the future
	txTime := txid.Timestamp().Time()

	attachOpts := []attacher.AttachTxOption{
		attacher.WithContext(options.ctx),
		attacher.WithTransactionMetadata(&options.txMetadata),
		attacher.WithInvokedBy("txInput"),
		attacher.WithEnforceTimestampBeforeRealTime,
	}
	if options.callback != nil {
		attachOpts = append(attachOpts, attacher.WithAttachmentCallback(options.callback))
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
			w.TraceTx(txid, "TxBytesIn: -> release")

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

	w.TraceTx(tx.ID(), "TxBytesIn: send to attach")
	w.Tracef(TraceTagTxInput, "-> attach tx %s", tx.IDShortString)
	if vid := attacher.AttachTransaction(tx, w, opts...); vid.IsBadOrDeleted() {
		// rare event. If tx is already purged, this was an unlucky try.
		// Transaction will be erased from the dag and pulled again, if necessary
		w.TraceTx(&vid.ID, "TxBytesIn: -> failed to attach: bad or deleted: err = %v", vid.GetError)
		w.Tracef(TraceTagTxInput, "-> failed to attach tx %s: it is bad or deleted: err = %v", vid.IDShortString, vid.GetError)
	}
}

// SequencerMilestoneAttachWait attaches sequencer transaction synchronously.
// Waits up to timeout until attacher finishes
func (w *Workflow) SequencerMilestoneAttachWait(txBytes []byte, meta *txmetadata.TransactionMetadata, timeout time.Duration) (*vertex.WrappedTx, error) {
	var vid *vertex.WrappedTx
	var err error

	const defaultTimeout = 5 * time.Second
	if timeout == 0 {
		timeout = defaultTimeout
	}
	errTimeoutCause := fmt.Errorf("exceeded timeout %v", timeout)
	ctx, cancelFun := context.WithTimeoutCause(w.Ctx(), timeout, errTimeoutCause)

	// we need end channel so that to be sure callback has been called before we use err and vid
	endCh := make(chan struct{})
	txid, errParse := w.TxBytesIn(txBytes,
		WithContext(ctx),
		WithMetadata(meta),
		WithAttachmentCallback(func(vidSubmit *vertex.WrappedTx, errSubmit error) {
			// it is guaranteed the callback will always be called, unless parse error
			vid = vidSubmit
			err = errSubmit
			cancelFun()
			close(endCh)
		}),
	)
	if errParse == nil {
		<-ctx.Done()
		<-endCh

		if errors.Is(context.Cause(ctx), errTimeoutCause) {
			err = errTimeoutCause
		}
	} else {
		err = errParse
		cancelFun()
		close(endCh)
	}

	if err != nil {
		txidStr := "txid=???"
		if txid != nil {
			txidStr = txid.StringShort()
		}
		return vid, fmt.Errorf("SequencerMilestoneAttachWait: %w, txid=%s", err, txidStr)
	}
	return vid, nil
}

func WithAttachmentCallback(fun func(vid *vertex.WrappedTx, err error)) TxInOption {
	return func(opts *txInOptions) {
		opts.callback = fun
	}
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

func WithTxTraceFlag(trace bool) TxInOption {
	return func(opts *txInOptions) {
		opts.txTrace = trace
	}
}

func WithContext(ctx context.Context) TxInOption {
	return func(opts *txInOptions) {
		opts.ctx = ctx
	}
}
