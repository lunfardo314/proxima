package workflow

import (
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
		pulled                            bool // transaction was received as a result of the pull request
		addedToWaitingLists               bool
		stemInputAlreadyPulled            bool
		sequencerPredecessorAlreadyPulled bool
		allInputsAlreadyPulled            bool
	}
)

const (
	keepNotSolid = 10 * time.Second // only for testing. Must be longer in reality
)

func (w *Workflow) initSolidifyConsumer() {
	c := &SolidifyConsumer{
		Consumer:                           NewConsumer[*SolidifyInputData](SolidifyConsumerName, w),
		txPending:                          make(map[core.TransactionID]wantedTx),
		stopSolidificationDeadlineLoopChan: make(chan struct{}),
	}
	c.AddOnConsume(c.consume)
	c.AddOnClosed(func() {
		close(c.stopSolidificationDeadlineLoopChan)
		w.validateConsumer.Stop()
		w.terminateWG.Done()
	})
	w.solidifyConsumer = c
	go c.solidificationDeadlineLoop()
}

func (c *SolidifyConsumer) consume(inp *SolidifyInputData) {
	//c.setTrace(inp.PrimaryTransactionData.SourceType == TransactionSourceTypeAPI) /// ?????????? TODO

	switch inp.Cmd {
	case SolidifyCommandNewTx:
		util.Assertf(inp.TxID == nil && inp.PrimaryTransactionData != nil, "inp.TxID == nil && inp.primaryInput != nil")
		c.Log().Debugf("cmd newTx %s", inp.TxID.StringShort())
		//inp.eventCallback(SolidifyConsumerName+".in.new", inp.Tx)
		c.glb.IncCounter(c.Name() + ".in.new")
		c.newTx(inp)

	case SolidifyCommandCheckTxID:
		util.Assertf(inp.TxID != nil && inp.PrimaryTransactionData == nil, "inp.TxID != nil && inp.primaryInput == nil")
		c.Log().Debugf("cmd checkTxID %s", inp.TxID.StringShort())
		//inp.eventCallback(SolidifyConsumerName+".in.check", inp.Tx)
		c.glb.IncCounter(c.Name() + ".in.check")
		c.checkTxID(inp.TxID)

	case SolidifyCommandRemoveAttachedTxID:
		util.Assertf(inp.TxID != nil && inp.PrimaryTransactionData == nil, "inp.TxID != nil && inp.primaryInput == nil")
		c.Log().Debugf("cmd removeAttachedTxID %s", inp.TxID.StringShort())
		//inp.eventCallback(SolidifyConsumerName+".in.removeAttached", inp.Tx)
		c.glb.IncCounter(c.Name() + ".in.removeAttached")
		c.removeAttachedTxID(inp.TxID)

	case SolidifyCommandRemoveTxID:
		util.Assertf(inp.TxID != nil && inp.PrimaryTransactionData == nil, "inp.TxID != nil && inp.primaryInput == nil")
		c.Log().Debugf("cmd removeTxID %s", inp.TxID.StringShort())
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
	txid := inp.Tx.ID()
	pendingData, exists := c.txPending[*txid]
	util.Assertf(pendingData.draftVertexData == nil, "repeating new tx in solidifier: %s", txid.StringShort())
	if !exists {
		pendingData.since = time.Now()
	}
	pendingData.draftVertexData = &draftVertexData{
		PrimaryTransactionData: inp.PrimaryTransactionData,
		vertex:                 utangle.NewVertex(inp.Tx),
		pulled:                 inp.WasPulled,
	}
	c.txPending[*txid] = pendingData
	c.checkTxID(txid)
}

// checkTxID runs solidification on the transaction. If it is solid, passes it to validator
func (c *SolidifyConsumer) checkTxID(txid *core.TransactionID) {
	pendingData, isKnown := c.txPending[*txid]
	if !isKnown {
		c.Log().Debugf("checkTxID: unknown %s", txid.StringShort())
		// nobody is waiting, nothing to remove. Ignore
		return
	}
	if pendingData.draftVertexData == nil {
		// should not happen
		return
	}
	// run solidification
	conflict := pendingData.draftVertexData.vertex.FetchMissingDependencies(c.glb.utxoTangle)
	if conflict != nil {
		// conflict in the past cone. Cannot be solidified. Remove from solidifier
		c.removeTxID(txid)
		return
	}
	// check if solidified already
	if pendingData.draftVertexData.vertex.IsSolid() {
		// solid already, pass to validator
		c.passToValidation(pendingData.draftVertexData.PrimaryTransactionData, pendingData.draftVertexData.vertex)
		return
	}
	// not solid yet
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
	c.Log().Debugf("removeAttachedTxID %s", txid.StringShort())
	pendingData, isKnown := c.txPending[*txid]
	util.Assertf(isKnown, "removeAttachedTxID: unknown", txid.StringShort())
	util.Assertf(pendingData.draftVertexData != nil, "pendingData.draftVertexData != nil")
	util.Assertf(pendingData.draftVertexData.vertex.IsSolid(), "pendingData.draftVertexData.vertex.IsSolid()")

	delete(c.txPending, *txid)

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
	delete(c.txPending, *txid)
	c.postRemoveTxIDs(pendingData.waitingList...)
}

func (c *SolidifyConsumer) removeTooOld() {
	nowis := time.Now()
	toRemove := make([]*core.TransactionID, 0)
	for txid, pendingData := range c.txPending {
		if nowis.After(pendingData.since.Add(keepNotSolid)) {
			txid1 := txid
			toRemove = append(toRemove, &txid1)
		}
	}
	for _, txid := range toRemove {
		c.removeTxID(txid)
	}
}

func (c *SolidifyConsumer) passToValidation(primaryTxData *PrimaryTransactionData, draftVertex *utangle.Vertex) {
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

func __txLstString(lst []*core.TransactionID) string {
	ret := lines.New()
	for i := range lst {
		ret.Add(lst[i].StringShort())
	}
	return ret.Join(",")
}

func (c *SolidifyConsumer) postCheckTxIDs(txids ...*core.TransactionID) {
	for _, txid := range txids {
		c.Push(&SolidifyInputData{
			Cmd:  SolidifyCommandCheckTxID,
			TxID: txid,
		}, true)
	}
}

func (c *SolidifyConsumer) postRemoveTxIDs(txids ...*core.TransactionID) {
	for _, txid := range txids {
		c.Push(&SolidifyInputData{
			Cmd:  SolidifyCommandRemoveTxID,
			TxID: txid,
		}, true)
	}
}

func (c *SolidifyConsumer) postRemoveAttachedTxID(txid *core.TransactionID) {
	c.Push(&SolidifyInputData{
		Cmd:  SolidifyCommandRemoveAttachedTxID,
		TxID: txid,
	}, true)
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
	if vd.Tx.IsBranchTransaction() && !vd.vertex.IsStemInputSolid() {
		// first need to solidify stem input. Only when stem input is solid, we pull the rest
		// this makes node synchronization more sequential, from past to present slot by slot
		if !vd.stemInputAlreadyPulled {
			// pull immediately
			c.pull(vd.Tx.SequencerTransactionData().StemOutputData.PredecessorOutputID.TransactionID(), pullImmediately)
			vd.stemInputAlreadyPulled = true
		}
		return
	}

	if vd.Tx.IsSequencerMilestone() {
		//stem is isInPullList solid, we can pull sequencer input
		if isSolid, seqInputIdx := vd.vertex.IsSequencerInputSolid(); !isSolid {
			seqInputOID := vd.Tx.MustInputAt(seqInputIdx)
			var delayFirst time.Duration
			if vd.WasPulled {
				delayFirst = pullImmediately
			} else {
				delayFirst = pullDelayFirstPeriodSequencer
			}
			c.pull(seqInputOID.TransactionID(), delayFirst)
			vd.sequencerPredecessorAlreadyPulled = true
			return
		}
	}

	// now we can pull the rest
	vd.vertex.MissingInputTxIDSet().ForEach(func(txid core.TransactionID) bool {
		var delayFirst time.Duration
		if vd.WasPulled {
			delayFirst = pullImmediately
		} else {
			delayFirst = pullDelayFirstPeriodOtherTransactions
		}
		c.pull(txid, delayFirst)
		return true
	})
}

func (c *SolidifyConsumer) pull(txid core.TransactionID, initialDelay time.Duration) {
	c.glb.pullConsumer.Push(&PullTxData{
		TxID:         txid,
		InitialDelay: initialDelay,
	})
}

const solidificationDeadlineLoopPeriod = time.Second

func (c *SolidifyConsumer) solidificationDeadlineLoop() {
	defer c.Log().Debugf("solidification deadline loop stopped")
	for {
		select {
		case <-c.stopSolidificationDeadlineLoopChan:
			return
		case <-time.After(solidificationDeadlineLoopPeriod):
			c.Push(&SolidifyInputData{Cmd: SolidifyCommandRemoveTooOld}, true)
		}
	}
}

func (c *SolidifyConsumer) missingInputsString(txid core.TransactionID) string {
	if pendingData, found := c.txPending[txid]; found && pendingData.draftVertexData != nil {
		return pendingData.draftVertexData.vertex.MissingInputTxIDString()
	}
	return "(unknown tx)"
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
