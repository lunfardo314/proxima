package pull_client

import (
	"context"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/queues/txinput"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/queue"
	"github.com/lunfardo314/proxima/util/set"
	"go.uber.org/zap/zapcore"
)

const pullPeriod = 500 * time.Millisecond

type (
	Environment interface {
		TxBytesStore() global.TxBytesStore
		QueryTransactionsFromRandomPeer(lst ...ledger.TransactionID) bool
		TransactionIn(txBytes []byte, opts ...txinput.TransactionInOption) (*transaction.Transaction, error)
		Tracef(tag string, format string, args ...any)
	}

	Input struct {
		TxIDs []ledger.TransactionID
	}

	PullClient struct {
		*queue.Queue[*Input]
		env                    Environment
		stopBackgroundLoopChan chan struct{}
		// set of transaction being pulled
		mutex    sync.RWMutex
		pullList map[ledger.TransactionID]time.Time
		// list of removed transactions
		toRemoveSetMutex sync.RWMutex
		toRemoveSet      set.Set[ledger.TransactionID]
	}
)

const chanBufferSize = 10

func New(env Environment, lvl zapcore.Level) *PullClient {
	return &PullClient{
		Queue:                  queue.NewQueueWithBufferSize[*Input]("pullClient", chanBufferSize, lvl, nil),
		env:                    env,
		pullList:               make(map[ledger.TransactionID]time.Time),
		toRemoveSet:            set.New[ledger.TransactionID](),
		stopBackgroundLoopChan: make(chan struct{}),
	}
}

func (q *PullClient) Start(ctx context.Context, doneOnClose *sync.WaitGroup) {
	q.AddOnClosed(func() {
		doneOnClose.Done()
	})
	q.Queue.Start(q, ctx)
}

func (q *PullClient) Consume(inp *Input) {
	q.env.Tracef("pull", "Consume: (%s) %s", len(inp.TxIDs), inp.TxIDs[0].StringShort())

	toPull := make([]ledger.TransactionID, 0)
	txBytesList := make([][]byte, 0)

	q.mutex.Lock()
	defer q.mutex.Unlock()

	nextPull := time.Now().Add(pullPeriod)
	for _, txid := range inp.TxIDs {
		if _, already := q.pullList[txid]; already {
			continue
		}
		if txBytes := q.env.TxBytesStore().GetTxBytes(&txid); len(txBytes) > 0 {
			//q.tracePull("%s fetched from txBytesStore", func() any { return txid.StringShort() })
			txBytesList = append(txBytesList, txBytes)
		} else {
			//q.tracePull("%s added to pull list, pull list size: %d", func() any { return txid.StringShort() }, len(q.pullList))
			q.pullList[txid] = nextPull
			toPull = append(toPull, txid)
		}
	}
	go q.transactionInMany(txBytesList)
	go q.env.QueryTransactionsFromRandomPeer(toPull...)
}

func (q *PullClient) transactionInMany(txBytesList [][]byte) {
	for _, txBytes := range txBytesList {
		_, err := q.env.TransactionIn(txBytes, txinput.WithTransactionSource(txinput.TransactionSourceStore))
		if err != nil {
			q.Log().Errorf("pull:TransactionIn returned: '%v'", err)
		}
		//q.tracePull("%s -> TransactionIn", tx.IDShortString())
	}
}

const pullLoopPeriod = 50 * time.Millisecond

func (q *PullClient) backgroundLoop() {
	defer q.Log().Infof("background loop stopped")

	buffer := make([]ledger.TransactionID, 0) // minimize heap use
	for {
		select {
		case <-q.stopBackgroundLoopChan:
			return
		case <-time.After(pullLoopPeriod):
		}
		q.pullAllMatured(buffer)
	}
}

func (q *PullClient) pullAllMatured(buf []ledger.TransactionID) {
	buf = util.ClearSlice(buf)
	toRemove := q.toRemoveSetClone()

	q.mutex.Lock()
	defer q.mutex.Unlock()

	toRemove.ForEach(func(removeTxID ledger.TransactionID) bool {
		delete(q.pullList, removeTxID)
		return true
	})

	nowis := time.Now()
	nextDeadline := nowis.Add(pullPeriod)
	for txid, deadline := range q.pullList {
		if nowis.After(deadline) {
			buf = append(buf, txid)
			q.pullList[txid] = nextDeadline
		}
	}
	if len(buf) > 0 {
		q.env.QueryTransactionsFromRandomPeer(buf...)
	}
}

func (q *PullClient) StopPulling(txid *ledger.TransactionID) {
	q.toRemoveSetMutex.Lock()
	defer q.toRemoveSetMutex.Unlock()

	q.toRemoveSet.Insert(*txid)
	//q.tracePull("StopPulling: %s. pull list size: %d", func() any { return txid.StringShort() }, len(q.pullList))
}

func (q *PullClient) toRemoveSetClone() set.Set[ledger.TransactionID] {
	q.toRemoveSetMutex.Lock()
	defer q.toRemoveSetMutex.Unlock()

	ret := q.toRemoveSet
	q.toRemoveSet = set.New[ledger.TransactionID]()
	return ret
}
