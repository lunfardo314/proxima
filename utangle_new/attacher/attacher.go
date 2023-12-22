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
	}

	attacher struct {
		env                   AttachEnvironment
		vid                   *vertex.WrappedTx
		baselineBranch        *vertex.WrappedTx
		baselineStateReader   multistate.SugaredStateReader
		goodPastVertices      set.Set[*vertex.WrappedTx]
		undefinedPastVertices set.Set[*vertex.WrappedTx]
		rootedVertices        set.Set[*vertex.WrappedTx]
		pendingOutputs        set.Set[vertex.WrappedOutput]
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
		rootedVertices:   set.New[*vertex.WrappedTx](),
		goodPastVertices: set.New[*vertex.WrappedTx](),
		pendingOutputs:   set.New[vertex.WrappedOutput](),
	}
	ret.vid.OnNotify(func(msg *vertex.WrappedTx) {
		ret.notify(msg)
	})
	return ret
}

func runAttacher(vid *vertex.WrappedTx, env AttachEnvironment, ctx context.Context) {
	a := newAttacher(vid, env, ctx)
	defer a.close()

	// first solidify baseline state
	status := a.lazyRepeatUntil(func(v *vertex.Vertex) (exit bool, status vertex.Status) {
		if a.baselineBranch == nil {
			status = a._solidifyBaseline(v)
		}
		return a.baselineBranch != nil, status
	})
	a.vid.SetTxStatus(status)
	if a.isFinalStatus() {
		return
	}
	util.Assertf(a.baselineBranch != nil, "a.baselineBranch != nil")
	// baseline is solid, i.e. we know the baseline state the transactions must be solidified upon
	a.baselineStateReader = multistate.MakeSugared(a.env.GetBaselineStateReader(a.baselineBranch))

	// then continue with the rest
	a.lazyRepeatUntil(func(v *vertex.Vertex) (exit bool, status vertex.Status) {
		return false, a.attachVertex(v, a.vid, core.NilLogicalTime)
	})
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

func (a *attacher) lazyRepeatUntil(processVertex func(v *vertex.Vertex) (exit bool, status vertex.Status)) (status vertex.Status) {
	var exit bool

	for {
		exit = true
		a.vid.Unwrap(vertex.UnwrapOptions{
			Vertex: func(v *vertex.Vertex) {
				exit, status = processVertex(v)
			},
		})
		if exit || a.isFinalStatus() {
			return
		}

		select {
		case <-a.ctx.Done():
			return

		case downstreamVID := <-a.inChan:
			if downstreamVID == nil {
				return
			}
			a.vid.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
				exit, status = a.processNotification(v, downstreamVID)
			}})
			if exit || a.isFinalStatus() {
				return
			}

		case <-time.After(periodicCheckEach):
		}
	}
}

func (a *attacher) isFinalStatus() bool {
	return !a.vid.IsVertex() || a.vid.GetTxStatus() != vertex.Undefined
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

func (a *attacher) checkIfRooted(vid *vertex.WrappedTx) bool {
	if a.rootedVertices.Contains(vid) {
		return true
	}
	if a.baselineStateReader.KnowsCommittedTransaction(vid.ID()) {
		a.rootedVertices.Insert(vid)
		return true
	}
	return false
}

func (a *attacher) attachOutput(wOut vertex.WrappedOutput, parasiticChainHorizon core.LogicalTime) vertex.Status {
	status := wOut.VID.GetTxStatus()
	if status != vertex.Undefined {
		return status
	}
	if a.checkIfRooted(wOut.VID) {
		oid := wOut.DecodeID()
		if out := a.baselineStateReader.GetOutput(oid); out != nil {
			ensured := wOut.VID.EnsureOutput(wOut.Index, out)
			util.Assertf(ensured, "attachInputID: inconsistency")
			a.pendingOutputs.Remove(wOut)
			return vertex.Good
		}
		return vertex.Bad
	}
	if wOut.Timestamp().Before(parasiticChainHorizon) {
		// parasitic chain rule
		return vertex.Bad
	}
	// input is not rootedVertices and status is undefined
	a.pendingOutputs.Insert(wOut)
	txid := wOut.VID.ID()
	wOut.VID.Unwrap(vertex.UnwrapOptions{
		Vertex: func(v *vertex.Vertex) {
			status = a.attachVertex(v, wOut.VID, parasiticChainHorizon) // >>>>>>> recursion
		},
		VirtualTx: func(v *vertex.VirtualTransaction) {
			if !txid.IsSequencerMilestone() {
				a.env.Pull(*txid)
			}
		},
	})
	return status
}

func (a *attacher) attachInputID(consumerVertex *vertex.Vertex, consumerTx *vertex.WrappedTx, inputIdx byte) vertex.Status {
	vidInputTx := consumerVertex.Inputs[inputIdx]
	if vidInputTx != nil {
		return vidInputTx.GetTxStatus()
	}
	inputOid := consumerVertex.Tx.MustInputAt(inputIdx)
	vidInputTx = attachTxID(inputOid.TransactionID(), a.env, false)
	status := vidInputTx.GetTxStatus()
	if status == vertex.Bad {
		return vertex.Bad
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
		status = _solidifyStem(v, a.env)
	} else {
		status = _solidifySequencerBaseline(v, a.env)
	}
	a.baselineBranch = v.BaselineBranch
	return
}

func _solidifyStem(v *vertex.Vertex, env AttachEnvironment) (status vertex.Status) {
	status = vertex.Undefined
	stemInputIdx := v.StemInputIndex()
	if v.Inputs[stemInputIdx] == nil {
		// predecessor stem is pendingOutputs
		stemInputOid := v.Tx.MustInputAt(stemInputIdx)
		v.Inputs[stemInputIdx] = attachTxID(stemInputOid.TransactionID(), env, false)
		util.Assertf(v.Inputs[stemInputIdx] != nil, "v.Inputs[stemInputIdx] != nil")
	}
	switch v.Inputs[stemInputIdx].GetTxStatus() {
	case vertex.Good:
		v.BaselineBranch = v.Inputs[stemInputIdx].BaselineBranch()
		util.Assertf(v.BaselineBranch != nil, "a.baselineBranch != nil")
	case vertex.Bad:
		status = vertex.Bad
	}
	return
}

func _solidifySequencerBaseline(v *vertex.Vertex, env AttachEnvironment) (status vertex.Status) {
	status = vertex.Undefined
	// regular sequencer tx. Go to the direction of the baseline branch
	predOid, predIdx := v.Tx.SequencerChainPredecessor()
	util.Assertf(predOid != nil, "inconsistency: sequencer milestone cannot be a chain origin")
	var inputTx *vertex.WrappedTx

	if predOid.TimeSlot() == v.Tx.TimeSlot() {
		// predecessor is on the same slot -> continue towards it
		if v.Inputs[predIdx] == nil {
			v.Inputs[predIdx] = attachTxID(predOid.TransactionID(), env, true)
			util.Assertf(v.Inputs[predIdx] != nil, "v.Inputs[predIdx] != nil")
		}
		inputTx = v.Inputs[predIdx]
	} else {
		// predecessor is on the earlier slot -> follow the first endorsement (guaranteed by the ledger constraint layer)
		util.Assertf(v.Tx.NumEndorsements() > 0, "v.Tx.NumEndorsements()>0")
		if v.Endorsements[0] == nil {
			v.Endorsements[0] = attachTxID(v.Tx.EndorsementAt(0), env, true)
		}
		inputTx = v.Endorsements[0]
	}
	if inputTx.GetTxStatus() == vertex.Bad {
		status = vertex.Bad
	} else {
		v.BaselineBranch = inputTx.BaselineBranch() // may be nil
	}
	return
}

func (a *attacher) processNotification(v *vertex.Vertex, vid *vertex.WrappedTx) (exit bool, status vertex.Status) {
	panic("not implemented")
}
