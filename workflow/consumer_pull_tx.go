package workflow

import (
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/util"
)

const PullTxConsumerName = "pulltx"

const pullPeriod = 500 * time.Millisecond

type (
	PullTxData struct {
		TxIDs []core.TransactionID
		// InitialDelay for PullTxCmdQuery, how long delay first pull.
		// It may not be needed at all if transaction comes through gossip
		// 0 means first pull immediately
		InitialDelay time.Duration
	}

	PullTxConsumer struct {
		*Consumer[*PullTxData]
		stopBackgroundLoopChan chan struct{}
		mutex                  sync.RWMutex
		// txid -> next pull deadline
		pullList map[core.TransactionID]pullInfo
	}

	pullInfo struct {
		deadline time.Time
		stopped  bool
	}
)

func (w *Workflow) initPullConsumer() {
	w.pullConsumer = &PullTxConsumer{
		Consumer:               NewConsumer[*PullTxData](PullTxConsumerName, w),
		pullList:               make(map[core.TransactionID]pullInfo),
		stopBackgroundLoopChan: make(chan struct{}),
	}
	w.pullConsumer.AddOnConsume(w.pullConsumer.consume)
	w.pullConsumer.AddOnClosed(func() {
		close(w.pullConsumer.stopBackgroundLoopChan)
	})

	go w.pullConsumer.backgroundLoop()
}

func (c *PullTxConsumer) consume(inp *PullTxData) {
	toPull := make([]core.TransactionID, 0)

	c.mutex.Lock()
	defer c.mutex.Unlock()

	for _, txid := range inp.TxIDs {
		needsPull, txBytes := c.pullOne(txid, inp.InitialDelay)
		if len(txBytes) > 0 {
			util.Assertf(!needsPull, "inconsistency: !needsPull")

			err := c.glb.TransactionIn(txBytes,
				WithTransactionSource(TransactionSourceStore),
				WithTraceCondition(func(_ *transaction.Transaction, _ TransactionSource, _ peer.ID) bool {
					return global.TraceTxEnabled()
				}),
				WithTransactionAlreadyRemovedFromPuller,
			)
			if err != nil {
				c.Log().Errorf("pull:TransactionIn returned: '%v'", err)
			}
			c.tracePull("%s -> TransactionIn", txid.StringShort())
			continue
		}
		if needsPull {
			toPull = append(toPull, txid)
		}
	}
	go c.glb.peers.PullTransactionsFromRandomPeer(toPull...)
}

func (c *PullTxConsumer) pullOne(txid core.TransactionID, initialDelay time.Duration) (bool, []byte) {
	if _, already := c.pullList[txid]; already {
		return false, nil
	}
	// look up for the transaction in the store
	if txBytes := c.glb.txBytesStore.GetTxBytes(&txid); len(txBytes) > 0 {
		// transaction bytes are in the transaction store. No need to query it from another peer
		c.tracePull("%s fetched from txBytesStore", func() any { return txid.StringShort() })
		return false, txBytes
	}
	// transaction is not in the store. Add it to the 'pullList' set
	nowis := time.Now()
	firstPullDeadline := nowis.Add(initialDelay)
	if initialDelay == 0 {
		firstPullDeadline = nowis.Add(pullPeriod)
	}

	c.pullList[txid] = pullInfo{deadline: firstPullDeadline}
	c.tracePull("%s addedToPullList, pull list size: %d", func() any { return txid.StringShort() }, len(c.pullList))
	return initialDelay == 0, nil
}

func (c *PullTxConsumer) pullListLen() int {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return len(c.pullList)
}

func (w *Workflow) PullListLen() int {
	return w.pullConsumer.pullListLen()
}

func (c *PullTxConsumer) removeFromPullList(txid *core.TransactionID) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	delete(c.pullList, *txid)
	c.tracePull("removeFromPullList: %s, list size: %d",
		func() any { return txid.StringShort() }, len(c.pullList))
}

func (c *PullTxConsumer) stopPulling(txid *core.TransactionID) bool {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	_, inTheList := c.pullList[*txid]
	if inTheList {
		c.pullList[*txid] = pullInfo{stopped: true}
	}
	c.tracePull("stopPulling: %s. Found: %v, list size: %d",
		func() any { return txid.StringShort() }, inTheList, len(c.pullList))
	return inTheList
}

const pullLoopPeriod = 10 * time.Millisecond

func (c *PullTxConsumer) backgroundLoop() {
	defer c.Log().Infof("background loop stopped")

	buffer := make([]core.TransactionID, 0) // minimize heap use
	for {
		select {
		case <-c.stopBackgroundLoopChan:
			return
		case <-time.After(pullLoopPeriod):
		}
		c.pullAllMatured(buffer)
	}
}

func (c *PullTxConsumer) pullAllMatured(buf []core.TransactionID) {
	buf = util.ClearSlice(buf)

	c.mutex.Lock()
	defer c.mutex.Unlock()

	nowis := time.Now()

	for txid, info := range c.pullList {
		if info.stopped {
			continue
		}
		if nowis.After(info.deadline) {
			buf = append(buf, txid)
			c.pullList[txid] = pullInfo{deadline: nowis.Add(pullPeriod)}
		}
	}
	if len(buf) > 0 {
		c.glb.peers.PullTransactionsFromRandomPeer(buf...)
	}
}
