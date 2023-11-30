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

func (p *PullTxConsumer) consume(inp *PullTxData) {
	if p.isInPullList(inp.TxID) {
		return
	}
	// look up for the transaction in the store
	txBytes := p.glb.utxoTangle.TxBytesStore().GetTxBytes(inp.TxID)
	if len(txBytes) != 0 {
		// transaction bytes are in the transaction store. No need to query it from another peer
		if err := p.glb.TransactionIn(txBytes, WithTransactionSourceType(TransactionSourceTypeStore)); err != nil {
			p.Log().Errorf("invalid transaction from txStore %s: '%v'", inp.TxID.StringShort(), err)
		}
		return
	}
	// transaction is not in the store. Add it to the 'pullList' set
	nowis := time.Now()
	firstPullDeadline := nowis.Add(inp.InitialDelay)
	if inp.InitialDelay == 0 {
		firstPullDeadline = nowis.Add(pullPeriod)
	}

	p.addToPullList(inp.TxID, firstPullDeadline)

	if inp.InitialDelay == 0 {
		// query immediately
		p.glb.peers.PullTransactionsFromRandomPeer(*inp.TxID)
	}
}

func (p *PullTxConsumer) isInPullList(txid *core.TransactionID) bool {
	p.mutex.RLock()
	defer p.mutex.RUnlock()

	_, already := p.pullList[*txid]
	return already
}

func (p *PullTxConsumer) pullListLen() int {
	p.mutex.RLock()
	defer p.mutex.RUnlock()

	return len(p.pullList)
}

func (w *Workflow) PullListLen() int {
	return w.pullConsumer.pullListLen()
}

func (p *PullTxConsumer) removeFromPullList(txid *core.TransactionID) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	p.Log().Infof(">>>>>>>>>>>>> removeFromPullList: %s", txid.StringShort())

	delete(p.pullList, *txid)
}

func (p *PullTxConsumer) stopPulling(txid *core.TransactionID) bool {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	p.Log().Infof(">>>>>>>>>>>>> stopPulling: %s", txid.StringShort())

	_, inTheList := p.pullList[*txid]
	if inTheList {
		p.pullList[*txid] = pullInfo{stopped: true}
	}
	return inTheList
}

func (p *PullTxConsumer) addToPullList(txid *core.TransactionID, deadline time.Time) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	p.pullList[*txid] = pullInfo{deadline: deadline}
}

const pullLoopPeriod = 10 * time.Millisecond

func (p *PullTxConsumer) backgroundLoop() {
	defer p.Log().Infof("background loop stopped")

	buffer := make([]core.TransactionID, 0) // minimize heap use
	for {
		select {
		case <-p.stopBackgroundLoopChan:
			return
		case <-time.After(pullLoopPeriod):
		}
		p.pullAllMatured(buffer)
	}
}

func (p *PullTxConsumer) pullAllMatured(buf []core.TransactionID) {
	buf = util.ClearSlice(buf)

	p.mutex.Lock()
	defer p.mutex.Unlock()

	nowis := time.Now()

	for txid, info := range p.pullList {
		if info.stopped {
			continue
		}
		if nowis.After(info.deadline) {
			buf = append(buf, txid)
			p.pullList[txid] = pullInfo{deadline: nowis.Add(pullPeriod)}
		}
	}
	if len(buf) > 0 {
		p.glb.peers.PullTransactionsFromRandomPeer(buf...)
	}
}
