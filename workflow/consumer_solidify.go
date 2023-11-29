package workflow

import (
	"fmt"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
	utangle "github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/set"
)

const SolidifyConsumerName = "solidify"

type (
	SolidifyInputData struct {
		// if not nil, its is a message to notify Solidify consumer that new transaction (valid and solid) has arrived to the tangle
		newSolidDependency *utangle.WrappedTx
		// used if newTx is == nil
		*PrimaryTransactionData
		// If true, PrimaryTransactionData bears txid to be removed
		Remove bool
	}

	SolidifyConsumer struct {
		*Consumer[*SolidifyInputData]
		stopBackgroundChan chan struct{}
		// mutex for main data structures
		mutex sync.RWMutex
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
		draftVertex                       *utangle.Vertex
		pulled                            bool // transaction was received as a result of the pull request
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
		Consumer:           NewConsumer[*SolidifyInputData](SolidifyConsumerName, w),
		txPending:          make(map[core.TransactionID]wantedTx),
		stopBackgroundChan: make(chan struct{}),
	}
	c.AddOnConsume(func(inp *SolidifyInputData) {
		if inp.Remove {
			c.traceTx(inp.PrimaryTransactionData, "IN (remove)")
			return
		}
		if inp.newSolidDependency == nil {
			c.traceTx(inp.PrimaryTransactionData, "IN (solidify)")
			return
		}
		c.traceTx(inp.PrimaryTransactionData, "IN (check dependency)")
	})
	c.AddOnConsume(c.consume)
	c.AddOnClosed(func() {
		close(c.stopBackgroundChan)
		w.validateConsumer.Stop()
		w.terminateWG.Done()
	})
	w.solidifyConsumer = c
	go c.backgroundLoop()
}

func (c *SolidifyConsumer) IsWaitedTransaction(txid *core.TransactionID) bool {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	_, ret := c.txPending[*txid]
	return ret
}

func (c *SolidifyConsumer) consume(inp *SolidifyInputData) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.setTrace(inp.PrimaryTransactionData.SourceType == TransactionSourceTypeAPI)

	if inp.Remove {
		// command to remove the transaction and other depending on it from the solidification pool
		inp.eventCallback(SolidifyConsumerName+".in.remove", inp.Tx)
		c.removeNonSolidifiableFutureCone(inp.Tx.ID())
		return
	}
	if inp.newSolidDependency == nil {
		// new transaction for solidification arrived
		inp.eventCallback(SolidifyConsumerName+".in.new", inp.Tx)
		c.glb.IncCounter(c.Name() + ".in.new")
		c.newVertexToSolidify(inp)
	} else {
		// new solid transaction has been appended to the tangle, probably some transactions are waiting for it
		inp.eventCallback(SolidifyConsumerName+".in.check", inp.Tx)
		c.glb.IncCounter(c.Name() + ".in.check")
		c.checkNewDependency(inp)
	}
}

func (c *SolidifyConsumer) newVertexToSolidify(inp *SolidifyInputData) {
	_, already := c.txPending[*inp.Tx.ID()]
	util.Assertf(!already, "transaction is in the solidifier isInPullList: %s", inp.Tx.IDString())

	// fetches available inputs, makes draftVertex
	draftVertex, err := c.glb.utxoTangle.MakeDraftVertex(inp.Tx)
	if err != nil {
		// not solidifiable
		c.IncCounter("err")
		c.removeNonSolidifiableFutureCone(inp.Tx.ID())
		c.glb.DropTransaction(*inp.Tx.ID(), "%v", err)
		return
	}

	if solid := !c.putIntoSolidifierIfNeeded(inp, draftVertex); solid {
		// all inputs solid. Send for validation
		c.passToValidation(inp.PrimaryTransactionData, draftVertex)
	}
}

func (c *SolidifyConsumer) passToValidation(primaryTxData *PrimaryTransactionData, draftVertex *utangle.Vertex) {
	util.Assertf(draftVertex.IsSolid(), "v.IsSolid()")
	c.traceTx(primaryTxData, "solidified in %v", time.Now().Sub(primaryTxData.ReceivedWhen))

	delete(c.txPending, *primaryTxData.Tx.ID())

	c.glb.validateConsumer.Push(&ValidateConsumerInputData{
		PrimaryTransactionData: primaryTxData,
		draftVertex:            draftVertex,
	})
}

// returns if draftVertex was placed into the solidifier for further tracking
func (c *SolidifyConsumer) putIntoSolidifierIfNeeded(inp *SolidifyInputData, draftVertex *utangle.Vertex) bool {
	txid := draftVertex.Tx.ID()
	_, already := c.txPending[*txid]
	util.Assertf(!already, "double add to solidifier")

	unknownInputTxIDs := draftVertex.MissingInputTxIDSet()
	if len(unknownInputTxIDs) == 0 {
		c.IncCounter("new.solid")
		return false
	}

	// some inputs unknown
	inp.eventCallback("notsolid."+SolidifyConsumerName, inp.Tx)
	util.Assertf(!draftVertex.IsSolid(), "inconsistency 1")
	c.IncCounter("new.notsolid")
	for unknownTxID := range unknownInputTxIDs {
		c.traceTx(inp.PrimaryTransactionData, "unknown input tx %s", unknownTxID.StringShort())
	}

	vd := &draftVertexData{
		PrimaryTransactionData: inp.PrimaryTransactionData,
		draftVertex:            draftVertex,
		pulled:                 inp.WasPulled,
	}
	c.txPending[*draftVertex.Tx.ID()] = wantedTx{
		since:           time.Now(),
		draftVertexData: vd,
	}

	// for each unknown input, add the new draftVertex to the list of txids
	// dependent on it (past cone tips, known consumers)
	unknownInputTxIDs.ForEach(func(wantedTxID core.TransactionID) bool {
		c.putIntoWaitingList(&wantedTxID, txid)
		return true
	})

	// add to the list of vertices waiting for solidification

	// optionally initialize pull request to other peers if needed
	c.pullIfNeeded(vd)
	return true
}

func (c *SolidifyConsumer) putIntoWaitingList(pendingID, whoIsWaiting *core.TransactionID) {
	var waitingLst []*core.TransactionID
	pendingData, exists := c.txPending[*pendingID]
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
	c.txPending[*pendingID] = pendingData
}

// collectWaitingFutureCone collects all known (to solidifier) txids from the future cone which directly or indirectly depend on the pending tx
func (c *SolidifyConsumer) collectWaitingFutureCone(txid *core.TransactionID, ret set.Set[core.TransactionID]) {
	if ret.Contains(*txid) {
		return
	}
	ret.Insert(*txid)
	pendingData := c.txPending[*txid]
	for _, txid1 := range pendingData.waitingList {
		c.collectWaitingFutureCone(txid1, ret)
	}
}

// removeNonSolidifiableFutureCone removes from solidifier all txids which directly or indirectly depend on txid
func (c *SolidifyConsumer) removeNonSolidifiableFutureCone(txid *core.TransactionID) {
	c.Log().Debugf("remove non-solidifiable future cone of %s", txid.StringShort())

	ns := set.New[core.TransactionID]()
	c.collectWaitingFutureCone(txid, ns)

	ns.ForEach(func(txid core.TransactionID) bool {
		delete(c.txPending, txid)
		return true
	})
}

// checkNewDependency checks all pending transactions waiting for the new solid transaction
func (c *SolidifyConsumer) checkNewDependency(inp *SolidifyInputData) {
	txid := inp.Tx.ID()
	pendingData, isKnownDependency := c.txPending[*txid]
	if !isKnownDependency {
		// nobody is waiting, nothing to remove. Ignore
		return
	}
	// it is not needed in the dependencies list anymore
	delete(c.txPending, *txid)

	c.traceTx(inp.PrimaryTransactionData, "waitingList: %s", __txLstString(pendingData.waitingList))

	// looping over pending vertices which are waiting for the dependency newTxID
	for _, txidWaiting := range pendingData.waitingList {
		waitingPendingData, found := c.txPending[*txidWaiting]
		if !found {
			c.Log().Debugf("%s was waiting for %s, not waitingPendingData anymore", txidWaiting.StringShort(), inp.Tx.IDShort())
			// not waitingPendingData anymore
			return
		}
		util.Assertf(waitingPendingData.draftVertexData != nil, "waitingPendingData.draftVertexData != nil")

		if conflict := waitingPendingData.draftVertexData.draftVertex.FetchMissingDependencies(c.glb.utxoTangle); conflict != nil {
			// tx cannot be solidified, remove
			c.removeNonSolidifiableFutureCone(txidWaiting)
			err := fmt.Errorf("conflict at %s", conflict.Short())
			inp.eventCallback("finish.fail."+SolidifyConsumerName, err)
			c.glb.DropTransaction(*txidWaiting, "%v", err)
			continue
		}
		if waitingPendingData.draftVertexData.draftVertex.IsSolid() {
			// all inputs are solid, send it to the validation
			c.passToValidation(waitingPendingData.draftVertexData.PrimaryTransactionData, waitingPendingData.draftVertexData.draftVertex)
			continue
		}
		//c.traceTx(waitingPendingData.PrimaryTransactionData, "not solid yet. Missing: %s\nTransaction: %s",
		//	waitingPendingData.draftVertex.MissingInputTxIDString(), waitingPendingData.draftVertex.Lines().String())

		// ask for missing inputs from peering
		c.pullIfNeeded(waitingPendingData.draftVertexData)
	}
}

func __txLstString(lst []*core.TransactionID) string {
	ret := lines.New()
	for i := range lst {
		ret.Add(lst[i].StringShort())
	}
	return ret.Join(",")
}

const (
	pullImmediately                       = time.Duration(0)
	pullDelayFirstPeriodSequencer         = 1 * time.Second
	pullDelayFirstPeriodOtherTransactions = 1 * time.Second
)

func (c *SolidifyConsumer) pullIfNeeded(vd *draftVertexData) {
	if vd.draftVertex.IsSolid() {
		return
	}
	if vd.allInputsAlreadyPulled {
		return
	}
	if vd.Tx.IsBranchTransaction() && !vd.draftVertex.IsStemInputSolid() {
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
		if isSolid, seqInputIdx := vd.draftVertex.IsSequencerInputSolid(); !isSolid {
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
	vd.draftVertex.MissingInputTxIDSet().ForEach(func(txid core.TransactionID) bool {
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

const solidifyBackgroundLoopPeriod = 100 * time.Millisecond

func (c *SolidifyConsumer) backgroundLoop() {
	defer c.Log().Debugf("background loop stopped")

	for {
		select {
		case <-c.stopBackgroundChan:
			return
		case <-time.After(solidifyBackgroundLoopPeriod):
		}
		c.doBackgroundCheck()
	}
}

func (c *SolidifyConsumer) doBackgroundCheck() {
	toRemove := c.collectToRemove()
	if len(toRemove) > 0 {
		c.removeDueToDeadline(toRemove)

		for i := range toRemove {
			c.glb.DropTransaction(toRemove[i], "solidification timeout %v. Missing: %s",
				keepNotSolid, c.missingInputsString(toRemove[i]))
		}
	}
}

func (c *SolidifyConsumer) collectToRemove() []core.TransactionID {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	nowis := time.Now()
	ret := make([]core.TransactionID, 0)
	for txid, vd := range c.txPending {
		if nowis.After(vd.since.Add(keepNotSolid)) {
			ret = append(ret, txid)
		}
	}
	return ret
}

func (c *SolidifyConsumer) removeDueToDeadline(toRemove []core.TransactionID) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	for i := range toRemove {
		delete(c.txPending, toRemove[i])
		c.removeNonSolidifiableFutureCone(&toRemove[i])
	}
}

func (c *SolidifyConsumer) missingInputsString(txid core.TransactionID) string {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	if pendingData, found := c.txPending[txid]; found && pendingData.draftVertexData != nil {
		return pendingData.draftVertexData.draftVertex.MissingInputTxIDString()
	}
	return "(unknown tx)"
}

func (c *SolidifyConsumer) DumpPending() *lines.Lines {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	ret := lines.New()
	ret.Add("======= transactions pending in solidifier")
	for txid, pd := range c.txPending {
		ret.Add("pending %s", txid.StringShort())
		if pd.draftVertexData != nil {
			ret.Append(pd.draftVertexData.draftVertex.PendingDependenciesLines("  "))
		} else {
			ret.Add("unknown tx")
		}
	}
	return ret
}
