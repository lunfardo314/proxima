package attacher

import (
	"fmt"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/utangle/vertex"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
)

func (a *attacher) solidifyPastCone() vertex.Status {
	// run attach vertex once. It will generate pending outputs
	var ok bool
	a.vid.Unwrap(vertex.UnwrapOptions{
		Vertex: func(v *vertex.Vertex) {
			ok = a.attachVertex(v, a.vid, core.NilLogicalTime)
		},
	})
	if !ok {
		return vertex.Bad
	}
	// run attaching pending outputs until no one left
	status := a.lazyRepeat(func() (status vertex.Status) {
		pending := util.Keys(a.pendingOutputs)
		for _, wOut := range pending {
			if !a.attachOutput(wOut, a.pendingOutputs[wOut]) {
				return vertex.Bad
			}
		}
		status = vertex.Good
		if len(a.pendingOutputs) == 0 {
			a.vid.Unwrap(vertex.UnwrapOptions{
				Vertex: func(v *vertex.Vertex) {
					if !a.attachVertex(v, a.vid, core.NilLogicalTime) {
						status = vertex.Bad
					}
				},
			})
		}
		return
	})
	return status
}

// attachVertex: vid corresponds to the vertex v
func (a *attacher) attachVertex(v *vertex.Vertex, vid *vertex.WrappedTx, parasiticChainHorizon core.LogicalTime) (ok bool) {
	//defer func() {
	//	a.tracef("RETURN attachVertex %s: %s", vid.IDShortString, status.String())
	//}()

	a.tracef("attachVertex %s", vid.IDShortString)

	util.Assertf(!util.IsNil(a.baselineStateReader), "!util.IsNil(a.baselineStateReader)")
	if a.validPastVertices.Contains(vid) && vid.GetTxStatus() == vertex.Good {
		return true
	}
	a.pastConeVertexVisited(vid, false)

	if !a.endorsementsOk {
		// depth-first along endorsements
		return a.attachEndorsements(v, parasiticChainHorizon) // <<< recursive
	}
	// only starting with inputs after endorsements are ok
	inputsOk, allValidated := a.attachInputs(v, vid, parasiticChainHorizon)
	if !inputsOk { // recursive
		return false
	}
	if allValidated {
		// TODO optimization: constraints can be validated even before the vertex becomes good (solidified).
		//  It is enough to have all inputs available, i.e. before solidification

		if err := v.ValidateConstraints(); err != nil {
			a.setReason(err)
			a.tracef("%v", err)
			return false
		}
		a.tracef("validated constraints: %s", v.Tx.IDShortString())
		a.pastConeVertexVisited(vid, true)
	}
	return
}

func (a *attacher) attachEndorsements(v *vertex.Vertex, parasiticChainHorizon core.LogicalTime) (ok bool) {
	a.tracef("attachVertex %s", v.Tx.IDShortString)
	//defer func() {
	//	a.tracef("RETURN attachEndorsements %s: %s", v.Tx.IDShortString, status.String())
	//}()

	allGood := true
	for i, vidEndorsed := range v.Endorsements {
		if vidEndorsed == nil {
			vidEndorsed = AttachTxID(v.Tx.EndorsementAt(byte(i)), a.env, true)
			v.Endorsements[i] = vidEndorsed
		}
		switch vidEndorsed.GetTxStatus() {
		case vertex.Bad:
			a.setReason(vidEndorsed.GetReason())
			return false
		case vertex.Good:
			a.pastConeVertexVisited(vidEndorsed, true)
		case vertex.Undefined:
			a.pastConeVertexVisited(vidEndorsed, false)
			allGood = false
		}

		vidEndorsed.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
			ok = a.attachVertex(v, vidEndorsed, parasiticChainHorizon) // <<<<<<<<<<< recursion
		}})
		if !ok {
			return false
		}
		status := vidEndorsed.GetTxStatus()
		switch status {
		case vertex.Bad:
			return false
		case vertex.Good:
			a.pastConeVertexVisited(vidEndorsed, true)
		case vertex.Undefined:
			a.pastConeVertexVisited(vidEndorsed, false)
			allGood = false
		}
	}
	if allGood {
		a.endorsementsOk = true
	}
	return true
}

func (a *attacher) attachInputs(v *vertex.Vertex, vid *vertex.WrappedTx, parasiticChainHorizon core.LogicalTime) (ok, allInputsValidated bool) {
	a.tracef("attachInputs %s", vid.IDShortString)
	//defer func() {
	//	a.tracef("RETURN attachInputs %s: %s", v.Tx.IDShortString, status.String())
	//}()

	allInputsValidated = true
	for i := range v.Inputs {
		if !a.attachInputID(v, vid, byte(i)) {
			a.tracef("bad input %d", i)
			return false, false
		}
		util.Assertf(v.Inputs[i] != nil, "v.Inputs[i] != nil")

		if parasiticChainHorizon == core.NilLogicalTime {
			// TODO revisit parasitic chain threshold because of syncing
			parasiticChainHorizon = core.MustNewLogicalTime(v.Inputs[i].Timestamp().TimeSlot()-maxToleratedParasiticChainSlots, 0)
		}
		wOut := vertex.WrappedOutput{
			VID:   v.Inputs[i],
			Index: v.Tx.MustOutputIndexOfTheInput(byte(i)),
		}
		if !a.attachOutput(wOut, parasiticChainHorizon) { // << recursion
			return false, false
		}
		if !a.validPastVertices.Contains(v.Inputs[i]) {
			allInputsValidated = false
		}
	}
	return true, allInputsValidated
}

func (a *attacher) attachRooted(wOut vertex.WrappedOutput) (ok bool) {
	a.tracef("attachRooted IN %s", wOut.IDShortString)
	//defer func() {
	//	a.tracef("RETURN attachRooted %s: %s", wOut.IDShortString, status.String())
	//}()

	consumedRooted := a.rooted[wOut.VID]
	if consumedRooted.Contains(wOut.Index) {
		// double spend
		err := fmt.Errorf("fail: rooted output %s is already spent", wOut.IDShortString())
		a.tracef("%v", err)
		a.setReason(err)
		return false
	}
	// not a double spend
	stateReader := a.baselineStateReader()

	oid := wOut.DecodeID()
	txid := oid.TransactionID()
	if len(consumedRooted) == 0 && !stateReader.KnowsCommittedTransaction(&txid) {
		// it is not rooted, but it is fine
		return true
	}
	// it is rooted -> must be in the state
	// check if output is in the state
	if out := stateReader.GetOutput(oid); out != nil {
		// output has been found in the state -> Good
		ensured := wOut.VID.EnsureOutput(wOut.Index, out)
		util.Assertf(ensured, "ensureOutput: internal inconsistency")
		if len(consumedRooted) == 0 {
			consumedRooted = set.New[byte]()
		}
		consumedRooted.Insert(wOut.Index)
		a.rooted[wOut.VID] = consumedRooted
		return true
	}
	// output has not been found in the state -> Bad
	err := fmt.Errorf("output %s is not in the state", wOut.IDShortString())
	a.setReason(err)
	a.tracef("%v", err)
	return false
}

func (a *attacher) attachOutput(wOut vertex.WrappedOutput, parasiticChainHorizon core.LogicalTime) (ok bool) {
	a.tracef("attachOutput %s", wOut.IDShortString)
	//defer func() {
	//	a.tracef("RETURN attachOutput %s: %s", wOut.IDShortString, status.String())
	//}()

	if !a.attachRooted(wOut) {
		return false
	}

	if wOut.Timestamp().Before(parasiticChainHorizon) {
		// parasitic chain rule
		err := fmt.Errorf("parasitic chain threshold %s broken while attaching output %s", parasiticChainHorizon.String(), wOut.IDShortString())
		a.setReason(err)
		a.tracef("%v", err)
		return false
	}

	// input is not rooted
	txid := wOut.VID.ID()
	wOut.VID.Unwrap(vertex.UnwrapOptions{
		Vertex: func(v *vertex.Vertex) {
			// remove from the pending list
			delete(a.pendingOutputs, wOut)
			ok = a.attachVertex(v, wOut.VID, parasiticChainHorizon) // >>>>>>> recursion
		},
		VirtualTx: func(v *vertex.VirtualTransaction) {
			// add to the pending list
			a.pendingOutputs[wOut] = parasiticChainHorizon
			if !txid.IsSequencerMilestone() {
				a.env.Pull(*txid)
			}
		},
	})
	return
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

func (a *attacher) attachInputID(consumerVertex *vertex.Vertex, consumerTx *vertex.WrappedTx, inputIdx byte) (ok bool) {
	a.tracef("attachInputID: tx: %s, inputIdx: %d", consumerTx.IDShortString, inputIdx)

	inputOid := consumerVertex.Tx.MustInputAt(inputIdx)
	vidInputTx := consumerVertex.Inputs[inputIdx]
	if vidInputTx == nil {
		vidInputTx = AttachTxID(inputOid.TransactionID(), a.env, false)
	}
	util.Assertf(vidInputTx != nil, "vidInputTx != nil")

	// attach consumer and check for conflicts. Does not matter the status
	if !vidInputTx.AttachConsumer(inputOid.Index(), consumerTx, a.checkConflicts(consumerTx)) {
		err := fmt.Errorf("input %s of consumer %s conflicts with existing consumers in the baseline state %s",
			inputOid.StringShort(), consumerTx.IDShortString(), a.baselineBranch.IDShortString())
		a.setReason(err)
		a.tracef("%v", err)
		return false
	}
	// no conflicts, now check the status
	if vidInputTx.GetTxStatus() == vertex.Bad {
		a.setReason(vidInputTx.GetReason())
		return false
	}
	if vidInputTx.IsSequencerMilestone() {
		// for sequencer milestones check if baselines do not conflict
		if inputBaselineBranch := vidInputTx.BaselineBranch(); inputBaselineBranch != nil {
			if !a.branchesCompatible(a.baselineBranch, inputBaselineBranch) {
				err := fmt.Errorf("branches %s and %s not compatible", a.baselineBranch.IDShortString(), inputBaselineBranch.IDShortString())
				a.setReason(err)
				a.tracef("%v", err)
				return false
			}
		}
	}
	consumerVertex.Inputs[inputIdx] = vidInputTx
	return true
}

func (a *attacher) checkConflicts(consumerTx *vertex.WrappedTx) func(existingConsumers set.Set[*vertex.WrappedTx]) bool {
	return func(existingConsumers set.Set[*vertex.WrappedTx]) bool {
		conflict := false
		existingConsumers.ForEach(func(existingConsumer *vertex.WrappedTx) bool {
			if existingConsumer == consumerTx {
				return true
			}
			if a.validPastVertices.Contains(existingConsumer) {
				conflict = true
				return false
			}
			if a.undefinedPastVertices.Contains(existingConsumer) {
				conflict = true
				return false
			}
			return true
		})
		return conflict
	}
}
