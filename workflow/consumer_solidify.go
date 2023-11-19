package workflow

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
	utangle "github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"go.uber.org/atomic"
)

const SolidifyConsumerName = "solidify"

type (
	SolidifyInputData struct {
		// if not nil, its is a message to notify Solidify consumer that new transaction (valid and solid) has arrived to the tangle
		newSolidDependency *utangle.WrappedTx
		// used if newTx is == nil
		*PrimaryInputConsumerData
		// If true, PrimaryInputConsumerData bears txid to be removed
		Remove bool
	}

	SolidifyConsumer struct {
		*Consumer[*SolidifyInputData]
		stopBackgroundLoop atomic.Bool
		// mutex for main data structures
		mutex sync.RWMutex
		// txPending is list of draft vertices waiting for solidification to be sent for validation
		txPending map[core.TransactionID]draftVertexData
		// txDependencies is a list of transaction IDs which are needed for solidification of pending tx-es
		txDependencies map[core.TransactionID]*txDependency
	}

	draftVertexData struct {
		*PrimaryInputConsumerData
		draftVertex *utangle.Vertex
		// stemInputAlreadyPulled for pull sequence and priorities
		stemInputAlreadyPulled            bool
		sequencerPredecessorAlreadyPulled bool
		allInputsAlreadyPulled            bool
	}

	txDependency struct {
		// consumingTxIDs a list of txID which depends on the txid in the key of txDependency map
		// The list should not be empty
		consumingTxIDs []*core.TransactionID
		// since when dependency is known
		since time.Time
		// nextPullDeadline when next pull is scheduled
		nextPullDeadline time.Time
	}
)

const (
	keepNotSolid    = 3 * time.Second // only for testing. Must be longer in reality
	repeatPullAfter = 2 * time.Second
)

func (w *Workflow) initSolidifyConsumer() {
	c := &SolidifyConsumer{
		Consumer:       NewConsumer[*SolidifyInputData](SolidifyConsumerName, w),
		txPending:      make(map[core.TransactionID]draftVertexData),
		txDependencies: make(map[core.TransactionID]*txDependency),
	}
	c.AddOnConsume(func(inp *SolidifyInputData) {
		if inp.Remove {
			c.Debugf(inp.PrimaryInputConsumerData, "IN (remove)")
			return
		}
		if inp.newSolidDependency == nil {
			c.Debugf(inp.PrimaryInputConsumerData, "IN (solidify)")
			c.TraceMilestones(inp.Tx, inp.Tx.ID(), "milestone arrived")
			return
		}
		c.Debugf(inp.PrimaryInputConsumerData, "IN (check dependency)")
	})
	c.AddOnConsume(c.consume)
	c.AddOnClosed(func() {
		c.stopBackgroundLoop.Store(true)
		w.validateConsumer.Stop()
		w.terminateWG.Done()
	})
	w.solidifyConsumer = c
	go c.backgroundLoop()
}

func (c *SolidifyConsumer) IsWaitedTransaction(txid *core.TransactionID) bool {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	_, ret := c.txDependencies[*txid]
	return ret
}

func (c *SolidifyConsumer) consume(inp *SolidifyInputData) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.setTrace(inp.PrimaryInputConsumerData.SourceType == TransactionSourceTypeAPI)

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
	util.Assertf(!already, "transaction is in the solidifier already: %s", inp.Tx.IDString())

	// fetches available inputs, makes draftVertex
	draftVertex, err := c.glb.utxoTangle.MakeDraftVertex(inp.Tx)
	if err != nil {
		// non solidifiable
		c.Debugf(inp.PrimaryInputConsumerData, "%v", err)
		c.IncCounter("err")
		c.removeNonSolidifiableFutureCone(inp.Tx.ID())
		c.RejectTransaction(inp.PrimaryInputConsumerData, "%v", err)
		return
	}

	if solid := !c.putIntoSolidifierIfNeeded(inp, draftVertex); solid {
		// all inputs solid. Send for validation
		util.Assertf(draftVertex.IsSolid(), "v.IsSolid()")
		c.glb.validateConsumer.Push(&ValidateConsumerInputData{
			PrimaryInputConsumerData: inp.PrimaryInputConsumerData,
			draftVertex:              draftVertex,
		})
		return
	}
}

// returns if draftVertex was placed into the solidifier for further tracking
func (c *SolidifyConsumer) putIntoSolidifierIfNeeded(inp *SolidifyInputData, draftVertex *utangle.Vertex) bool {
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
		c.Debugf(inp.PrimaryInputConsumerData, "unknown input tx %s", unknownTxID.StringShort())
	}

	// for each unknown input, add the new draftVertex to the list of txids
	// dependent on it (past cone tips, known consumers)
	nowis := time.Now()
	for wantedTxID := range unknownInputTxIDs {
		if dept, ok := c.txDependencies[wantedTxID]; !ok {
			c.txDependencies[wantedTxID] = &txDependency{
				since:            nowis,
				nextPullDeadline: nowis,
				consumingTxIDs:   []*core.TransactionID{draftVertex.Tx.ID()},
			}
		} else {
			dept.consumingTxIDs = append(dept.consumingTxIDs, draftVertex.Tx.ID())
		}
	}
	// add to the list of vertices waiting for solidification
	vd := draftVertexData{
		PrimaryInputConsumerData: inp.PrimaryInputConsumerData,
		draftVertex:              draftVertex,
	}
	c.txPending[*draftVertex.Tx.ID()] = vd
	c.pullIfNeeded(&vd)
	return true
}

// collectDependingFutureCone collects all known (to solidifier) txids from the future cone which directly or indirectly depend on the txid
// It is recursive traversing of the DAG in the opposite order in the future cone of dependencies
func (c *SolidifyConsumer) collectDependingFutureCone(txid *core.TransactionID, ret map[core.TransactionID]struct{}) {
	if _, already := ret[*txid]; already {
		return
	}
	if dep, isKnownDependency := c.txDependencies[*txid]; isKnownDependency {
		for _, txid1 := range dep.consumingTxIDs {
			c.collectDependingFutureCone(txid1, ret)
		}
	}
	ret[*txid] = struct{}{}
}

// removeNonSolidifiableFutureCone removes from solidifier all txids which directly or indirectly depend on txid
func (c *SolidifyConsumer) removeNonSolidifiableFutureCone(txid *core.TransactionID) {
	c.Log().Debugf("remove non-solidifiable future cone of %s", txid.StringShort())

	ns := make(map[core.TransactionID]struct{})
	c.collectDependingFutureCone(txid, ns)
	for txid1 := range ns {
		if v, ok := c.txPending[txid1]; ok {
			pendingDept := v.draftVertex.PendingDependenciesLines("   ").String()
			v.PrimaryInputConsumerData.eventCallback("finish.remove."+SolidifyConsumerName,
				fmt.Errorf("%s solidication problem. Pending dependencies:\n%s", txid1.StringShort(), pendingDept))
		}

		delete(c.txPending, txid1)
		delete(c.txDependencies, txid1)

		c.Log().Debugf("remove %s", txid1.StringShort())
	}
}

// checkNewDependency checks all pending transactions waiting for the new incoming transaction
// The new vertex has just been added to the tangle
func (c *SolidifyConsumer) checkNewDependency(inp *SolidifyInputData) {
	dep, isKnownDependency := c.txDependencies[*inp.Tx.ID()]
	if !isKnownDependency {
		return
	}
	// it is not needed in the dependencies list anymore
	delete(c.txDependencies, *inp.Tx.ID())

	whoIsWaiting := dep.consumingTxIDs
	util.Assertf(len(whoIsWaiting) > 0, "len(whoIsWaiting)>0")

	solidified := make([]core.TransactionID, 0)
	// looping over pending vertices which are waiting for the dependency newTxID
	for _, txid := range whoIsWaiting {
		pending, found := c.txPending[*txid]
		if !found {
			c.Log().Debugf("%s was waiting for %s, not pending anymore", txid.StringShort(), inp.Tx.IDShort())
			// not pending anymore
			return
		}
		if conflict := pending.draftVertex.FetchMissingDependencies(c.glb.utxoTangle); conflict != nil {
			// tx cannot be solidified, remove
			c.removeNonSolidifiableFutureCone(txid)
			err := fmt.Errorf("conflict at %s", conflict.Short())
			inp.eventCallback("finish.fail."+SolidifyConsumerName, err)
			c.glb.DropTransaction(*txid, "%v", err)
			continue
		}
		if pending.draftVertex.IsSolid() {
			c.Log().Debugf("solidified -> validator: %s", txid.StringShort())
			// all inputs are solid, send it to the validation
			c.glb.validateConsumer.Push(&ValidateConsumerInputData{
				PrimaryInputConsumerData: pending.PrimaryInputConsumerData,
				draftVertex:              pending.draftVertex,
			})
			solidified = append(solidified, *txid)
			continue
		}
		c.Log().Debugf("%s not solid yet. Missing: %s\nTransaction: %s",
			pending.Tx.IDShort(), pending.draftVertex.MissingInputTxIDString(), pending.draftVertex.Lines().String())

		// ask for missing inputs from peering
		c.pullIfNeeded(&pending)
	}
	for i := range solidified {
		delete(c.txPending, solidified[i])
		c.Log().Debugf("removed from solidifier %s", solidified[i].StringShort())
	}
}

const (
	pullGracePeriodBranch            = time.Duration(0)
	pullGracePeriodSequencer         = 1 * time.Second
	pullGracePeriodOtherTransactions = 1 * time.Second
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
			c.pull(vd.Tx.SequencerTransactionData().StemOutputData.PredecessorOutputID.TransactionID(), pullGracePeriodBranch)
			vd.stemInputAlreadyPulled = true
		}
		return
	}

	if vd.Tx.IsSequencerMilestone() {
		//stem is already solid, we can pull sequencer input
		if isSolid, seqInputIdx := vd.draftVertex.IsSequencerInputSolid(); !isSolid {
			seqInputOID := vd.Tx.MustInputAt(seqInputIdx)
			c.pull(seqInputOID.TransactionID(), pullGracePeriodSequencer)
			vd.sequencerPredecessorAlreadyPulled = true
			return
		}
	}

	// now we can pull the rest
	vd.draftVertex.MissingInputTxIDSet().ForEach(func(txid core.TransactionID) bool {
		c.pull(txid, pullGracePeriodOtherTransactions)
		return true
	})
}

func (c *SolidifyConsumer) pull(txid core.TransactionID, gracePeriod time.Duration) {
	c.glb.pullConsumer.Push(&PullTxData{
		Cmd:         PullTxCmdQuery,
		TxID:        txid,
		GracePeriod: gracePeriod,
	})
}

func (c *SolidifyConsumer) backgroundLoop() {
	for !c.stopBackgroundLoop.Load() {
		time.Sleep(100 * time.Millisecond)
		c.doBackgroundCheck()
	}
	c.Log().Debugf("background loop stopped")
}

func (c *SolidifyConsumer) collectToRemove() []core.TransactionID {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	nowis := time.Now()
	ret := make([]core.TransactionID, 0)
	for txid, dep := range c.txDependencies {
		if nowis.After(dep.since.Add(keepNotSolid)) {
			ret = append(ret, txid)
		} else {
			if dep.nextPullDeadline.After(nowis) {
				c.Log().Debugf("PULL NOT IMPLEMENTED: %s", txid.String())
				dep.nextPullDeadline = nowis.Add(repeatPullAfter)
			}
		}
	}
	return ret
}

func (c *SolidifyConsumer) removeDueToDeadline(toRemove []core.TransactionID) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	for _, txid := range toRemove {
		c.removeNonSolidifiableFutureCone(&txid)
		c.glb.DropTransaction(txid, "solidification timeout")
	}
}

func (c *SolidifyConsumer) doBackgroundCheck() {
	toRemove := c.collectToRemove()
	if len(toRemove) > 0 {
		c.removeDueToDeadline(toRemove)

		for i := range toRemove {
			c.glb.DropTransaction(toRemove[i], "solidification timeout")
		}
	}
}

func (d *txDependency) __text(dep *core.TransactionID) string {
	txids := make([]string, 0)
	for _, id := range d.consumingTxIDs {
		txids = append(txids, id.StringShort())
	}
	return fmt.Sprintf("%s <- [%s]", dep.StringShort(), strings.Join(txids, ","))
}

func (c *SolidifyConsumer) DumpUnresolvedDependencies() *lines.Lines {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	ret := lines.New()
	ret.Add("======= unresolved dependencies in solidifier")
	for txid, v := range c.txDependencies {
		ret.Add(v.__text(&txid))
	}
	return ret
}

func (c *SolidifyConsumer) DumpPending() *lines.Lines {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	ret := lines.New()
	ret.Add("======= transactions pending in solidifier")
	for txid, v := range c.txPending {
		ret.Add("pending %s", txid.StringShort())
		ret.Append(v.draftVertex.PendingDependenciesLines("  "))
	}
	return ret
}
