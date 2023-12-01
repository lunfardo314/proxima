package workflow

import (
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/util"
)

const PullTxConsumerName = "pulltx"

const pullPeriod = 500 * time.Millisecond

type (
	PullTxData struct {
		TxID *core.TransactionID
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
	ret := &PullTxConsumer{
		Consumer:               NewConsumer[*PullTxData](PullTxConsumerName, w),
		pullList:               make(map[core.TransactionID]pullInfo),
		stopBackgroundLoopChan: make(chan struct{}),
	}
	ret.AddOnConsume(ret.consume)
	ret.AddOnClosed(func() {
		close(ret.stopBackgroundLoopChan)
	})
	w.pullConsumer = ret
	go ret.backgroundLoop()
}

func (c *PullTxConsumer) consume(inp *PullTxData) {
	if c.isInPullList(inp.TxID) {
		return
	}
	// look up for the transaction in the store
	txBytes := c.glb.utxoTangle.TxBytesStore().GetTxBytes(inp.TxID)
	if len(txBytes) != 0 {
		// transaction bytes are in the transaction store. No need to query it from another peer
		if err := c.glb.TransactionIn(txBytes, WithTransactionSourceType(TransactionSourceTypeStore)); err != nil {
			c.Log().Errorf("invalid transaction from txStore %s: '%v'", inp.TxID.StringShort(), err)
		}
		return
	}
	// transaction is not in the store. Add it to the 'pullList' set
	nowis := time.Now()
	firstPullDeadline := nowis.Add(inp.InitialDelay)
	if inp.InitialDelay == 0 {
		firstPullDeadline = nowis.Add(pullPeriod)
	}

	c.addToPullList(inp.TxID, firstPullDeadline)

	if inp.InitialDelay == 0 {
		// query immediately
		c.glb.peers.PullTransactionsFromRandomPeer(*inp.TxID)
	}
}

func (c *PullTxConsumer) isInPullList(txid *core.TransactionID) bool {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	_, already := c.pullList[*txid]
	return already
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

	c.tracePull("removeFromPullList: %s", func() any { return txid.StringShort() })
	delete(c.pullList, *txid)
}

func (c *PullTxConsumer) stopPulling(txid *core.TransactionID) bool {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.tracePull("stopPulling: %s", func() any { return txid.StringShort() })

	_, inTheList := c.pullList[*txid]
	if inTheList {
		c.pullList[*txid] = pullInfo{stopped: true}
	}
	return inTheList
}

func (c *PullTxConsumer) addToPullList(txid *core.TransactionID, deadline time.Time) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.pullList[*txid] = pullInfo{deadline: deadline}
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
