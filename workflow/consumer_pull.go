package workflow

import (
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/peering"
	"go.uber.org/atomic"
)

const PullTxConsumerName = "pull"

const pullPeriod = 500 * time.Millisecond

const (
	PullTxCmdQuery = byte(iota)
	PullTxCmdRemove
)

type (
	PullData struct {
		Cmd  byte
		TxID core.TransactionID
		// GracePeriod for PullTxCmdQuery, how long delay first pull.
		// It may not be needed at all if transaction comes through gossip
		// 0 means first pull immediately
		GracePeriod time.Duration
	}

	PullConsumer struct {
		*Consumer[*PullData]
		mutex sync.RWMutex

		stopBackgroundLoop atomic.Bool
		// txid -> next pull deadline
		wanted map[core.TransactionID]time.Time
		peers  peering.Peers
	}
)

func (w *Workflow) initPullConsumer() {
	c := &PullConsumer{
		Consumer: NewConsumer[*PullData](PullTxConsumerName, w),
		wanted:   make(map[core.TransactionID]time.Time),
		peers:    peering.NewDummyPeering(),
	}
	c.AddOnConsume(c.consume)
	c.AddOnClosed(func() {
		c.stop()
		w.terminateWG.Done()
	})
	w.pullConsumer = c
	go c.backgroundLoop()
}

func (p *PullConsumer) already(txid *core.TransactionID) bool {
	p.mutex.RLock()
	defer p.mutex.RUnlock()

	_, already := p.wanted[*txid]
	return already
}

func (p *PullConsumer) consume(inp *PullData) {
	switch inp.Cmd {
	case PullTxCmdQuery:
		p.queryTransactionCmd(inp)
	case PullTxCmdRemove:
		p.removeTransactionCmd(inp)
	default:
		p.Log().Panicf("wrong command")
	}
}

func (p *PullConsumer) queryTransactionCmd(inp *PullData) {
	if p.already(&inp.TxID) {
		return
	}
	p.mutex.Lock()
	defer p.mutex.Unlock()

	// look up for the transaction in the store
	txBytes := p.glb.utxoTangle.TxBytesStore().GetTxBytes(&inp.TxID)
	if len(txBytes) != 0 {
		// transaction bytes are in the transaction store. No need to query it from another peer
		if err := p.glb.TransactionIn(txBytes, WithTransactionSource(TransactionSourceStore)); err != nil {
			p.Log().Errorf("invalid transaction from txStore %s: '%v'", inp.TxID.StringShort(), err)
		}
		return
	}
	// transaction is not in the store. Add it to the 'wanted' set
	nowis := time.Now()
	firstPullDeadline := nowis.Add(inp.GracePeriod)
	if inp.GracePeriod == 0 {
		firstPullDeadline = nowis.Add(pullPeriod)
	}
	p.wanted[inp.TxID] = firstPullDeadline
	if inp.GracePeriod == 0 {
		// query immediately
		go p.pullTransactions(inp.TxID)
	}

	p.Log().Debugf("<-- added %s", inp.TxID.StringShort())
}

func (p *PullConsumer) removeTransactionCmd(inp *PullData) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	delete(p.wanted, inp.TxID)

	p.Log().Debugf("removed %s", inp.TxID.StringShort())
}

func (p *PullConsumer) stop() {
	p.stopBackgroundLoop.Store(true)
}

func (p *PullConsumer) backgroundLoop() {
	for !p.stopBackgroundLoop.Load() {
		time.Sleep(10 * time.Millisecond)

		p.pullAllMatured()
	}
	p.Log().Infof("background loop stopped")
}

func (p *PullConsumer) pullAllMatured() {
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
	p.pullTransactions(txids...)
}

func (p *PullConsumer) pullTransactions(txids ...core.TransactionID) {
	peer := p.peers.SelectRandomPeer()
	peer.SendMsgBytes(peering.EncodePeerMessageTypeQueryTransactions(txids...))
}
