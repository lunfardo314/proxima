package attacher

import (
	"context"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/utangle_new/vertex"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"go.uber.org/zap"
)

type (
	AttachEnvironment interface {
		Log() *zap.SugaredLogger
		WithGlobalWriteLock(fun func())
		GetVertexNoLock(txid *core.TransactionID) *vertex.WrappedTx
		AddVertexNoLock(vid *vertex.WrappedTx)
		StateStore() global.StateStore
		GetBaselineStateReader(branch *vertex.WrappedTx) global.IndexedStateReader
		AddBranchNoLock(branch *vertex.WrappedTx, branchData *multistate.BranchData)
		Pull(txid core.TransactionID)
		OnChangeNotify(onChange, notify *vertex.WrappedTx)
		Notify(changed *vertex.WrappedTx)
	}

	attacher struct {
		env                   AttachEnvironment
		vid                   *vertex.WrappedTx
		baselineBranch        *vertex.WrappedTx
		baselineStateReader   multistate.SugaredStateReader
		goodPastVertices      set.Set[*vertex.WrappedTx]
		undefinedPastVertices set.Set[*vertex.WrappedTx]
		rooted                map[*vertex.WrappedTx]set.Set[byte]
		pendingOutputs        map[vertex.WrappedOutput]core.LogicalTime
		closeMutex            sync.RWMutex
		inChan                chan *vertex.WrappedTx
		ctx                   context.Context
		closed                bool
		endorsementsOk        bool
	}
)

const (
	periodicCheckEach               = 500 * time.Millisecond
	maxToleratedParasiticChainTicks = core.TimeTicksPerSlot
)

func newAttacher(vid *vertex.WrappedTx, env AttachEnvironment, ctx context.Context) *attacher {
	ret := &attacher{
		ctx:              ctx,
		vid:              vid,
		env:              env,
		inChan:           make(chan *vertex.WrappedTx, 1),
		rooted:           make(map[*vertex.WrappedTx]set.Set[byte]),
		goodPastVertices: set.New[*vertex.WrappedTx](),
		pendingOutputs:   make(map[vertex.WrappedOutput]core.LogicalTime),
	}
	ret.vid.OnNotify(func(msg *vertex.WrappedTx) {
		ret.notify(msg)
	})
	return ret
}

func runAttacher(vid *vertex.WrappedTx, env AttachEnvironment, ctx context.Context) vertex.Status {
	a := newAttacher(vid, env, ctx)
	defer a.close()

	// first solidify baseline state
	status := a.solidifyBaselineState()
	if status != vertex.Good {
		return vertex.Bad
	}

	util.Assertf(a.baselineBranch != nil, "a.baselineBranch != nil")
	// baseline is solid, i.e. we know the baseline state the transactions must be solidified upon
	a.baselineStateReader = multistate.MakeSugared(a.env.GetBaselineStateReader(a.baselineBranch))

	// then continue with the rest
	status = a.solidifyPastCone()
	if status != vertex.Good {
		return vertex.Bad
	}

	status = a.finalize()
	if status != vertex.Good {
		return vertex.Bad
	}

	return vertex.Good
}

func (a *attacher) solidifyBaselineState() vertex.Status {
	return a.lazyRepeat(func() (status vertex.Status) {
		status = vertex.Bad
		a.vid.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
			if a.baselineBranch == nil {
				status = a._solidifyBaseline(v)
			}
		}})
		if a.baselineBranch != nil {
			return vertex.Good
		}
		return status
	})
}

func (a *attacher) solidifyPastCone() vertex.Status {
	// run attach vertex once. It will generate pending outputs
	status := vertex.Bad
	a.vid.Unwrap(vertex.UnwrapOptions{
		Vertex: func(v *vertex.Vertex) {
			status = a.attachVertex(v, a.vid, core.NilLogicalTime)
		},
	})
	if status != vertex.Undefined {
		return status
	}
	// run attaching pending outputs until no one left
	return a.lazyRepeat(func() (status vertex.Status) {
		pending := util.Keys(a.pendingOutputs)
		for _, wOut := range pending {
			status = a.attachOutput(wOut, a.pendingOutputs[wOut])
			if status == vertex.Bad {
				return vertex.Bad
			}
		}
		if len(a.pendingOutputs) == 0 {
			return vertex.Good
		}
		return vertex.Undefined
	})
}

func (a *attacher) finalize() vertex.Status {
	util.Assertf(a.vid.GetTxStatus() == vertex.Good, "a.vid.GetTxStatus() == vertex.Good")
	util.Assertf(len(a.undefinedPastVertices) == 0, "len(a.undefinedPastVertices)==0")
	util.Assertf(len(a.pendingOutputs) == 0, "len(a.pendingOutputs)==0")

	coverage := a.calculateCoverage()
	if a.vid.IsBranchTransaction() {
		a.commitBranch(coverage)
	}
	// TODO
}

func (a *attacher) commitBranch(coverage multistate.LedgerCoverage) {
	util.Assertf(a.vid.IsBranchTransaction(), "a.vid.IsBranchTransaction()")

	muts := multistate.NewMutations()

	// generate DEL mutations
	for vid, consumed := range a.rooted {
		util.Assertf(len(consumed) > 0, "len(consumed)>0")
		for idx := range consumed {
			muts.InsertDelOutputMutation(vid.OutputID(idx))
		}
	}
	// generate ADD TX and ADD OUTPUT mutations
	for vid := range a.goodPastVertices {
		muts.InsertAddTxMutation(*vid.ID(), a.vid.TimeSlot())

		// ADD OUTPUT mutations only for not consumed outputs
		producedOutputIndices := vid.NotConsumedOutputIndices(a.goodPastVertices)
		for _, idx := range producedOutputIndices {
			muts.InsertAddOutputMutation(vid.OutputID(idx), vid.MustOutputAt(idx))
		}
	}

	seqID, stemOID := a.vid.MustSequencerIDAndStemID()
	upd := multistate.MustNewUpdatable(a.env.StateStore(), a.baselineStateReader.Root())
	upd.MustUpdate(muts, &stemOID, &seqID, coverage)
}

func (a *attacher) calculateCoverage() multistate.LedgerCoverage {

}

// attachVertex: vid corresponds to the vertex v
func (a *attacher) attachVertex(v *vertex.Vertex, vid *vertex.WrappedTx, parasiticChainHorizon core.LogicalTime) vertex.Status {
	util.Assertf(!util.IsNil(a.baselineStateReader), "!util.IsNil(a.baselineStateReader)")
	if a.goodPastVertices.Contains(vid) {
		return vertex.Good
	}
	a.undefinedPastVertices.Insert(vid)

	if !a.endorsementsOk {
		// depth-first along endorsements
		if status := a.attachEndorsements(v, parasiticChainHorizon); status != vertex.Good { // <<< recursive
			return status
		}
		a.endorsementsOk = true
	}
	// only starting with inputs after endorsements are ok
	return a.attachInputs(v, vid, parasiticChainHorizon) // recursive
}

func (a *attacher) attachEndorsements(v *vertex.Vertex, parasiticChainHorizon core.LogicalTime) vertex.Status {
	allGood := true
	var status vertex.Status

	for i, vidEndorsed := range v.Endorsements {
		if vidEndorsed == nil {
			vidEndorsed = attachTxID(v.Tx.EndorsementAt(byte(i)), a.env, true)
			v.Endorsements[i] = vidEndorsed
		}
		switch vidEndorsed.GetTxStatus() {
		case vertex.Bad:
			return vertex.Bad
		case vertex.Good:
			a.goodPastVertices.Insert(vidEndorsed)
			a.undefinedPastVertices.Remove(vidEndorsed)
		case vertex.Undefined:
			a.undefinedPastVertices.Insert(vidEndorsed)
			allGood = false
		}

		status = vertex.Undefined
		vidEndorsed.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
			status = a.attachVertex(v, vidEndorsed, parasiticChainHorizon) // <<<<<<<<<<< recursion
		}})
		switch status {
		case vertex.Bad:
			return vertex.Bad
		case vertex.Good:
			a.goodPastVertices.Insert(vidEndorsed)
			a.undefinedPastVertices.Remove(vidEndorsed)
		case vertex.Undefined:
			a.undefinedPastVertices.Insert(vidEndorsed)
			allGood = false
		}
	}
	if allGood {
		status = vertex.Good
	}
	return status
}

func (a *attacher) attachInputs(v *vertex.Vertex, vid *vertex.WrappedTx, parasiticChainHorizon core.LogicalTime) (status vertex.Status) {
	allGood := true
	for i := range v.Inputs {
		switch status = a.attachInputID(v, vid, byte(i)); status {
		case vertex.Bad:
			return // invalidate
		case vertex.Undefined:
			allGood = false
		}
		util.Assertf(v.Inputs[i] != nil, "v.Inputs[i] != nil")

		if parasiticChainHorizon == core.NilLogicalTime {
			parasiticChainHorizon = v.Inputs[i].Timestamp().AddTimeTicks(maxToleratedParasiticChainTicks)
		}
		status = a.attachOutput(vertex.WrappedOutput{
			VID:   v.Inputs[i],
			Index: v.Tx.MustOutputIndexOfTheInput(byte(i)),
		}, parasiticChainHorizon) // << recursion

		switch status {
		case vertex.Bad:
			return // Invalidate
		case vertex.Undefined:
			allGood = false
		}
	}
	if allGood {
		status = vertex.Good
	}
	return status
}

func (a *attacher) attachRooted(wOut vertex.WrappedOutput) vertex.Status {
	status := vertex.Undefined
	consumed := a.rooted[wOut.VID]
	if len(consumed) == 0 {
		if a.baselineStateReader.KnowsCommittedTransaction(wOut.VID.ID()) {
			consumed = set.New(wOut.Index)
			status = vertex.Good
		}
	} else {
		// transaction is rooted
		if consumed.Contains(wOut.Index) {
			// double spend
			status = vertex.Bad
		} else {
			oid := wOut.DecodeID()
			if out := a.baselineStateReader.GetOutput(oid); out != nil {
				consumed.Insert(wOut.Index)
				ensured := wOut.VID.EnsureOutput(wOut.Index, out)
				util.Assertf(ensured, "attachInputID: inconsistency")
				status = vertex.Good
			} else {
				// transaction is known, but output is already spent
				status = vertex.Bad
			}
		}
	}
	if status == vertex.Good {
		a.rooted[wOut.VID] = consumed
	}
	return status
}

func (a *attacher) attachOutput(wOut vertex.WrappedOutput, parasiticChainHorizon core.LogicalTime) vertex.Status {
	_, alreadyPending := a.pendingOutputs[wOut]
	util.Assertf(!alreadyPending, "inconsistency: unexpected wrapped output in the pending list")

	status := a.attachRooted(wOut)
	if status != vertex.Undefined {
		return status
	}
	if wOut.Timestamp().Before(parasiticChainHorizon) {
		// parasitic chain rule
		return vertex.Bad
	}

	// input is not rooted and status is undefined
	txid := wOut.VID.ID()
	wOut.VID.Unwrap(vertex.UnwrapOptions{
		Vertex: func(v *vertex.Vertex) {
			// remove from the pending list
			delete(a.pendingOutputs, wOut)
			status = a.attachVertex(v, wOut.VID, parasiticChainHorizon) // >>>>>>> recursion
		},
		VirtualTx: func(v *vertex.VirtualTransaction) {
			// add to the pending list
			a.pendingOutputs[wOut] = parasiticChainHorizon
			if !txid.IsSequencerMilestone() {
				a.env.Pull(*txid)
			}
		},
	})
	return status
}

func (a *attacher) branchesCompatible(vid1, vid2 *vertex.WrappedTx) bool {
	util.Assertf(vid1 != nil && vid2 != nil, "vid1 != nil && vid2 != nil")
	util.Assertf(vid1.IsBranchTransaction() && vid2.IsBranchTransaction(), "vid1.IsBranchTransaction() && vid2.IsBranchTransaction()")
	switch {
	case vid1 == vid2:
		return true
	case vid1.TimeSlot() == vid2.TimeSlot():
		return false
	case vid1.TimeSlot() < vid2.TimeSlot():
		return multistate.BranchIsDescendantOf(vid2.ID(), vid1.ID(), a.env.StateStore)
	default:
		return multistate.BranchIsDescendantOf(vid1.ID(), vid2.ID(), a.env.StateStore)
	}
}

func (a *attacher) attachInputID(consumerVertex *vertex.Vertex, consumerTx *vertex.WrappedTx, inputIdx byte) vertex.Status {
	vidInputTx := consumerVertex.Inputs[inputIdx]
	if vidInputTx != nil {
		if vidInputTx.GetTxStatus() == vertex.Bad {
			return vertex.Bad
		}
		if vidInputTx.IsSequencerMilestone() {
			if inputBaselineBranch := vidInputTx.BaselineBranch(); inputBaselineBranch != nil {
				if !a.branchesCompatible(a.baselineBranch, inputBaselineBranch) {
					return vertex.Bad
				}
			}
			return vidInputTx.GetTxStatus()
		}
	}

	inputOid := consumerVertex.Tx.MustInputAt(inputIdx)
	vidInputTx = attachTxID(inputOid.TransactionID(), a.env, false)
	status := vidInputTx.GetTxStatus()
	if status == vertex.Bad {
		return vertex.Bad
	}
	if vidInputTx.IsSequencerMilestone() {
		if inputBaselineBranch := vidInputTx.BaselineBranch(); inputBaselineBranch != nil {
			if !a.branchesCompatible(a.baselineBranch, inputBaselineBranch) {
				return vertex.Bad
			}
		}
		return vidInputTx.GetTxStatus()
	}

	conflict := vidInputTx.AttachConsumer(inputOid.Index(), consumerTx, func(existingConsumers set.Set[*vertex.WrappedTx]) bool {
		conflict1 := false
		existingConsumers.ForEach(func(existingConsumer *vertex.WrappedTx) bool {
			if existingConsumer == consumerTx {
				return true
			}
			if a.goodPastVertices.Contains(existingConsumer) {
				conflict1 = true
				return false
			}
			if a.undefinedPastVertices.Contains(existingConsumer) {
				conflict1 = true
				return false
			}
			return true
		})
		return conflict1
	})
	if conflict {
		return vertex.Bad
	}
	consumerVertex.Inputs[inputIdx] = vidInputTx
	return status
}

// _solidifyBaseline directs attachment process down the DAG to reach the deterministically known baseline state
// for a sequencer milestone. Existence of it is guaranteed by the ledger constraints
func (a *attacher) _solidifyBaseline(v *vertex.Vertex) (status vertex.Status) {
	if v.Tx.IsBranchTransaction() {
		status = a._solidifyStem(v)
	} else {
		status = a._solidifySequencerBaseline(v)
	}
	a.baselineBranch = v.BaselineBranch
	return
}

func (a *attacher) _solidifyStem(v *vertex.Vertex) vertex.Status {
	stemInputIdx := v.StemInputIndex()
	if v.Inputs[stemInputIdx] == nil {
		// predecessor stem is pending
		stemInputOid := v.Tx.MustInputAt(stemInputIdx)
		v.Inputs[stemInputIdx] = attachTxID(stemInputOid.TransactionID(), a.env, false)
	}
	util.Assertf(v.Inputs[stemInputIdx] != nil, "v.Inputs[stemInputIdx] != nil")

	status := v.Inputs[stemInputIdx].GetTxStatus()
	switch status {
	case vertex.Good:
		v.BaselineBranch = v.Inputs[stemInputIdx].BaselineBranch()
		util.Assertf(v.BaselineBranch != nil, "a.baselineBranch != nil")
		return vertex.Good
	case vertex.Bad:
	case vertex.Undefined:
		a.env.OnChangeNotify(v.Inputs[stemInputIdx], a.vid)
	default:
		panic("wrong state")
	}
	return status
}

func (a *attacher) _solidifySequencerBaseline(v *vertex.Vertex) vertex.Status {
	// regular sequencer tx. Go to the direction of the baseline branch
	predOid, predIdx := v.Tx.SequencerChainPredecessor()
	util.Assertf(predOid != nil, "inconsistency: sequencer milestone cannot be a chain origin")
	var inputTx *vertex.WrappedTx

	if predOid.TimeSlot() == v.Tx.TimeSlot() {
		// predecessor is on the same slot -> continue towards it
		if v.Inputs[predIdx] == nil {
			v.Inputs[predIdx] = attachTxID(predOid.TransactionID(), a.env, true)
			util.Assertf(v.Inputs[predIdx] != nil, "v.Inputs[predIdx] != nil")
		}
		inputTx = v.Inputs[predIdx]
	} else {
		// predecessor is on the earlier slot -> follow the first endorsement (guaranteed by the ledger constraint layer)
		util.Assertf(v.Tx.NumEndorsements() > 0, "v.Tx.NumEndorsements()>0")
		if v.Endorsements[0] == nil {
			v.Endorsements[0] = attachTxID(v.Tx.EndorsementAt(0), a.env, true)
		}
		inputTx = v.Endorsements[0]
	}
	status := inputTx.GetTxStatus()
	switch status {
	case vertex.Good:
		v.BaselineBranch = inputTx.BaselineBranch() // may be nil
	case vertex.Bad:
	case vertex.Undefined:
		a.env.OnChangeNotify(inputTx, a.vid)
	}
	return status
}

func (a *attacher) close() {
	a.closeMutex.Lock()
	defer a.closeMutex.Unlock()

	a.closed = true
	a.vid.OnNotify(nil)
	close(a.inChan)
}

func (a *attacher) notify(msg *vertex.WrappedTx) {
	a.closeMutex.RLock()
	defer a.closeMutex.RUnlock()

	if !a.closed {
		a.inChan <- msg
	}
}

func (a *attacher) lazyRepeat(fun func() vertex.Status) (status vertex.Status) {
	for {
		if fun() != vertex.Undefined {
			return
		}
		select {
		case <-a.ctx.Done():
			return
		case <-a.inChan:
		case <-time.After(periodicCheckEach):
		}
	}
}
