package workflow

import (
	"context"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util"
)

type (
	txBytesInOptions struct {
		txMetadata       txmetadata.TransactionMetadata
		receivedFromPeer *peer.ID
		callback         func(vid *vertex.WrappedTx, err error)
		txTrace          bool
	}

	TxBytesInOption func(options *txBytesInOptions)
)

const (
	TraceTagTxInput = "txinput"
)

// ignoreTxID always false if sync manager is not enabled
func (w *Workflow) ignoreTxID(txid *ledger.TransactionID) bool {
	return w.syncManager != nil && w.syncManager.IgnoreFutureTxID(txid)
}

// TxBytesIn main entry point of the transaction into the workflow
func (w *Workflow) TxBytesIn(txBytes []byte, opts ...TxBytesInOption) (*ledger.TransactionID, error) {
	options := &txBytesInOptions{}
	for _, opt := range opts {
		opt(options)
	}
	// base validation
	tx, err := transaction.FromBytes(txBytes)
	if err != nil {
		// any malformed data chunk will be rejected immediately before all the advanced validations
		return nil, err
	}
	txid := tx.ID()

	if w.ignoreTxID(txid) {
		// sync manager is still syncing. Ignore transaction
		return nil, nil
	}

	if options.txTrace {
		w.StartTracingTx(*txid)
	}
	w.Tracef(TraceTagTxInput, "-> %s, meta: %s", txid.StringShort, options.txMetadata.String())
	// bytes are identifiable as transaction
	w.Assertf(!options.txMetadata.IsResponseToPull || options.txMetadata.SourceTypeNonPersistent == txmetadata.SourceTypePeer,
		"inData.TxMetadata.IsResponseToPull || inData.TxMetadata.SourceTypeNonPersistent == txmetadata.SourceTypePeer")

	// transaction is here, stop pulling, if pulled before
	w.StopPulling(tx.ID())

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
	err = tx.Validate(transaction.CheckTimestampUpperBound(timeUpperBound))
	if err != nil {
		if enforceTimeBounds {
			w.Tracef(TraceTagTxInput, "invalidate %s: time bounds validation failed", txid.StringShort)
			err = fmt.Errorf("upper timestamp bound exceeded (MaxDurationInTheFuture = %v)", w.MaxDurationInTheFuture())
			attacher.InvalidateTxID(*txid, w, err)

			return txid, err
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
		return txid, err
	}

	// considered good incoming
	if options.txMetadata.SourceTypeNonPersistent == txmetadata.SourceTypePeer ||
		options.txMetadata.SourceTypeNonPersistent == txmetadata.SourceTypeAPI {
		// always gossip pre-validated (parsed) transaction received from peer or from API, even if it needs delay.
		// Reason: other nodes might have slightly different clocks, let them handle delay themselves
		// Sequencer transactions not from outside will be gossiped by attacher
		w.Tracef(TraceTagTxInput, "send to GossipTransactionIfNeeded %s", txid.StringShort)
		w.TraceTx(txid, "TxBytesIn: send to GossipTransactionIfNeeded")
		w.GossipTransactionIfNeeded(tx, &options.txMetadata, options.receivedFromPeer)
	}

	// passes transaction to attacher
	// - immediately if timestamp is in the past
	// - with delay if timestamp is in the future
	txTime := txid.Timestamp().Time()

	attachOpts := []attacher.Option{
		attacher.OptionWithTransactionMetadata(&options.txMetadata),
		attacher.OptionInvokedBy("txInput"),
		attacher.OptionEnforceTimestampBeforeRealTime,
	}
	if options.callback != nil {
		attachOpts = append(attachOpts, attacher.OptionWithAttachmentCallback(options.callback))
	}

	if txTime.Before(nowis) {
		// timestamp is in the past -> attach immediately
		w._attach(tx, attachOpts...)
		return txid, nil
	}

	// timestamp is in the future. Put it on wait. Adding some milliseconds to avoid rounding errors in assertions
	delayFor := txTime.Sub(nowis)
	w.Tracef(TraceTagTxInput, "%s -> delay for %v", txid.StringShort, delayFor)
	w.TraceTx(txid, "TxBytesIn: delay for %v", delayFor)

	go func() {
		time.Sleep(delayFor)
		_ensureNowIsAfter(txTime) // to avoid time rounding errors

		w.Tracef(TraceTagTxInput, "%s -> release", txid.StringShort)
		w.TraceTx(txid, "TxBytesIn: -> release")

		w._attach(tx, attachOpts...)
	}()
	return txid, nil
}

func _ensureNowIsAfter(targetTime time.Time) {
	for !time.Now().After(targetTime) {
		time.Sleep(time.Millisecond)
	}
}

func (w *Workflow) _attach(tx *transaction.Transaction, opts ...attacher.Option) {
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
	var txid *ledger.TransactionID

	ctx, cancel := context.WithTimeout(w.Ctx(), timeout)
	go func() {
		var errParse error
		txid, errParse = w.TxBytesIn(txBytes,
			WithMetadata(meta),
			WithAttachmentCallback(func(vidSubmit *vertex.WrappedTx, errSubmit error) {
				vid = vidSubmit
				err = errSubmit
				cancel()
			}),
		)
		if errParse != nil {
			// parse error means attacher won't be started and callback won't be called
			err = errParse
			cancel()
		}
	}()
	<-ctx.Done()

	if err != nil {
		return nil, err
	}
	if vid == nil {
		txidStr := "txid=???"
		if txid != nil {
			txidStr = txid.StringShort()
		}
		return nil, fmt.Errorf("SequencerMilestoneAttachWait: timeout %v exceeded while submitting transaction %s", timeout, txidStr)
	}
	return vid, nil
}

func WithAttachmentCallback(fun func(vid *vertex.WrappedTx, err error)) TxBytesInOption {
	return func(opts *txBytesInOptions) {
		opts.callback = fun
	}
}

func WithMetadata(metadata *txmetadata.TransactionMetadata) TxBytesInOption {
	return func(opts *txBytesInOptions) {
		if metadata != nil {
			opts.txMetadata = *metadata
		}
	}
}

func WithSourceType(sourceType txmetadata.SourceType) TxBytesInOption {
	return func(opts *txBytesInOptions) {
		opts.txMetadata.SourceTypeNonPersistent = sourceType
	}
}

func WithPeerMetadata(peerID peer.ID, metadata *txmetadata.TransactionMetadata) TxBytesInOption {
	return func(opts *txBytesInOptions) {
		if metadata != nil {
			opts.txMetadata = *metadata
		}
		opts.txMetadata.SourceTypeNonPersistent = txmetadata.SourceTypePeer
		opts.receivedFromPeer = &peerID
	}
}

func WithTxTraceFlag(trace bool) TxBytesInOption {
	return func(opts *txBytesInOptions) {
		opts.txTrace = trace
	}
}
