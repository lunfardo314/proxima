package workflow

import (
	"fmt"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
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

func (w *Workflow) TxBytesIn(txBytes []byte, opts ...TxBytesInOption) (*ledger.TransactionID, error) {
	// base validation
	tx, err := transaction.FromBytes(txBytes)
	if err != nil {
		// any malformed data chunk will be rejected immediately before all the advanced validations
		return nil, err
	}
	txid := tx.ID()

	options := &txBytesInOptions{}
	for _, opt := range opts {
		opt(options)
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
		w.TraceTx(txid, "checking time bounds: '%v'", err)
	}

	// run remaining pre-validations on the transaction
	if err = tx.Validate(transaction.MainTxValidationOptions...); err != nil {
		err = fmt.Errorf("error while pre-validating transaction %s: '%w'", txid.StringShort(), err)
		w.Tracef(TraceTagTxInput, "%v", err)
		w.TraceTx(txid, "%v", err)
		attacher.InvalidateTxID(*txid, w, err)
		return txid, err
	}

	if options.txMetadata.SourceTypeNonPersistent == txmetadata.SourceTypePeer ||
		options.txMetadata.SourceTypeNonPersistent == txmetadata.SourceTypeAPI {
		// always gossip pre-validated (parsed) transaction received from peer or from API, even if it needs delay.
		// Reason: other nodes might have slightly different clocks, let them handle delay themselves
		// Sequencer transactions not from outside will be gossiped by attacher
		w.Tracef(TraceTagTxInput, "send to GossipTransactionIfNeeded %s", txid.StringShort)
		w.TraceTx(txid, "send to GossipTransactionIfNeeded")
		w.GossipTransactionIfNeeded(tx, &options.txMetadata, options.receivedFromPeer)
	}

	// passes transaction to attacher
	// - immediately if timestamp is in the past
	// - with delay if timestamp is in the future
	txTime := txid.Timestamp().Time()

	attachOpts := []attacher.Option{
		attacher.OptionWithTransactionMetadata(&options.txMetadata),
		attacher.OptionInvokedBy("txInput"),
	}
	if options.callback != nil {
		attachOpts = append(attachOpts, attacher.OptionWithAttachmentCallback(options.callback))
	}

	if txTime.Before(nowis) {
		// timestamp is in the past -> attach immediately
		w._attach(tx, attachOpts...)
		return txid, nil
	}

	// timestamp is in the future. Put it on wait
	delayFor := txTime.Sub(nowis)
	w.Tracef(TraceTagTxInput, "%s -> delay for %v", txid.StringShort, delayFor)
	w.TraceTx(txid, "delay for %v", delayFor)

	go func() {
		time.Sleep(delayFor)
		w.Tracef(TraceTagTxInput, "%s -> release", txid.StringShort)
		w.TraceTx(txid, "-> release")

		w._attach(tx, attachOpts...)
	}()
	return txid, nil
}

func (w *Workflow) _attach(tx *transaction.Transaction, opts ...attacher.Option) {
	w.TraceTx(tx.ID(), "send to attach")
	w.Tracef(TraceTagTxInput, "-> attach tx %s", tx.IDShortString)
	if vid := attacher.AttachTransaction(tx, w, opts...); vid.IsBadOrDeleted() {
		// rare event. If tx is already purged, this was an unlucky try.
		// Transaction will be erased from the dag and pulled again, if necessary
		w.TraceTx(&vid.ID, "-> failed to attach: bad or deleted: err = %v", vid.GetError)
		w.Tracef(TraceTagTxInput, "-> failed to attach tx %s: it is bad or deleted: err = %v", vid.IDShortString, vid.GetError)
	}
}

func (w *Workflow) SequencerMilestoneAttachWait(txBytes []byte, meta *txmetadata.TransactionMetadata, timeout time.Duration) (*vertex.WrappedTx, error) {
	type result struct {
		vid *vertex.WrappedTx
		err error
	}

	closed := false
	var closedMutex sync.Mutex
	resCh := make(chan result)
	defer func() {
		closedMutex.Lock()
		defer closedMutex.Unlock()
		closed = true
		close(resCh)
	}()

	go func() {
		writeResult := func(res result) {
			closedMutex.Lock()
			defer closedMutex.Unlock()
			if closed {
				return
			}
			resCh <- res
		}

		_, errParse := w.TxBytesIn(txBytes,
			WithMetadata(meta),
			WithCallback(func(vid *vertex.WrappedTx, err error) {
				writeResult(result{vid: vid, err: err})
			}))
		if errParse != nil {
			writeResult(result{err: errParse})
		}
	}()
	select {
	case res := <-resCh:
		return res.vid, res.err
	case <-w.Ctx().Done():
		return nil, fmt.Errorf("cancelled")
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout %v", timeout)
	}
}

func WithCallback(fun func(vid *vertex.WrappedTx, err error)) TxBytesInOption {
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

func WithTxTrace(opts *txBytesInOptions) {
	opts.txTrace = true
}
