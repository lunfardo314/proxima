package workflow

import (
	"github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/workerpool"
)

const ValidateConsumerName = "validate"

type (
	ValidateConsumerInputData struct {
		*PrimaryTransactionData
		draftVertex *utangle.Vertex
	}

	ValidateConsumer struct {
		*Consumer[*ValidateConsumerInputData]
		workerPool workerpool.WorkerPool
	}
)

const maxNumberOfWorkers = 5

func (w *Workflow) initValidateConsumer() {
	ret := &ValidateConsumer{
		Consumer:   NewConsumer[*ValidateConsumerInputData](ValidateConsumerName, w),
		workerPool: workerpool.NewWorkerPool(maxNumberOfWorkers),
	}
	ret.AddOnConsume(func(inp *ValidateConsumerInputData) {
		ret.traceTx(inp.PrimaryTransactionData, "IN")
	})
	ret.AddOnConsume(ret.consume)
	ret.AddOnClosed(func() {
		w.appendTxConsumer.Stop()
	})
	w.validateConsumer = ret
}

func (c *ValidateConsumer) consume(inp *ValidateConsumerInputData) {
	inp.eventCallback(ValidateConsumerName+".in.new", inp.Tx)

	util.Assertf(inp.draftVertex.IsSolid(), "inp.vertex.IsSolid()")
	// will start a worker goroutine or block util worker is available
	c.workerPool.Work(func() {
		if err := inp.draftVertex.Validate(); err != nil {
			inp.eventCallback("finish."+ValidateConsumerName, err)
			c.IncCounter("err")
			c.glb.DropTransaction(inp.Tx.ID(), ValidateConsumerName, "%v", err)
			return
		}
		c.IncCounter("ok")
		c.Debugf(inp.PrimaryTransactionData, "OK")
		// send to appender
		c.glb.appendTxConsumer.Push(&AppendTxConsumerInputData{
			PrimaryTransactionData: inp.PrimaryTransactionData,
			Vertex:                 inp.draftVertex,
		})
	})
}
