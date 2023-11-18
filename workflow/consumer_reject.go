package workflow

import (
	"fmt"
	"sync"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/util/eventtype"
)

const RejectConsumerName = "reject"

type (
	RejectInputData struct {
		TxID core.TransactionID
		Msg  string
	}

	RejectConsumer struct {
		*Consumer[*RejectInputData]
		mutex    sync.RWMutex
		rejected map[core.TransactionID]struct{}
	}
)

var EventRejectedTx = eventtype.RegisterNew[*RejectInputData]("rejectTx")

func (w *Workflow) initRejectConsumer() {
	c := &RejectConsumer{
		Consumer: NewConsumer[*RejectInputData](RejectConsumerName, w),
		rejected: make(map[core.TransactionID]struct{}),
	}
	c.AddOnConsume(c.consume)
	c.AddOnClosed(func() {
		w.eventsConsumer.Stop()
		w.terminateWG.Done()
	})

	nmReject := EventRejectedTx.String()
	w.MustOnEvent(EventRejectedTx, func(inp *RejectInputData) {
		c.glb.IncCounter(c.Name() + "." + nmReject)
		c.Log().Debugf("%s: %s", nmReject, inp.TxID.StringShort())
	})
	w.rejectConsumer = c
}

func (c *RejectConsumer) consume(inp *RejectInputData) {
	c.glb.PostEvent(EventRejectedTx, inp)

	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.rejected[inp.TxID] = struct{}{}

	// TODO remove from the solidifier
}

func (c *RejectConsumer) IsRejected(txid *core.TransactionID) bool {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	_, yes := c.rejected[*txid]
	return yes
}

func (w *Workflow) IsRejected(txid *core.TransactionID) bool {
	return w.rejectConsumer.IsRejected(txid)
}

func (w *Workflow) RejectTransaction(txid core.TransactionID, reasonFormat string, args ...any) {
	w.rejectConsumer.Push(&RejectInputData{
		TxID: txid,
		Msg:  fmt.Sprintf(reasonFormat, args...),
	})
}
