package workflow

import (
	"github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util/eventtype"
)

const AppendTxConsumerName = "addtx"

type (
	AppendTxConsumerInputData struct {
		*PrimaryInputConsumerData
		Vertex *utangle.Vertex
	}

	AppendTxConsumer struct {
		*Consumer[*AppendTxConsumerInputData]
	}

	NewVertexEventData struct {
		*PrimaryInputConsumerData
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
		c.Debugf(inp.PrimaryInputConsumerData, "IN")
	})
	c.AddOnConsume(c.consume)
	c.AddOnClosed(func() {
		w.rejectConsumer.Stop()
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
	inp.eventCallback(AppendTxConsumerName+".in", inp.Tx)
	// append to the UTXO tangle
	vid, err := c.glb.utxoTangle.AppendVertex(inp.Vertex, utangle.BypassValidation)
	if err != nil {
		inp.eventCallback("finish."+AppendTxConsumerName, err.Error())
		c.Debugf(inp.PrimaryInputConsumerData, "can't append vertex to the tangle: '%v'", err)
		c.IncCounter("fail")
		c.glb.RejectTransaction(*inp.Tx.ID(), "%v", err)
		// inform solidifier
		c.glb.solidifyConsumer.Push(&SolidifyInputData{
			PrimaryInputConsumerData: inp.PrimaryInputConsumerData,
			Remove:                   true,
		})
		return
	}
	inp.eventCallback("finish."+AppendTxConsumerName, "")

	// rise new vertex event
	c.glb.PostEvent(EventNewVertex, &NewVertexEventData{
		PrimaryInputConsumerData: inp.PrimaryInputConsumerData,
		VID:                      vid,
	})

	c.glb.IncCounter(c.Name() + ".ok")
	c.Log().Debugf("added to the UTXO tangle: %s", vid.IDShort())
	c.TraceMilestones(inp.Tx, inp.Tx.ID(), "milestone has been added to the tangle")

	// notify solidifier upon new transaction added to the tangle
	c.glb.solidifyConsumer.Push(&SolidifyInputData{
		newSolidDependency:       vid,
		PrimaryInputConsumerData: inp.PrimaryInputConsumerData,
	}, true)
}
