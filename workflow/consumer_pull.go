package workflow

import (
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/peering"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"go.uber.org/atomic"
)

const PullConsumerName = "pull"

const (
	PullCmdQuery = byte(iota)
	PullCmdRemove
)

type (
	PullData struct {
		Cmd  byte
		TxID core.TransactionID
	}

	PullConsumer struct {
		*Consumer[*PullData]
		mutex sync.RWMutex

		stopBackgroundLoop atomic.Bool
		wanted             map[core.TransactionID]time.Time
		peers              peering.Peers
	}
)

func (w *Workflow) initPullConsumer() {
	c := &PullConsumer{
		Consumer: NewConsumer[*PullData](PullConsumerName, w),
		wanted:   make(map[core.TransactionID]time.Time),
		peers:    peering.NewDummyPeering(),
	}
	c.AddOnConsume(c.consume)
	c.AddOnClosed(func() {
		c.stop()
		w.terminateWG.Done()
	})
	w.pullConsumer = c

}

func (p *PullConsumer) already(txid *core.TransactionID) bool {
	p.mutex.RLock()
	defer p.mutex.RUnlock()

	_, already := p.wanted[*txid]
	return already
}

func (p *PullConsumer) consume(inp *PullData) {
	switch inp.Cmd {
	case PullCmdQuery:
		p.queryTransactionCmd(inp)
	case PullCmdRemove:
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
		if err := p.glb.TransactionIn(txBytes); err != nil {
			p.Log().Errorf("invalid transaction from txStore %s: '%v'", inp.TxID.Short(), err)
		}
		return
	}
	// transaction is not in the store. Add it to the 'wanted' set
	p.wanted[inp.TxID] = time.Time{}
	p.Log().Debugf("<-- added %s", inp.TxID.Short())
}

func (p *PullConsumer) removeTransactionCmd(inp *PullData) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	delete(p.wanted, inp.TxID)

	p.Log().Debugf("removed %s", inp.TxID.Short())
}

func (p *PullConsumer) stop() {
	p.stopBackgroundLoop.Store(true)
}

const pullPeriod = 10 * time.Millisecond

func (p *PullConsumer) selectTransactionsToPull() set.Set[core.TransactionID] {
	ret := set.New[core.TransactionID]()
	nowis := time.Now()

	p.mutex.RLock()
	defer p.mutex.RUnlock()

	for txid, whenLast := range p.wanted {
		if whenLast.Add(pullPeriod).Before(nowis) {
			ret.Insert(txid)
		}
	}
	return ret
}

func (p *PullConsumer) backgroundPullLoop() {
	for !p.stopBackgroundLoop.Load() {
		time.Sleep(100 * time.Millisecond) // TODO temporary

		toPull := p.selectTransactionsToPull()
		if len(toPull) == 0 {
			continue
		}
		p.pullTransactions(toPull)
	}
}

func (p *PullConsumer) pullTransactions(m set.Set[core.TransactionID]) {
	peer := p.peers.SelectRandomPeer()
	peer.SendMsgBytes(peering.EncodePeerMessageTypeQueryTransactions(util.Keys(m)...))
}
