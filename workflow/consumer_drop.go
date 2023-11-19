package workflow

import (
	"fmt"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/util/eventtype"
)

// sink for dropped transactions

const RejectConsumerName = "drop"

type (
	DropConsumerInputData struct {
		TxID core.TransactionID
		Msg  string
	}

	DropTxConsumer struct {
		*Consumer[*DropConsumerInputData]
	}
)

var EventDroppedTx = eventtype.RegisterNew[*DropConsumerInputData]("dropTx")

func (w *Workflow) initRejectConsumer() {
	c := &DropTxConsumer{
		Consumer: NewConsumer[*DropConsumerInputData](RejectConsumerName, w),
	}
	c.AddOnConsume(c.consume)
	c.AddOnClosed(func() {
		w.eventsConsumer.Stop()
		w.terminateWG.Done()
	})

	nmDrop := EventDroppedTx.String()
	w.MustOnEvent(EventDroppedTx, func(inp *DropConsumerInputData) {
		c.glb.IncCounter(c.Name() + "." + nmDrop)
		c.Log().Debugf("%s: %s", nmDrop, inp.TxID.StringShort())
	})
	w.dropTxConsumer = c
}

func (c *DropTxConsumer) consume(inp *DropConsumerInputData) {
	c.glb.PostEvent(EventDroppedTx, inp)

	// TODO remove from the solidifier all dependent
}

func (w *Workflow) DropTransaction(txid core.TransactionID, reasonFormat string, args ...any) {
	w.dropTxConsumer.Push(&DropConsumerInputData{
		TxID: txid,
		Msg:  fmt.Sprintf(reasonFormat, args...),
	})
}
