package workflow

import (
	"github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/workerpool"
)

const ValidateConsumerName = "validate"

type (
	ValidateConsumerInputData struct {
		*PrimaryInputConsumerData
		draftVertex *utangle.Vertex
	}

	ValidateConsumer struct {
		*Consumer[*ValidateConsumerInputData]
		workerPool workerpool.WorkerPool
	}
)

const maxNumberOfWorkers = 5

func (w *Workflow) initValidateConsumer() {
	c := &ValidateConsumer{
		Consumer:   NewConsumer[*ValidateConsumerInputData](ValidateConsumerName, w),
		workerPool: workerpool.NewWorkerPool(maxNumberOfWorkers),
	}
	c.AddOnConsume(func(inp *ValidateConsumerInputData) {
		c.Debugf(inp.PrimaryInputConsumerData, "IN")
		c.TraceMilestones(inp.draftVertex.Tx, inp.Tx.ID(), "milestone arrived")
	})
	c.AddOnConsume(c.consume)
	c.AddOnClosed(func() {
		w.appendTxConsumer.Stop()
		w.terminateWG.Done()
	})
	w.validateConsumer = c
}

func (c *ValidateConsumer) consume(inp *ValidateConsumerInputData) {
	inp.eventCallback(ValidateConsumerName+".in.new", inp.Tx)

	util.Assertf(inp.draftVertex.IsSolid(), "inp.draftVertex.IsSolid()")
	// will start a worker goroutine or block util worker is available
	c.workerPool.Work(func() {
		// will check for conflicts
		vid, err := utangle.MakeVertex(inp.draftVertex)
		if err != nil {
			inp.eventCallback(ValidateConsumerName+".fail", inp.Tx)
			c.IncCounter("err")
			c.glb.RejectTransaction(*inp.Tx.ID(), "%v", err)
			// inform solidifier
			c.glb.solidifyConsumer.Push(&SolidifyInputData{
				PrimaryInputConsumerData: inp.PrimaryInputConsumerData,
				Remove:                   true,
			})
			return
		}
		c.IncCounter("ok")
		c.Debugf(inp.PrimaryInputConsumerData, "OK")
		// send to appender
		c.glb.appendTxConsumer.Push(&AppendTxConsumerInputData{
			PrimaryInputConsumerData: inp.PrimaryInputConsumerData,
			VID:                      vid,
		})
	})
}
