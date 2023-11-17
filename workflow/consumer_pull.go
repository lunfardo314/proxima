package workflow

import (
	"github.com/lunfardo314/proxima/core"
)

const PullConsumerName = "pull"

type (
	PullData struct {
		TxID core.TransactionID
	}

	PullConsumer struct {
		*Consumer[*PullData]
	}
)

func (w *Workflow) initPullConsumer() {
	c := &PullConsumer{
		Consumer: NewConsumer[*PullData](PullConsumerName, w),
	}
	c.AddOnConsume(c.consume)
	c.AddOnClosed(func() {
		// TODO close network resources
		w.terminateWG.Done()
	})
	w.pullConsumer = c
}

func (c *PullConsumer) consume(inp *PullData) {
	c.Log().Infof("<-- %s", inp.TxID.Short())
}
