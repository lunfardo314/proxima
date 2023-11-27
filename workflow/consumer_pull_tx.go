package workflow

import (
	"fmt"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
)

const PullTxConsumerName = "pulltx"

const pullPeriod = 500 * time.Millisecond

const (
	PullTxCmdQuery = byte(iota)
	PullTxCmdRemove
)

type (
	PullTxData struct {
		Cmd  byte
		TxID core.TransactionID
		// InitialDelay for PullTxCmdQuery, how long delay first pull.
		// It may not be needed at all if transaction comes through gossip
		// 0 means first pull immediately
		InitialDelay time.Duration
	}

	PullTxConsumer struct {
		*Consumer[*PullTxData]
		mutex sync.RWMutex

		stopBackgroundLoopChan chan struct{}
		// txid -> next pull deadline
		wanted map[core.TransactionID]time.Time
	}
)

func (w *Workflow) initPullConsumer() {
	c := &PullTxConsumer{
		Consumer:               NewConsumer[*PullTxData](PullTxConsumerName, w),
		wanted:                 make(map[core.TransactionID]time.Time),
		stopBackgroundLoopChan: make(chan struct{}),
	}
	c.AddOnConsume(func(data *PullTxData) {
		if data.Cmd == PullTxCmdQuery {
			c.Log().Infof("PULL (query) %s, delay: %v", data.TxID.StringShort(), data.InitialDelay)
		} else {
			c.Log().Infof("PULL (remove) %s, delay: %v", data.TxID.StringShort(), data.InitialDelay)
		}
	})
	c.AddOnConsume(c.consume)
	c.AddOnClosed(func() {
		c.stop()
		w.terminateWG.Done()
	})
	w.pullConsumer = c
	go c.backgroundLoop()
}

func (p *PullTxConsumer) already(txid *core.TransactionID) bool {
	p.mutex.RLock()
	defer p.mutex.RUnlock()

	_, already := p.wanted[*txid]
	return already
}

func (p *PullTxConsumer) consume(inp *PullTxData) {
	switch inp.Cmd {
	case PullTxCmdQuery:
		p.queryTransactionCmd(inp)
	case PullTxCmdRemove:
		p.removeTransactionCmd(inp)
	default:
		p.Log().Panicf("wrong command")
	}
}

func (p *PullTxConsumer) queryTransactionCmd(inp *PullTxData) {
	if p.already(&inp.TxID) {
		return
	}
	p.mutex.Lock()
	defer p.mutex.Unlock()

	// look up for the transaction in the store
	txBytes := p.glb.utxoTangle.TxBytesStore().GetTxBytes(&inp.TxID)
	if len(txBytes) != 0 {
		// transaction bytes are in the transaction store. No need to query it from another peer
		if err := p.glb.TransactionIn(txBytes, WithTransactionSourceType(TransactionSourceTypeStore)); err != nil {
			p.Log().Errorf("invalid transaction from txStore %s: '%v'", inp.TxID.StringShort(), err)
		}
		return
	}
	// transaction is not in the store. Add it to the 'wanted' set
	nowis := time.Now()
	firstPullDeadline := nowis.Add(inp.InitialDelay)
	if inp.InitialDelay == 0 {
		firstPullDeadline = nowis.Add(pullPeriod)
	}
	p.wanted[inp.TxID] = firstPullDeadline
	if inp.InitialDelay == 0 {
		// query immediately
		go p.pullTransactions(inp.TxID)
	}

	p.Log().Debugf("<-- added %s", inp.TxID.StringShort())
}

func (p *PullTxConsumer) removeTransactionCmd(inp *PullTxData) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	delete(p.wanted, inp.TxID)

	p.Log().Debugf("removed %s", inp.TxID.StringShort())
}

func (p *PullTxConsumer) stop() {
	close(p.stopBackgroundLoopChan)
}

const pullLoopPeriod = 10 * time.Millisecond

func (p *PullTxConsumer) backgroundLoop() {
	defer p.Log().Infof("background loop stopped")

	for {
		select {
		case <-p.stopBackgroundLoopChan:
			return
		case <-time.After(pullLoopPeriod):
		}
		p.pullAllMatured()
	}
}

func (p *PullTxConsumer) pullAllMatured() {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	nowis := time.Now()

	txids := make([]core.TransactionID, 0)
	for txid, whenNext := range p.wanted {
		if whenNext.Before(nowis) {
			txids = append(txids, txid)
		}
		p.wanted[txid] = nowis.Add(pullPeriod)
	}
	if len(txids) > 0 {
		p.pullTransactions(txids...)
	}
}

func (p *PullTxConsumer) pullTransactions(txids ...core.TransactionID) {
	fmt.Printf(">>>>>>>>>>>>>>>> pullTransactions %d\n", len(txids))
	p.glb.peers.PullTransactionsFromRandomPeer(txids...)
}
