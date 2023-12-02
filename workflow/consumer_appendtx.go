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
	EventNewVertex = eventtype.RegisterNew[*NewVertexEventData]("newTx event")
)

func (w *Workflow) initAppendTxConsumer() {
	ret := &AppendTxConsumer{
		Consumer: NewConsumer[*AppendTxConsumerInputData](AppendTxConsumerName, w),
	}
	ret.AddOnConsume(func(inp *AppendTxConsumerInputData) {
		ret.Debugf(inp.PrimaryTransactionData, "IN")
	})
	ret.AddOnConsume(ret.consume)
	nmAdd := EventNewVertex.String()
	w.MustOnEvent(EventNewVertex, func(inp *NewVertexEventData) {
		ret.glb.IncCounter(ret.Name() + "." + nmAdd)
		ret.Log().Debugf("%s: %s", nmAdd, inp.Tx.IDShort())
	})

	w.appendTxConsumer = ret
}

func (c *AppendTxConsumer) consume(inp *AppendTxConsumerInputData) {
	//c.setTrace(inp.Source == TransactionSourceTypeAPI)

	inp.eventCallback(AppendTxConsumerName+".in", inp.Tx)
	// append to the UTXO tangle
	vid, err := c.glb.utxoTangle.AppendVertex(inp.Vertex, utangle.BypassValidation)
	if err != nil {
		// failed
		inp.eventCallback("finish."+AppendTxConsumerName, err)
		c.Debugf(inp.PrimaryTransactionData, "can't append vertex to the tangle: '%v'", err)
		c.IncCounter("fail")

		c.glb.solidifyConsumer.postRemoveTxIDs(inp.Tx.ID())
		c.glb.pullConsumer.removeFromPullList(inp.Tx.ID())
		c.glb.PostEventDropTxID(inp.Tx.ID(), AppendTxConsumerName, "%v", err)
		return
	}
	inp.eventCallback("finish."+AppendTxConsumerName, nil)

	c.logBranch(inp.PrimaryTransactionData, vid.LedgerCoverage(c.glb.UTXOTangle()))
	c.glb.pullConsumer.removeFromPullList(inp.Tx.ID())

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
	c.glb.solidifyConsumer.postRemoveAttachedTxID(vid.ID())
}

func (c *AppendTxConsumer) logBranch(inp *PrimaryTransactionData, coverage uint64) {
	if !inp.Tx.IsBranchTransaction() {
		return
	}

	seqID := inp.Tx.SequencerTransactionData().SequencerID
	c.Log().Infof("BRANCH %s (%s). Source: %s. Coverage: %s",
		inp.Tx.IDShort(), seqID.StringVeryShort(), inp.SourceType.String(), util.GoThousands(coverage))
}
