package workflow

import (
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/core"
	utangle "github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

const SolidifyConsumerName = "solidify"

const (
	SolidifyCommandNewTx = byte(iota)
	SolidifyCommandCheckTxID
	SolidifyCommandRemoveAttachedTxID
	SolidifyCommandRemoveTxID
	SolidifyCommandRemoveTooOld
)

type (
	SolidifyInputData struct {
		Cmd byte
		// != nil for SolidifyCommandCheckTxID and SolidifyCommandRemoveTxID, == nil for SolidifyCommandNewTx
		TxID *core.TransactionID
		// == nil for SolidifyCommandCheckTxID and SolidifyCommandRemoveTxID, != nil for SolidifyCommandNewTx
		*PrimaryTransactionData
	}

	SolidifyConsumer struct {
		*Consumer[*SolidifyInputData]
		stopSolidificationDeadlineLoopChan chan struct{}
		// txPending is list of draft vertices waiting for solidification to be sent for validation
		txPending map[core.TransactionID]wantedTx
	}

	wantedTx struct {
		since time.Time // since when wantedTx
		// if != nil, it is transaction being solidified
		// if == nil, transaction is unknown yet
		draftVertexData *draftVertexData
		// tx IDs who are waiting for the tx to be solid
		waitingList []*core.TransactionID
	}

	draftVertexData struct {
		*PrimaryTransactionData
		vertex                            *utangle.Vertex
		addedToWaitingLists               bool
		stemInputAlreadyPulled            bool
		sequencerPredecessorAlreadyPulled bool
		allInputsAlreadyPulled            bool
		sentForValidation                 bool
	}
)

const (
	solidificationTimeout = 3 * time.Minute // only for testing. Must be longer in reality
)

func (w *Workflow) initSolidifyConsumer() {
	ret := &SolidifyConsumer{
		Consumer:                           NewConsumer[*SolidifyInputData](SolidifyConsumerName, w),
		txPending:                          make(map[core.TransactionID]wantedTx),
		stopSolidificationDeadlineLoopChan: make(chan struct{}),
	}
	ret.AddOnConsume(ret.consume)
	ret.AddOnClosed(func() {
		close(ret.stopSolidificationDeadlineLoopChan)
		w.validateConsumer.Stop()
	})
	w.solidifyConsumer = ret
	go ret.solidificationDeadlineLoop()
}

func (c *SolidifyConsumer) consume(inp *SolidifyInputData) {
	switch inp.Cmd {
	case SolidifyCommandNewTx:
		util.Assertf(inp.TxID == nil && inp.PrimaryTransactionData != nil, "inp.TxID == nil && inp.primaryInput != nil")
		c.traceTx(inp.PrimaryTransactionData, "cmd newTx")
		//inp.eventCallback(SolidifyConsumerName+".in.new", inp.Tx)
		c.glb.IncCounter(c.Name() + ".in.new")
		c.newTx(inp)

	case SolidifyCommandCheckTxID:
		util.Assertf(inp.TxID != nil && inp.PrimaryTransactionData == nil, "inp.TxID != nil && inp.primaryInput == nil")
		c.traceTxID(inp.TxID, "cmd checkTxID")
		//inp.eventCallback(SolidifyConsumerName+".in.check", inp.Tx)
		c.glb.IncCounter(c.Name() + ".in.check")
		c.checkTxID(inp.TxID)

	case SolidifyCommandRemoveAttachedTxID:
		util.Assertf(inp.TxID != nil && inp.PrimaryTransactionData == nil, "inp.TxID != nil && inp.primaryInput == nil")
		c.traceTxID(inp.TxID, "cmd removeAttachedTxID")
		//inp.eventCallback(SolidifyConsumerName+".in.removeAttached", inp.Tx)
		c.glb.IncCounter(c.Name() + ".in.removeAttached")
		c.removeAttachedTxID(inp.TxID)

	case SolidifyCommandRemoveTxID:
		util.Assertf(inp.TxID != nil && inp.PrimaryTransactionData == nil, "inp.TxID != nil && inp.primaryInput == nil")
		c.traceTxID(inp.TxID, "cmd removeTxID")
		//inp.eventCallback(SolidifyConsumerName+".in.remove", inp.TxID)
		c.glb.IncCounter(c.Name() + ".in.remove")
		c.removeTxID(inp.TxID)

	case SolidifyCommandRemoveTooOld:
		util.Assertf(inp.TxID == nil && inp.PrimaryTransactionData == nil, "inp.TxID != nil && inp.primaryInput == nil")
		c.removeTooOld()

	default:
		panic("wrong solidifier command")
	}
}

func (c *SolidifyConsumer) newTx(inp *SolidifyInputData) {
	txid := inp.tx.ID()
	pendingData, exists := c.txPending[*txid]
	util.Assertf(pendingData.draftVertexData == nil, "repeating new tx in solidifier: %s", txid.StringShort())
	if !exists {
		pendingData.since = time.Now()
	}

	pendingData.draftVertexData = &draftVertexData{
		PrimaryTransactionData: inp.PrimaryTransactionData,
		vertex:                 utangle.NewVertex(inp.tx),
	}
	c.txPending[*txid] = pendingData
	c.checkTxID(txid)
}

// checkTxID runs solidification on the transaction. If it is solid, passes it to validator
func (c *SolidifyConsumer) checkTxID(txid *core.TransactionID) {
	pendingData, isKnown := c.txPending[*txid]
	if !isKnown {
		c.traceTxID(txid, "checkTxID: unknown tx")
		// nobody is waiting, nothing to remove. Ignore
		return
	}
	if pendingData.draftVertexData == nil {
		// should not happen ??
		return
	}
	if pendingData.draftVertexData.sentForValidation {
		return
	}
	// run solidification
	c.traceTx(pendingData.draftVertexData.PrimaryTransactionData, "checkTxID: run solidification")

	conflict := pendingData.draftVertexData.vertex.FetchMissingDependencies(c.glb.utxoTangle)
	if conflict != nil {
		// conflict in the past cone. Cannot be solidified. Remove from solidifier
		c.traceTx(pendingData.draftVertexData.PrimaryTransactionData, "checkTxID: conflict", conflict.StringShort())

		c.removeTxID(txid)
		c.glb.PostEventDropTxID(txid, c.Name(), "conflict %s", conflict.StringShort())
		if pendingData.draftVertexData.vertex.Tx.IsBranchTransaction() {
			c.glb.utxoTangle.SyncData().UnEvidenceIncomingBranch(txid)
		}
		return
	}
	// check if solidified already
	if pendingData.draftVertexData.vertex.IsSolid() {
		// solid already, pass to validator
		c.traceTx(pendingData.draftVertexData.PrimaryTransactionData, "checkTxID: send for validation")
		pendingData.draftVertexData.sentForValidation = true
		c.sendForValidation(pendingData.draftVertexData.PrimaryTransactionData, pendingData.draftVertexData.vertex)
		return
	}

	{ // not solid yet
		numMissingInputs, numMissingEndorsements := pendingData.draftVertexData.vertex.NumMissingInputs()
		c.traceTx(pendingData.draftVertexData.PrimaryTransactionData, "checkTxID: not solid: missing inputs: %d, missing endorsements: %d",
			numMissingInputs, numMissingEndorsements)
	}

	if !pendingData.draftVertexData.addedToWaitingLists {
		// first time add missing inputs into the waiting lists
		missing := pendingData.draftVertexData.vertex.MissingInputTxIDSet()
		util.Assertf(len(missing) > 0, "len(missing) > 0")

		missing.ForEach(func(wanted core.TransactionID) bool {
			c.putIntoWaitingList(&wanted, txid)
			return true
		})
		pendingData.draftVertexData.addedToWaitingLists = true
	}
	c.pullIfNeeded(pendingData.draftVertexData)
}

func (c *SolidifyConsumer) removeAttachedTxID(txid *core.TransactionID) {
	pendingData, isKnown := c.txPending[*txid]

	if !isKnown {
		// could happen when transactions are dropped because of solidification deadline during high intensity sync
		c.Log().Warnf("RARE: transaction %s is not among pending", txid.StringShort())
		return
	}
	if pendingData.draftVertexData != nil {
		util.Assertf(pendingData.draftVertexData.vertex.IsSolid(), "pendingData.draftVertexData.vertex.IsSolid()")
	}

	delete(c.txPending, *txid)
	c.traceTxID(txid, "removeAttachedTxID: deleted")

	c.postCheckTxIDs(pendingData.waitingList...)
}

// removeTxID removes from solidifier all txids which directly or indirectly depend on txid
func (c *SolidifyConsumer) removeTxID(txid *core.TransactionID) {
	c.Log().Debugf("remove %s", txid.StringShort())
	pendingData, isKnown := c.txPending[*txid]
	if !isKnown {
		c.Log().Debugf("removeTxID: unknown %s", txid.StringShort())
		return
	}
	c.traceTxID(txid, "removeTxID: deleted")
	delete(c.txPending, *txid)
	c.postRemoveTxIDs(pendingData.waitingList...)
}

func (c *SolidifyConsumer) removeTooOld() {
	nowis := time.Now()
	toRemove := make([]*core.TransactionID, 0)
	for txid, pendingData := range c.txPending {
		if nowis.After(pendingData.since.Add(solidificationTimeout)) {
			txid1 := txid
			toRemove = append(toRemove, &txid1)
			c.glb.PostEventDropTxID(&txid1, c.Name(), "deadline. Missing: %s", c.missingInputsString(txid1))
			if pendingData.draftVertexData != nil && pendingData.draftVertexData.vertex.Tx.IsBranchTransaction() {
				c.glb.utxoTangle.SyncData().UnEvidenceIncomingBranch(&txid1)
			}
		}
	}
	c.postRemoveTxIDs(toRemove...)
}

func __txLstString(lst []*core.TransactionID) string {
	ret := lines.New()
	for i := range lst {
		ret.Add(lst[i].StringShort())
	}
	return ret.Join(",")
}

func (c *SolidifyConsumer) sendForValidation(primaryTxData *PrimaryTransactionData, draftVertex *utangle.Vertex) {
	util.Assertf(draftVertex.IsSolid(), "v.IsSolid()")
	c.glb.validateConsumer.Push(&ValidateConsumerInputData{
		PrimaryTransactionData: primaryTxData,
		draftVertex:            draftVertex,
	})
}

func (c *SolidifyConsumer) putIntoWaitingList(wantedID, whoIsWaiting *core.TransactionID) {
	var waitingLst []*core.TransactionID
	pendingData, exists := c.txPending[*wantedID]
	if exists {
		waitingLst = pendingData.waitingList
	} else {
		// new wanted transaction, not seen yet
		pendingData.since = time.Now()
	}
	if len(waitingLst) == 0 {
		waitingLst = make([]*core.TransactionID, 0, 1)
	}
	pendingData.waitingList = append(waitingLst, whoIsWaiting)
	c.txPending[*wantedID] = pendingData
}

func (c *SolidifyConsumer) postCheckTxIDs(txids ...*core.TransactionID) {
	for _, txid := range txids {
		c.Push(&SolidifyInputData{
			Cmd:  SolidifyCommandCheckTxID,
			TxID: txid,
		})
	}
}

func (c *SolidifyConsumer) postRemoveTxIDs(txids ...*core.TransactionID) {
	for _, txid := range txids {
		c.Push(&SolidifyInputData{
			Cmd:  SolidifyCommandRemoveTxID,
			TxID: txid,
		})
	}
}

func (c *SolidifyConsumer) postRemoveAttachedTxID(txid *core.TransactionID) {
	c.Push(&SolidifyInputData{
		Cmd:  SolidifyCommandRemoveAttachedTxID,
		TxID: txid,
	})
}

const (
	pullImmediately                       = time.Duration(0)
	pullDelayFirstPeriodSequencer         = 1 * time.Second
	pullDelayFirstPeriodOtherTransactions = 1 * time.Second
)

func (c *SolidifyConsumer) pullIfNeeded(vd *draftVertexData) {
	if vd.vertex.IsSolid() {
		return
	}
	if vd.allInputsAlreadyPulled {
		return
	}
	neededFor := vd.tx.ID()
	if vd.tx.IsBranchTransaction() && !vd.vertex.IsStemInputSolid() {
		// first need to solidify stem input. Only when stem input is solid, we pull the rest
		// this makes node synchronization more sequential, from past to present slot by slot
		if !vd.stemInputAlreadyPulled {
			// pull immediately
			vd.stemInputAlreadyPulled = true
			c.pull(pullImmediately, neededFor, vd.tx.SequencerTransactionData().StemOutputData.PredecessorOutputID.TransactionID())
		}
		return
	}
	// here stem input solid, if any
	if vd.tx.IsSequencerMilestone() {
		if isSolid, seqInputIdx := vd.vertex.IsSequencerInputSolid(); !isSolid {
			if !vd.sequencerPredecessorAlreadyPulled {
				seqInputOID := vd.tx.MustInputAt(seqInputIdx)
				vd.sequencerPredecessorAlreadyPulled = true
				c.pull(pullImmediately, neededFor, seqInputOID.TransactionID())
			}
			return
		}
	}

	// now we can pull the rest
	c.pull(pullImmediately, neededFor, util.Keys(vd.vertex.MissingInputTxIDSet())...)
}

func (c *SolidifyConsumer) pull(initialDelay time.Duration, neededFor *core.TransactionID, txids ...core.TransactionID) {
	for i := range txids {
		c.tracePull("send pull %s, delay %v. Needed for %s",
			func() any { return txids[i].StringShort() },
			initialDelay,
			func() any { return neededFor.StringShort() },
		)
	}

	c.glb.pullConsumer.Push(&PullTxData{
		TxIDs:        txids,
		InitialDelay: initialDelay,
	})
}

const solidificationDeadlineLoopPeriod = time.Second

func (c *SolidifyConsumer) solidificationDeadlineLoop() {
	defer c.Log().Debugf("solidification deadline loop stopped")
	syncStatus := c.glb.utxoTangle.SyncData()
	for {
		select {
		case <-c.stopSolidificationDeadlineLoopChan:
			return
		case <-time.After(solidificationDeadlineLoopPeriod):
			// invoke purge of too old if inside sync window. If node is not synced, be patient and do not remove old transactions
			if syncStatus.IsSynced() {
				c.Push(&SolidifyInputData{Cmd: SolidifyCommandRemoveTooOld})
			}
		}
	}
}

func (c *SolidifyConsumer) missingInputsString(txid core.TransactionID) string {
	if pendingData, found := c.txPending[txid]; found {
		if pendingData.draftVertexData != nil {
			return pendingData.draftVertexData.vertex.MissingInputTxIDString()
		} else {
			return fmt.Sprintf("%s is not in solidifier yet", txid.StringShort())
		}
	}
	return fmt.Sprintf("inconsistency: txid %s is not among pendingtx IDs", txid.StringShort())
}

//
//func (c *SolidifyConsumer) DumpPending() *lines.Lines {
//	ret := lines.New()
//	ret.Add("======= transactions pending in solidifier")
//	for txid, pd := range c.txPending {
//		ret.Add("pending %s", txid.StringShort())
//		if pd.draftVertexData != nil {
//			ret.Append(pd.draftVertexData.vertex.PendingDependenciesLines("  "))
//		} else {
//			ret.Add("unknown tx")
//		}
//	}
//	return ret
//}
