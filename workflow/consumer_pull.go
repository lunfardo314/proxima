package workflow

import (
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
	"go.uber.org/atomic"
)

const PullConsumerName = "pull"

type (
	PullData struct {
		TxID core.TransactionID
	}

	PullConsumer struct {
		*Consumer[*PullData]
		mutex sync.RWMutex

		stopBackgroundLoop atomic.Bool
		wanted             map[core.TransactionID]*wantedTxData
	}

	wantedTxData struct {
		whenLastPulled time.Time
		queriedPeers   []int // TODO, placeholder
	}
)

func (w *Workflow) initPullConsumer() {
	c := &PullConsumer{
		Consumer: NewConsumer[*PullData](PullConsumerName, w),
		wanted:   make(map[core.TransactionID]*wantedTxData),
	}
	c.AddOnConsume(c.consume)
	c.AddOnClosed(func() {
		c.stop()
		w.terminateWG.Done()
	})
	w.pullConsumer = c

}

func (c *PullConsumer) already(txid *core.TransactionID) bool {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	_, already := c.wanted[*txid]
	return already
}

func (c *PullConsumer) consume(inp *PullData) {
	if c.already(&inp.TxID) {
		return
	}
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.wanted[inp.TxID] = &wantedTxData{}
	c.Log().Infof("<-- %s", inp.TxID.Short())
}

func (c *PullConsumer) stop() {
	c.stopBackgroundLoop.Store(true)
}

const pullPeriod = 10 * time.Millisecond

func (c *PullConsumer) selectTransactionsToPull() map[core.TransactionID]*wantedTxData {
	ret := make(map[core.TransactionID]*wantedTxData)
	nowis := time.Now()

	c.mutex.RLock()
	defer c.mutex.RUnlock()

	for txid, d := range c.wanted {
		if d.whenLastPulled.Add(pullPeriod).Before(nowis) {
			ret[txid] = d
		}
	}
	return ret
}

func (c *PullConsumer) backgroundLoop() {
	for !c.stopBackgroundLoop.Load() {
		time.Sleep(100 * time.Millisecond) // TODO temporary

		toPull := c.selectTransactionsToPull()
		if len(toPull) == 0 {
			continue
		}

	}
}

func (c *PullConsumer) removeFromPullList(txids ...*core.TransactionID) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	for _, txid := range txids {
		delete(c.wanted, *txid)
	}
}
