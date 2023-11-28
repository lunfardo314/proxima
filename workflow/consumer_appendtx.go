package workflow

import (
	"github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/eventtype"
)

const AppendTxConsumerName = "addtx"

type (
	AppendTxConsumerInputData struct {
		*PrimaryTransactionData
		Vertex *utangle.Vertex
	}

	AppendTxConsumer struct {
		*Consumer[*AppendTxConsumerInputData]
	}

	NewVertexEventData struct {
		*PrimaryTransactionData
		VID *utangle.WrappedTx
	}
)

var (
	EventNewVertex = eventtype.RegisterNew[*NewVertexEventData]("newTx")
)

func (w *Workflow) initAppendTxConsumer() {
	c := &AppendTxConsumer{
		Consumer: NewConsumer[*AppendTxConsumerInputData](AppendTxConsumerName, w),
	}
	c.AddOnConsume(func(inp *AppendTxConsumerInputData) {
		c.Debugf(inp.PrimaryTransactionData, "IN")
	})
	c.AddOnConsume(c.consume)
	c.AddOnClosed(func() {
		w.dropTxConsumer.Stop()
		w.terminateWG.Done()
	})
	nmAdd := EventNewVertex.String()
	w.MustOnEvent(EventNewVertex, func(inp *NewVertexEventData) {
		c.glb.IncCounter(c.Name() + "." + nmAdd)
		c.Log().Debugf("%s: %s", nmAdd, inp.Tx.IDShort())
	})

	w.appendTxConsumer = c
}

func (c *AppendTxConsumer) consume(inp *AppendTxConsumerInputData) {
	//c.setTrace(inp.Source == TransactionSourceTypeAPI)

	inp.eventCallback(AppendTxConsumerName+".in", inp.Tx)
	// append to the UTXO tangle
	vid, err := c.glb.utxoTangle.AppendVertex(inp.Vertex, utangle.BypassValidation)
	if err != nil {
		inp.eventCallback("finish."+AppendTxConsumerName, err)
		c.Debugf(inp.PrimaryTransactionData, "can't append vertex to the tangle: '%v'", err)
		c.IncCounter("fail")
		c.glb.DropTransaction(*inp.Tx.ID(), "%v", err)
		// inform solidifier
		c.glb.solidifyConsumer.Push(&SolidifyInputData{
			PrimaryTransactionData: inp.PrimaryTransactionData,
			Remove:                 true,
		})
		return
	}
	inp.eventCallback("finish."+AppendTxConsumerName, nil)

	c.traceTx(inp.PrimaryTransactionData, "booked. Source: '%s'. Coverage: %s",
		inp.SourceType.String(), util.GoThousands(vid.LedgerCoverage(c.glb.UTXOTangle())))

	c.glb.pullConsumer.removeFromPullList(*inp.Tx.ID())

	if !inp.WasGossiped {
		// transaction wasn't gossiped yet, it needs to be sent to other peers
		c.glb.txGossipOutConsumer.Push(TxGossipSendInputData{
			PrimaryTransactionData: inp.PrimaryTransactionData,
		})
	}

	// rise new vertex event
	c.glb.PostEvent(EventNewVertex, &NewVertexEventData{
		PrimaryTransactionData: inp.PrimaryTransactionData,
		VID:                    vid,
	})

	c.glb.IncCounter(c.Name() + ".ok")
	c.trace("added to the UTXO tangle: %s", vid.IDShort())

	// notify solidifier upon new transaction added to the tangle
	c.glb.solidifyConsumer.Push(&SolidifyInputData{
		newSolidDependency:     vid,
		PrimaryTransactionData: inp.PrimaryTransactionData,
	}, true)
}
