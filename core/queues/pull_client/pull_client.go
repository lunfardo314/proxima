package pull_client

import (
	"context"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/queues/txinput"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/queue"
	"github.com/lunfardo314/proxima/util/set"
)

const pullPeriod = 500 * time.Millisecond

type (
	Environment interface {
		global.Logging
		global.TxBytesGet
		QueryTransactionsFromRandomPeer(lst ...ledger.TransactionID) bool
		TxBytesIn(txBytes []byte, opts ...txinput.TransactionInOption) (*ledger.TransactionID, error)
	}

	Input struct {
		TxIDs []ledger.TransactionID
	}

	PullClient struct {
		*queue.Queue[*Input]
		Environment
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

func New(env Environment) *PullClient {
	return &PullClient{
		Queue:                  queue.NewQueueWithBufferSize[*Input]("pullClient", chanBufferSize, env.Log().Level(), nil),
		Environment:            env,
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
	q.Tracef("pull", "Consume: (%s) %s", len(inp.TxIDs), inp.TxIDs[0].StringShort())

	toPull := make([]ledger.TransactionID, 0)
	txBytesList := make([][]byte, 0)

	q.mutex.Lock()
	defer q.mutex.Unlock()

	nextPull := time.Now().Add(pullPeriod)
	for _, txid := range inp.TxIDs {
		if _, already := q.pullList[txid]; already {
			continue
		}
		if txBytesWithMetadata := q.GetTxBytesWithMetadata(&txid); len(txBytesWithMetadata) > 0 {
			q.Tracef("pull", "%s fetched from txBytesStore", txid.StringShort)
			txBytesList = append(txBytesList, txBytesWithMetadata)
		} else {
			q.pullList[txid] = nextPull
			q.Tracef("pull", "%s added to the pull list. Pull list size: %d", txid.StringShort, len(q.pullList))
			toPull = append(toPull, txid)
		}
	}
	go q.transactionInMany(txBytesList)
	go q.QueryTransactionsFromRandomPeer(toPull...)
}

func (q *PullClient) transactionInMany(txBytesList [][]byte) {
	for _, txBytesWithMetadata := range txBytesList {
		metadataBytes, txBytes, err := txmetadata.SplitBytesWithMetadata(txBytesWithMetadata)
		if err != nil {
			q.Environment.Log().Errorf("pull: error while parsing tx metadata: '%v'", err)
			continue
		}
		metadata, err := txmetadata.TransactionMetadataFromBytes(metadataBytes)
		if err != nil {
			q.Environment.Log().Errorf("pull: error while parsing tx metadata: '%v'", err)
			continue
		}
		if metadata == nil {
			metadata = &txmetadata.TransactionMetadata{}
		}
		metadata.SourceTypeNonPersistent = txmetadata.SourceTypeTxStore
		if _, err = q.TxBytesIn(txBytes, txinput.WithMetadata(metadata)); err != nil {
			q.Environment.Log().Errorf("pull: tx parse error while pull: '%v'", err)
		}
	}
}

const pullLoopPeriod = 50 * time.Millisecond

func (q *PullClient) backgroundLoop() {
	defer q.Environment.Log().Infof("background loop stopped")

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
		q.QueryTransactionsFromRandomPeer(buf...)
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
