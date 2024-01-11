package pull

import (
	"context"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/queue"
	"github.com/lunfardo314/proxima/util/set"
	"go.uber.org/zap"
)

const pullPeriod = 500 * time.Millisecond

type (
	Environment interface {
		TxBytesStore() global.TxBytesStore
		QueryTransactionsFromRandomPeer(lst ...ledger.TransactionID)
		TransactionIn(txBytes []byte) (*transaction.Transaction, error)
	}

	InputData struct {
		TxIDs []ledger.TransactionID
	}

	RequestQueue struct {
		*queue.Queue[*InputData]
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

func StartPullRequestQueue(ctx context.Context, env Environment) *RequestQueue {
	ret := &RequestQueue{
		Queue:                  queue.NewConsumerWithBufferSize[*InputData]("pullReq", chanBufferSize, zap.InfoLevel, nil),
		env:                    env,
		pullList:               make(map[ledger.TransactionID]time.Time),
		toRemoveSet:            set.New[ledger.TransactionID](),
		stopBackgroundLoopChan: make(chan struct{}),
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

func (q *RequestQueue) consume(inp *InputData) {
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

func (q *RequestQueue) transactionInMany(txBytesList [][]byte) {
	for _, txBytes := range txBytesList {
		tx, err := q.env.TransactionIn(txBytes,
			WithTransactionSource(TransactionSourceStore),
			WithTraceCondition(func(_ *transaction.Transaction, _ TransactionSource, _ peer.ID) bool {
				return global.TraceTxEnabled()
			}),
		)
		if err != nil {
			q.Log().Errorf("pull:TransactionIn returned: '%v'", err)
		}
		q.tracePull("%s -> TransactionIn", tx.IDShortString())
	}
}

const pullLoopPeriod = 50 * time.Millisecond

func (q *RequestQueue) backgroundLoop() {
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

func (q *RequestQueue) pullAllMatured(buf []ledger.TransactionID) {
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
		q.glb.peers.PullTransactionsFromRandomPeer(buf...)
	}
}

func (q *RequestQueue) stopPulling(txid *ledger.TransactionID) {
	q.toRemoveSetMutex.Lock()
	defer q.toRemoveSetMutex.Unlock()

	q.toRemoveSet.Insert(*txid)
	q.tracePull("stopPulling: %s. pull list size: %d", func() any { return txid.StringShort() }, len(q.pullList))
}

func (q *RequestQueue) toRemoveSetClone() set.Set[ledger.TransactionID] {
	q.toRemoveSetMutex.Lock()
	defer q.toRemoveSetMutex.Unlock()

	ret := q.toRemoveSet
	q.toRemoveSet = set.New[ledger.TransactionID]()
	return ret
}

func (q *RequestQueue) isInPullList(txid *ledger.TransactionID) (ret bool) {
	q.mutex.RLock()
	defer q.mutex.RUnlock()

	_, ret = q.pullList[*txid]
	return
}

func (q *RequestQueue) isInToRemoveSet(txid *ledger.TransactionID) (ret bool) {
	q.toRemoveSetMutex.RLock()
	defer q.toRemoveSetMutex.RUnlock()

	return q.toRemoveSet.Contains(*txid)
}

func (q *RequestQueue) isBeingPulled(txid *ledger.TransactionID) bool {
	return !q.isInToRemoveSet(txid) && q.isInPullList(txid)
}

func (q *RequestQueue) pullListLen() int {
	q.mutex.RLock()
	defer q.mutex.RUnlock()

	return len(q.pullList)
}

func (w *Workflow) PullListLen() int {
	return w.pullConsumer.pullListLen()
}
