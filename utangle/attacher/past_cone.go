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
	status := a.lazyRepeat(func() (status vertex.Status) {
		ok := true
		a.vid.Unwrap(vertex.UnwrapOptions{
			Vertex: func(v *vertex.Vertex) {
				ok = a.attachVertex(v, a.vid, core.NilLogicalTime, set.New[*vertex.WrappedTx]())
			},
		})
		if !ok {
			return vertex.Bad
		}
		if a.endorsementsOk {
			return vertex.Good
		}
		return vertex.Undefined
	})

	// run attach vertex once. It will generate pending outputs, if any
	ok := true
	a.vid.Unwrap(vertex.UnwrapOptions{
		Vertex: func(v *vertex.Vertex) {
			ok = a.attachVertex(v, a.vid, core.NilLogicalTime, set.New[*vertex.WrappedTx]())
		},
	})
	if !ok {
		return vertex.Bad
	}
	// run attaching pending outputs until no one left
	status = a.lazyRepeat(func() (status vertex.Status) {
		pending := util.Keys(a.pendingOutputs)
		visited := set.New[*vertex.WrappedTx]()
		for _, wOut := range pending {
			if !a.attachOutput(wOut, a.pendingOutputs[wOut], visited) {
				return vertex.Bad
			}
		}
		status = vertex.Good
		if len(a.pendingOutputs) == 0 {
			a.vid.Unwrap(vertex.UnwrapOptions{
				Vertex: func(v *vertex.Vertex) {
					if !a.attachVertex(v, a.vid, core.NilLogicalTime, set.New[*vertex.WrappedTx]()) {
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
func (a *attacher) attachVertex(v *vertex.Vertex, vid *vertex.WrappedTx, parasiticChainHorizon core.LogicalTime, visited set.Set[*vertex.WrappedTx]) (ok bool) {
	if visited.Contains(vid) {
		return true
	}
	visited.Insert(vid)

	a.tracef("attachVertex %s", vid.IDShortString)
	util.Assertf(!util.IsNil(a.baselineStateReader), "!util.IsNil(a.baselineStateReader)")
	if a.validPastVertices.Contains(vid) {
		return true
	}
	a.pastConeVertexVisited(vid, false)
	if !a.endorsementsOk {
		// depth-first along endorsements
		return a.attachEndorsements(v, parasiticChainHorizon, visited) // <<< recursive
	}
	// only starting with inputs after endorsements are ok. It ensures all endorsed past cone is known
	// for the attached before going to other dependencies. Note, that endorsing past cone consists only of
	// sequencer milestones which are validated/solidified by their attachers
	inputsOk, allValidated := a.attachInputs(v, vid, parasiticChainHorizon, visited)
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
		ok = true
	}
	return true
}

// Attaches endorsements of the vertex
func (a *attacher) attachEndorsements(v *vertex.Vertex, parasiticChainHorizon core.LogicalTime, visited set.Set[*vertex.WrappedTx]) bool {
	a.tracef("attachEndorsements %s", v.Tx.IDShortString)

	allGood := true
	for i, vidEndorsed := range v.Endorsements {
		if vidEndorsed == nil {
			vidEndorsed = AttachTxID(v.Tx.EndorsementAt(byte(i)), a.env, true)
			v.Endorsements[i] = vidEndorsed
		}
		endorsedStatus := vidEndorsed.GetTxStatus()
		if endorsedStatus == vertex.Bad {
			return false
		}
		if a.validPastVertices.Contains(vidEndorsed) {
			// it means past cone of vidEndorsed is fully validated already
			continue
		}
		a.pastConeVertexVisited(vidEndorsed, endorsedStatus == vertex.Good)

		ok := true
		vidEndorsed.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
			ok = a.attachVertex(v, vidEndorsed, parasiticChainHorizon, visited) // <<<<<<<<<<< recursion
		}})
		if !ok {
			return false
		}
		if vidEndorsed.GetTxStatus() != vertex.Good {
			allGood = false
		}
	}
	if allGood {
		a.endorsementsOk = true
	}
	return true
}

func (a *attacher) attachInputs(v *vertex.Vertex, vid *vertex.WrappedTx, parasiticChainHorizon core.LogicalTime, visited set.Set[*vertex.WrappedTx]) (ok, allInputsValidated bool) {
	a.tracef("attachInputs %s", vid.IDShortString)

	allInputsValidated = true
	for i := range v.Inputs {
		if !a.attachInputID(v, vid, byte(i)) {
			a.tracef("bad input %d", i)
			return false, false
		}
		util.Assertf(v.Inputs[i] != nil, "v.Inputs[i] != nil")

		if parasiticChainHorizon == core.NilLogicalTime {
			// TODO revisit parasitic chain threshold because of syncing branches
			parasiticChainHorizon = core.MustNewLogicalTime(v.Inputs[i].Timestamp().TimeSlot()-maxToleratedParasiticChainSlots, 0)
		}
		wOut := vertex.WrappedOutput{
			VID:   v.Inputs[i],
			Index: v.Tx.MustOutputIndexOfTheInput(byte(i)),
		}
		if !a.attachOutput(wOut, parasiticChainHorizon, visited) { // << recursion
			return false, false
		}
		if !a.validPastVertices.Contains(v.Inputs[i]) && !a.isRooted(v.Inputs[i]) {
			// all outputs valid == each output is either validated, or rooted
			allInputsValidated = false
		}
	}
	return true, allInputsValidated
}

func (a *attacher) isRooted(vid *vertex.WrappedTx) bool {
	return len(a.rooted[vid]) > 0
}

func (a *attacher) isValidated(vid *vertex.WrappedTx) bool {
	return a.validPastVertices.Contains(vid)
}

func (a *attacher) attachRooted(wOut vertex.WrappedOutput) (ok bool, isRooted bool) {
	a.tracef("attachRooted IN %s", wOut.IDShortString)

	consumedRooted := a.rooted[wOut.VID]
	if consumedRooted.Contains(wOut.Index) {
		// double spend
		err := fmt.Errorf("fail: rooted output %s is already spent", wOut.IDShortString())
		a.tracef("%v", err)
		a.setReason(err)
		return false, true
	}
	// not a double spend
	stateReader := a.baselineStateReader()

	oid := wOut.DecodeID()
	txid := oid.TransactionID()
	if len(consumedRooted) == 0 && !stateReader.KnowsCommittedTransaction(&txid) {
		// it is not rooted, but it is fine
		return true, false
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
		return true, true
	}
	// output has not been found in the state -> Bad
	err := fmt.Errorf("output %s is not in the state", wOut.IDShortString())
	a.setReason(err)
	a.tracef("%v", err)
	return false, false
}

func (a *attacher) attachOutput(wOut vertex.WrappedOutput, parasiticChainHorizon core.LogicalTime, visited set.Set[*vertex.WrappedTx]) bool {
	a.tracef("attachOutput %s", wOut.IDShortString)
	ok, isRooted := a.attachRooted(wOut)
	if !ok {
		return false
	}
	if isRooted {
		delete(a.pendingOutputs, wOut)
		return true
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
	ok = true
	wOut.VID.Unwrap(vertex.UnwrapOptions{
		Vertex: func(v *vertex.Vertex) {
			// remove from the pending list
			delete(a.pendingOutputs, wOut)
			ok = a.attachVertex(v, wOut.VID, parasiticChainHorizon, visited) // >>>>>>> recursion
			if a.isValidated(wOut.VID) {
				delete(a.pendingOutputs, wOut)
			}
		},
		VirtualTx: func(v *vertex.VirtualTransaction) {
			// add to the pending list
			a.pendingOutputs[wOut] = parasiticChainHorizon
			if !txid.IsSequencerMilestone() {
				a.env.Pull(*txid)
			}
		},
	})
	if !ok {
		return false
	}

	return true
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

	if vidInputTx.GetTxStatus() == vertex.Bad {
		a.setReason(vidInputTx.GetReason())
		return false
	}
	// attach consumer and check for conflicts
	if !vidInputTx.AttachConsumer(inputOid.Index(), consumerTx, a.checkConflicts(consumerTx)) {
		err := fmt.Errorf("input %s of consumer %s conflicts with existing consumers in the baseline state %s",
			inputOid.StringShort(), consumerTx.IDShortString(), a.baselineBranch.IDShortString())
		a.setReason(err)
		a.tracef("%v", err)
		return false
	}
	if vidInputTx.IsSequencerMilestone() {
		// for sequencer milestones check if baselines are compatible
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
