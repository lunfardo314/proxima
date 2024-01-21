package txinput

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/queue"
)

type (
	Environment interface {
		global.Logging
		MaxDurationInTheFuture() time.Duration
		IncCounter(string)
		StopPulling(txid *ledger.TransactionID)
		DropTxID(txid *ledger.TransactionID, callback func(vid *vertex.WrappedTx, err error), reasonFormat string, args ...any)
		AttachTransaction(inp *Input, opts ...attacher.Option)
		GossipTransaction(inp *Input)
	}

	Input struct {
		Tx               *transaction.Transaction
		TxMetadata       txmetadata.TransactionMetadata
		ReceivedFromPeer peer.ID
		ReceivedWhen     time.Time
		Callback         func(vid *vertex.WrappedTx, err error)
	}

	TxInput struct {
		*queue.Queue[*Input]
		Environment
	}

	TransactionInOption func(*Input)
)

const chanBufferSize = 10

func New(env Environment) *TxInput {
	return &TxInput{
		Queue:       queue.NewQueueWithBufferSize[*Input]("txInput", chanBufferSize, env.Log().Level(), nil),
		Environment: env,
	}
}

func (q *TxInput) Start(ctx context.Context, doneOnClose *sync.WaitGroup) {
	q.Queue.AddOnClosed(func() {
		doneOnClose.Done()
	})
	q.Queue.Start(q, ctx)
}

func (q *TxInput) Consume(inp *Input) {
	q.Tracef("txinput", "IN %s", inp.Tx.IDShortString)
	var err error
	// TODO revisit checking lower time bounds
	enforceTimeBounds := inp.TxMetadata.SourceTypeNonPersistent == txmetadata.SourceTypeAPI || inp.TxMetadata.SourceTypeNonPersistent == txmetadata.SourceTypePeer
	// transaction is rejected if it is too far in the future wrt the local clock
	nowis := time.Now()
	txid := inp.Tx.ID()

	q.StopPulling(txid)

	timeUpperBound := nowis.Add(q.MaxDurationInTheFuture())
	err = inp.Tx.Validate(transaction.CheckTimestampUpperBound(timeUpperBound))
	if err != nil {
		if enforceTimeBounds {
			q.Tracef("txinput", "drop %s due to time bounds", inp.Tx.IDShortString)
			q.DropTxID(txid, inp.Callback, "upper timestamp bound exceeded")
			q.IncCounter("invalid")
			return
		}
		q.Environment.Log().Warnf("checking time bounds of %s: '%v'", txid.StringShort(), err)
	}
	// run remaining pre-validations on the transaction
	if err = inp.Tx.Validate(transaction.MainTxValidationOptions...); err != nil {
		q.Tracef("txinput", "drop %s due validation failed: '%v'", inp.Tx.IDShortString, err)
		q.DropTxID(txid, inp.Callback, "error while pre-validating transaction: '%v'", err)
		q.IncCounter("invalid")
		return
	}
	q.IncCounter("ok")
	if !inp.TxMetadata.IsResponseToPull {
		// gossip always, even if it needs delay.
		// Reason: other nodes might have slightly different clock, let them handle delay themselves
		q.Tracef("txinput", "send to gossip %s", inp.Tx.IDShortString)
		q.GossipTransaction(inp)
	}

	// passes transaction for solidification
	// - immediately if timestamp is in the past
	// - with delay if timestamp is in the future
	txTime := inp.Tx.TimestampTime()

	opts := []attacher.Option{
		attacher.OptionWithTransactionMetadata(&inp.TxMetadata),
		attacher.OptionInvokedBy("txInput"),
	}
	if inp.Callback != nil {
		opts = append(opts, attacher.OptionWithAttachmentCallback(inp.Callback))
	}

	if txTime.Before(nowis) {
		// timestamp is in the past
		q.IncCounter("ok.now")
		q.Tracef("txinput", "-> attach tx %s", inp.Tx.IDShortString)
		q.AttachTransaction(inp, opts...)
		return
	}
	// timestamp is in the future. Put it into the waiting room
	q.IncCounter("ok.delay")
	delayFor := txTime.Sub(nowis)
	q.Tracef("txinput", "%s -> delay for %v", txid.StringShort, delayFor)

	go func() {
		time.Sleep(delayFor)
		q.Tracef("txinput", "%s -> release", txid.StringShort)
		q.IncCounter("ok.release")
		q.Tracef("txinput", "-> attach tx %s", inp.Tx.IDShortString)
		q.AttachTransaction(inp, opts...)
	}()
}

func (q *TxInput) TxBytesIn(txBytes []byte, opts ...TransactionInOption) (*ledger.TransactionID, error) {
	// base validation
	tx, err := transaction.FromBytes(txBytes)
	if err != nil {
		return nil, err
	}
	inData := &Input{}
	for _, opt := range opts {
		opt(inData)
	}
	inData.Tx = tx
	util.Assertf(!inData.TxMetadata.IsResponseToPull || inData.TxMetadata.SourceTypeNonPersistent == txmetadata.SourceTypePeer,
		"inData.TxMetadata.IsResponseToPull || inData.TxMetadata.SourceTypeNonPersistent == txmetadata.SourceTypePeer")

	if inData.TxMetadata.IsResponseToPull {
		q.StopPulling(tx.ID())
	}

	if !tx.IsSequencerMilestone() {
		inData.Callback = func(_ *vertex.WrappedTx, _ error) {}
	}
	priority := inData.TxMetadata.IsResponseToPull || inData.TxMetadata.SourceTypeNonPersistent == txmetadata.SourceTypeTxStore
	q.Queue.Push(inData, priority) // priority for pulled
	return tx.ID(), nil
}

func (q *TxInput) SequencerMilestoneNewAttachWait(txBytes []byte, timeout time.Duration) (*vertex.WrappedTx, error) {
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
		_, errParse := q.TxBytesIn(txBytes,
			WithMetadata(&txmetadata.TransactionMetadata{
				SourceTypeNonPersistent: txmetadata.SourceTypeSequencer,
			}),
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
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout %v", timeout)
	}
}

func WithCallback(fun func(vid *vertex.WrappedTx, err error)) TransactionInOption {
	return func(inp *Input) {
		inp.Callback = fun
	}
}

func WithMetadata(metadata *txmetadata.TransactionMetadata) TransactionInOption {
	return func(inp *Input) {
		if metadata != nil {
			inp.TxMetadata = *metadata
		}
	}
}

func WithSourceType(sourceType txmetadata.SourceType) TransactionInOption {
	return func(inp *Input) {
		inp.TxMetadata.SourceTypeNonPersistent = sourceType
	}
}
