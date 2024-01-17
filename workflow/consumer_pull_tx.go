package workflow

import (
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
)

const PullTxConsumerName = "pulltx"

const pullPeriod = 500 * time.Millisecond

type (
	PullTxData struct {
		TxIDs []ledger.TransactionID
	}

	PullTxConsumer struct {
		*Consumer[*PullTxData]
		stopBackgroundLoopChan chan struct{}
		// set of transaction being pulled
		mutex    sync.RWMutex
		pullList map[ledger.TransactionID]time.Time
		// list of removed transactions
		toRemoveSetMutex sync.RWMutex
		toRemoveSet      set.Set[ledger.TransactionID]
	}
)

func (w *Workflow) initPullConsumer() {
	w.pullConsumer = &PullTxConsumer{
		Consumer:               NewConsumer[*PullTxData](PullTxConsumerName, w),
		pullList:               make(map[ledger.TransactionID]time.Time),
		toRemoveSet:            set.New[ledger.TransactionID](),
		stopBackgroundLoopChan: make(chan struct{}),
	}
	w.pullConsumer.AddOnConsume(w.pullConsumer.consume)
	w.pullConsumer.AddOnClosed(func() {
		close(w.pullConsumer.stopBackgroundLoopChan)
	})

	go w.pullConsumer.backgroundLoop()
}

func (c *PullTxConsumer) consume(inp *PullTxData) {
	toPull := make([]ledger.TransactionID, 0)
	txBytesList := make([][]byte, 0)

	c.mutex.Lock()
	defer c.mutex.Unlock()

	nextPull := time.Now().Add(pullPeriod)
	for _, txid := range inp.TxIDs {
		if _, already := c.pullList[txid]; already {
			continue
		}
		if txBytes := c.glb.txBytesStore.GetTxBytes(&txid); len(txBytes) > 0 {
			c.tracePull("%s fetched from txBytesStore", func() any { return txid.StringShort() })
			txBytesList = append(txBytesList, txBytes)
		} else {
			c.tracePull("%s added to pull list, pull list size: %d", func() any { return txid.StringShort() }, len(c.pullList))
			c.pullList[txid] = nextPull
			toPull = append(toPull, txid)
		}
	}
	go c.transactionInMany(txBytesList)
	go c.glb.peers.PullTransactionsFromRandomPeer(toPull...)
}

func (c *PullTxConsumer) transactionInMany(txBytesList [][]byte) {
	for _, txBytes := range txBytesList {
		tx, err := c.glb.TransactionInReturnTx(txBytes,
			WithTransactionSource(TransactionSourceStore),
			WithTraceCondition(func(_ *transaction.Transaction, _ TransactionSource, _ peer.ID) bool {
				return global.TraceTxEnabled()
			}),
		)
		if err != nil {
			c.Log().Errorf("pull:TxBytesIn returned: '%v'", err)
		}
		c.tracePull("%s -> TxBytesIn", tx.IDShortString())
	}
}

const pullLoopPeriod = 50 * time.Millisecond

func (c *PullTxConsumer) backgroundLoop() {
	defer c.Log().Infof("background loop stopped")

	buffer := make([]ledger.TransactionID, 0) // minimize heap use
	for {
		select {
		case <-c.stopBackgroundLoopChan:
			return
		case <-time.After(pullLoopPeriod):
		}
		c.pullAllMatured(buffer)
	}
}

func (c *PullTxConsumer) pullAllMatured(buf []ledger.TransactionID) {
	buf = util.ClearSlice(buf)
	toRemove := c.toRemoveSetClone()

	c.mutex.Lock()
	defer c.mutex.Unlock()

	toRemove.ForEach(func(removeTxID ledger.TransactionID) bool {
		delete(c.pullList, removeTxID)
		return true
	})

	nowis := time.Now()
	nextDeadline := nowis.Add(pullPeriod)
	for txid, deadline := range c.pullList {
		if nowis.After(deadline) {
			buf = append(buf, txid)
			c.pullList[txid] = nextDeadline
		}
	}
	if len(buf) > 0 {
		c.glb.peers.PullTransactionsFromRandomPeer(buf...)
	}
}

func (c *PullTxConsumer) stopPulling(txid *ledger.TransactionID) {
	c.toRemoveSetMutex.Lock()
	defer c.toRemoveSetMutex.Unlock()

	c.toRemoveSet.Insert(*txid)
	c.tracePull("stopPulling: %s. pull list size: %d", func() any { return txid.StringShort() }, len(c.pullList))
}

func (c *PullTxConsumer) toRemoveSetClone() set.Set[ledger.TransactionID] {
	c.toRemoveSetMutex.Lock()
	defer c.toRemoveSetMutex.Unlock()

	ret := c.toRemoveSet
	c.toRemoveSet = set.New[ledger.TransactionID]()
	return ret
}

func (c *PullTxConsumer) isInPullList(txid *ledger.TransactionID) (ret bool) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	_, ret = c.pullList[*txid]
	return
}

func (c *PullTxConsumer) isInToRemoveSet(txid *ledger.TransactionID) (ret bool) {
	c.toRemoveSetMutex.RLock()
	defer c.toRemoveSetMutex.RUnlock()

	return c.toRemoveSet.Contains(*txid)
}

func (c *PullTxConsumer) isBeingPulled(txid *ledger.TransactionID) bool {
	return !c.isInToRemoveSet(txid) && c.isInPullList(txid)
}

func (c *PullTxConsumer) pullListLen() int {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return len(c.pullList)
}

func (w *Workflow) PullListLen() int {
	return w.pullConsumer.pullListLen()
}
