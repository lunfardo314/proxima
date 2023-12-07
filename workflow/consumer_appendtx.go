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
		ret.Log().Debugf("%s: %s", nmAdd, inp.tx.IDShort())
	})

	w.appendTxConsumer = ret
}

func (c *AppendTxConsumer) consume(inp *AppendTxConsumerInputData) {
	//c.setTrace(inp.Source == TransactionSourceAPI)
	//inp.eventCallback(AppendTxConsumerName+".in", inp.Tx)

	// TODO due to unclear reasons, sometimes repeating transactions reach this point and attach panics
	// In order to prevent this (rare) panic we do this check
	if c.glb.utxoTangle.Contains(inp.tx.ID()) {
		c.Log().Warnf("repeating transaction %s", inp.tx.IDShort())
		return
	}

	// append to the UTXO tangle
	var vid *utangle.WrappedTx
	var err error
	if inp.makeVirtualTx {
		// append virtualTx right from transaction (non-sequencer transactions from store comes right from pre-validation)
		vid = c.glb.utxoTangle.AppendVirtualTx(inp.tx)
	} else {
		vid, err = c.glb.utxoTangle.AppendVertex(inp.Vertex, c.glb.StoreTxBytes(inp.tx.Bytes()), utangle.BypassValidation)
	}
	if err != nil {
		// failed
		inp.eventCallback("finish."+AppendTxConsumerName, err)
		c.Debugf(inp.PrimaryTransactionData, "can't append transaction to the tangle: '%v'", err)
		c.IncCounter("fail")

		c.glb.solidifyConsumer.postRemoveTxIDs(inp.tx.ID())
		c.glb.pullConsumer.removeFromPullList(inp.tx.ID())
		c.glb.PostEventDropTxID(inp.tx.ID(), AppendTxConsumerName, "%v", err)
		if inp.tx.IsBranchTransaction() {
			c.glb.utxoTangle.SyncData().UnEvidenceIncomingBranch(inp.tx.ID())
		}
		return
	}
	inp.eventCallback("finish."+AppendTxConsumerName, nil)

	c.logBranch(inp.PrimaryTransactionData, vid.LedgerCoverage(c.glb.UTXOTangle()))
	c.glb.pullConsumer.removeFromPullList(inp.tx.ID())

	c.GossipTransactionIfNeeded(inp.PrimaryTransactionData)

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
	if !inp.tx.IsBranchTransaction() {
		return
	}

	seqID := inp.tx.SequencerTransactionData().SequencerID
	c.Log().Infof("BRANCH %s (%s). Source: %s. Coverage: %s",
		inp.tx.IDShort(), seqID.StringVeryShort(), inp.source.String(), util.GoThousands(coverage))
}
