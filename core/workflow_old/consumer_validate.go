package workflow_old

import (
	"github.com/lunfardo314/proxima/utangle_old"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/workerpool"
)

const ValidateConsumerName = "validate"

type (
	ValidateConsumerInputData struct {
		*PrimaryTransactionData
		draftVertex *utangle_old.Vertex
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
	inp.eventCallback(ValidateConsumerName+".in.new", inp.tx)

	util.Assertf(inp.draftVertex.IsSolid(), "inp.vertex.IsSolid()")
	// will start a worker goroutine or block util worker is available
	c.workerPool.Work(func() {
		if err := inp.draftVertex.Validate(); err != nil {
			inp.eventCallback("finish."+ValidateConsumerName, err)
			c.IncCounter("err")
			txid := inp.tx.ID()
			c.glb.pullConsumer.stopPulling(txid)
			c.glb.solidifyConsumer.postDropTxID(txid)
			c.glb.PostEventDropTxID(inp.tx.ID(), ValidateConsumerName, "%v", err)
			if inp.tx.IsBranchTransaction() {
				c.glb.utxoTangle.SyncData().UnEvidenceIncomingBranch(*txid)
			}

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