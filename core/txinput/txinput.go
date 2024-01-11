package txinput

import (
	"context"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/txmetadata"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/queue"
	"go.uber.org/zap"
)

type (
	TransactionInputEnvironment interface {
		MaxDurationInTheFuture() time.Duration
		IncCounter(string)
		StopPulling(txid *ledger.TransactionID)
		DropTxID(txid *ledger.TransactionID, who string, reasonFormat string, args ...any)
		AttachTransaction(inp *TransactionInputData)
		GossipTransaction(inp *TransactionInputData)
	}

	TransactionSource byte

	TransactionInputData struct {
		Tx               *transaction.Transaction
		TxMetadata       *txmetadata.TransactionMetadata
		ReceivedFromPeer peer.ID
		ReceivedWhen     time.Time
		TxSource         TransactionSource
	}

	TransactionInputQueue struct {
		*queue.Queue[*TransactionInputData]
		env TransactionInputEnvironment
	}

	TransactionInOption func(*TransactionInputData)
)

const (
	TransactionSourceAPI = TransactionSource(iota)
	TransactionSourceSequencer
	TransactionSourcePeer
	TransactionSourceStore
)

const chanBufferSize = 10

func (t TransactionSource) String() string {
	switch t {
	case TransactionSourceAPI:
		return "API"
	case TransactionSourceSequencer:
		return "sequencer"
	case TransactionSourcePeer:
		return "peer"
	case TransactionSourceStore:
		return "txStore"
	default:
		return "(unknown Tx TxSource)"
	}
}

func Start(ctx context.Context) *TransactionInputQueue {
	ret := &TransactionInputQueue{
		Queue: queue.NewConsumerWithBufferSize[*TransactionInputData]("txInput", chanBufferSize, zap.InfoLevel, nil),
	}
	ret.AddOnConsume(ret.consume)
	go func() {
		ret.Log().Infof("starting..")
		ret.Run()
	}()

	go func() {
		<-ctx.Done()
		ret.Queue.Stop()
	}()
	return ret
}

func (q *TransactionInputQueue) consume(inp *TransactionInputData) {
	var err error
	// TODO revisit checking lower time bounds
	enforceTimeBounds := inp.TxSource == TransactionSourceAPI || inp.TxSource == TransactionSourcePeer
	// transaction is rejected if it is too far in the future wrt the local clock
	nowis := time.Now()
	txid := inp.Tx.ID()

	q.env.StopPulling(txid)

	timeUpperBound := nowis.Add(q.env.MaxDurationInTheFuture())
	err = inp.Tx.Validate(transaction.CheckTimestampUpperBound(timeUpperBound))
	if err != nil {
		if enforceTimeBounds {
			q.env.DropTxID(txid, "txInput", "upper timestamp bound exceeded")
			q.env.IncCounter("invalid")
			return
		}
		q.Log().Warnf("checking time bounds of %s: '%v'", txid.StringShort(), err)
	}
	// run remaining pre-validations on the transaction
	if err = inp.Tx.Validate(transaction.MainTxValidationOptions...); err != nil {
		q.env.DropTxID(txid, "txInput", "error while pre-validating Tx: '%v'", err)
		q.env.IncCounter("invalid")
		return
	}
	q.env.IncCounter("ok")
	// gossip always, even if it needs delay
	q.env.GossipTransaction(inp)

	// passes transaction for solidification
	// - immediately if timestamp is in the past
	// - with delay if timestamp is in the future
	txTime := inp.Tx.TimestampTime()

	if txTime.Before(nowis) {
		// timestamp is in the past, pass it to the solidifier
		q.env.IncCounter("ok.now")
		q.env.AttachTransaction(inp)
		return
	}
	// timestamp is in the future. Put it into the waiting room
	q.env.IncCounter("ok.delay")
	delayFor := txTime.Sub(nowis)
	q.Log().Debugf("%s -> delay for %v", txid.StringShort(), delayFor)
	go func() {
		time.Sleep(delayFor)
		q.env.IncCounter("ok.release")
		q.env.GossipTransaction(inp)
		q.env.AttachTransaction(inp)
	}()
}

func (q *TransactionInputQueue) TransactionIn(txBytes []byte, opts ...TransactionInOption) error {
	_, err := q.TransactionInReturnTx(txBytes, opts...)
	return err
}

func (q *TransactionInputQueue) TransactionInReturnTx(txBytes []byte, opts ...TransactionInOption) (*transaction.Transaction, error) {
	//if !w.IsRunning() {
	//	return nil, fmt.Errorf("TransactionInReturnTx: workflow_old is inactive")
	//}
	// base validation
	tx, err := transaction.FromBytes(txBytes)
	if err != nil {
		return nil, err
	}
	// if raw transaction data passes the basic check, it means it is identifiable as a transaction and main properties
	// are correct: ID, timestamp, sequencer and branch transaction flags. The signature and semantic has not been checked at this point

	inData := &TransactionInputData{Tx: tx}
	for _, opt := range opts {
		opt(inData)
	}

	responseToPull := inData.TxMetadata != nil && inData.TxMetadata.SendType == txmetadata.SendTypeResponseToPull
	util.Assertf(!responseToPull || inData.TxSource == TransactionSourcePeer, "!responseToPull || inData.source == TransactionSourcePeer")

	if responseToPull {
		q.env.StopPulling(tx.ID())
	}

	priority := responseToPull || inData.TxSource == TransactionSourceStore
	q.Queue.Push(inData, priority) // priority for pulled
	return tx, nil
}

func WithTransactionSource(src TransactionSource) TransactionInOption {
	return func(data *TransactionInputData) {
		data.TxSource = src
	}
}

func WithTransactionMetadata(metadata *txmetadata.TransactionMetadata) TransactionInOption {
	return func(data *TransactionInputData) {
		data.TxMetadata = metadata
	}
}

func WithTransactionSourcePeer(from peer.ID) TransactionInOption {
	return func(data *TransactionInputData) {
		data.TxSource = TransactionSourcePeer
		data.ReceivedFromPeer = from
	}
}

func WithTraceCondition(cond func(tx *transaction.Transaction, src TransactionSource, rcv peer.ID) bool) TransactionInOption {
	panic("not implemented")
	//return func(data *TransactionInputData) {
	//	data.traceFlag = cond(data.tx, data.source, data.receivedFromPeer)
	//}
}
