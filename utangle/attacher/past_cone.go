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
	return a.lazyRepeat(func() (status vertex.Status) {
		ok := true
		success := false
		a.vid.Unwrap(vertex.UnwrapOptions{
			Vertex: func(v *vertex.Vertex) {
				ok = a.attachVertex(v, a.vid, core.NilLogicalTime, set.New[*vertex.WrappedTx]())
				if ok {
					success = v.FlagsUp(vertex.FlagsSequencerVertexCompleted)
				}
			},
		})
		switch {
		case !ok:
			return vertex.Bad
		case success:
			return vertex.Good
		default:
			return vertex.Undefined
		}
	})
}

// attachVertex: vid corresponds to the vertex v
func (a *attacher) attachVertex(v *vertex.Vertex, vid *vertex.WrappedTx, parasiticChainHorizon core.LogicalTime, visited set.Set[*vertex.WrappedTx]) (ok bool) {
	util.Assertf(!v.Tx.IsSequencerMilestone() || v.FlagsUp(vertex.FlagBaselineSolid), "v.FlagsUp(vertex.FlagBaselineSolid) in %s", v.Tx.IDShortString)

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
	if !v.FlagsUp(vertex.FlagEndorsementsSolid) {
		// depth-first along endorsements
		return a.attachEndorsements(v, parasiticChainHorizon, visited) // <<< recursive
	}
	// only starting with inputs after endorsements are ok. It ensures all endorsed past cone is known
	// for the attached before going to other dependencies. Note, that endorsing past cone consists only of
	// sequencer milestones which are validated/solidified by their attachers
	inputsOk := a.attachInputs(v, vid, parasiticChainHorizon, visited) // deep recursion
	if !inputsOk {
		return false
	}
	if v.FlagsUp(vertex.FlagAllInputsSolid) {
		// TODO optimization: constraints can be validated even before the vertex becomes good (solidified).
		//  It is enough to have all inputs available, i.e. before solidification

		if err := v.ValidateConstraints(); err != nil {
			a.setReason(err)
			a.tracef("%v", err)
			return false
		}
		a.tracef("constraints has been validated OK: %s", v.Tx.IDShortString())
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
		v.SetFlagUp(vertex.FlagEndorsementsSolid)
	}
	return true
}

func (a *attacher) attachInputs(v *vertex.Vertex, vid *vertex.WrappedTx, parasiticChainHorizon core.LogicalTime, visited set.Set[*vertex.WrappedTx]) (ok bool) {
	util.Assertf(v.FlagsUp(vertex.FlagEndorsementsSolid), "vertex.FlagEndorsementsSolid)")
	a.tracef("attachInputs %s", vid.IDShortString)

	// solidify sequencer input first
	if v.Tx.IsSequencerMilestone() {
		if !v.FlagsUp(vertex.FlagSequencerSolid) {
			a.tracef("solidify sequencer input")
			if !a.attachSequencerInput(v, vid, parasiticChainHorizon, visited) {
				return false
			}
			if !v.FlagsUp(vertex.FlagSequencerSolid) {
				return true
			}
		}
	} else {
		v.SetFlagUp(vertex.FlagSequencerSolid)
	}
	util.Assertf(v.FlagsUp(vertex.FlagSequencerSolid), "v.FlagsUp(vertex.FlagSequencerSolid)")
	// solidify the rest
	a.tracef("solidify other inputs")
	allInputsValidated := true
	var success bool
	for i := range v.Inputs {
		ok, success = a.attachInput(v, byte(i), vid, parasiticChainHorizon, visited)
		if !ok {
			return false
		}
		if !success {
			allInputsValidated = false
		}
	}
	if allInputsValidated {
		v.SetFlagUp(vertex.FlagAllInputsSolid)
	}
	return true
}

func (a *attacher) attachSequencerInput(v *vertex.Vertex, vid *vertex.WrappedTx, parasiticChainHorizon core.LogicalTime, visited set.Set[*vertex.WrappedTx]) (ok bool) {
	a.tracef("attachSequencerInput %s", vid.IDShortString)
	predInputIdx := v.Tx.SequencerTransactionData().SequencerOutputData.ChainConstraint.PredecessorInputIndex
	var success bool
	ok, success = a.attachInput(v, predInputIdx, vid, parasiticChainHorizon, visited)
	if ok && success {
		v.SetFlagUp(vertex.FlagSequencerSolid)
		a.tracef("sequencer input %s solidified", util.Ref(v.Tx.MustInputAt(predInputIdx)).StringShort)
	}
	return
}

func (a *attacher) attachInput(v *vertex.Vertex, inputIdx byte, vid *vertex.WrappedTx, parasiticChainHorizon core.LogicalTime, visited set.Set[*vertex.WrappedTx]) (ok, success bool) {
	a.tracef("attachInput #%d of %s", inputIdx, vid.IDShortString)
	if !a.attachInputID(v, vid, inputIdx) {
		a.tracef("bad input %d", inputIdx)
		return false, false
	}
	util.Assertf(v.Inputs[inputIdx] != nil, "v.Inputs[i] != nil")

	if parasiticChainHorizon == core.NilLogicalTime {
		// TODO revisit parasitic chain threshold because of syncing branches
		parasiticChainHorizon = core.MustNewLogicalTime(v.Inputs[inputIdx].Timestamp().TimeSlot()-maxToleratedParasiticChainSlots, 0)
	}
	wOut := vertex.WrappedOutput{
		VID:   v.Inputs[inputIdx],
		Index: v.Tx.MustOutputIndexOfTheInput(inputIdx),
	}
	if !a.attachOutput(wOut, parasiticChainHorizon, visited) {
		return false, false
	}
	success = a.validPastVertices.Contains(v.Inputs[inputIdx]) || a.isRooted(v.Inputs[inputIdx])
	if success {
		a.tracef("input #%d (%s) solidified", inputIdx, util.Ref(v.Tx.MustInputAt(inputIdx)).StringShort())
	}
	return true, success
}

func (a *attacher) isRooted(vid *vertex.WrappedTx) bool {
	return len(a.rooted[vid]) > 0
}

func (a *attacher) isValidated(vid *vertex.WrappedTx) bool {
	return a.validPastVertices.Contains(vid)
}

func (a *attacher) attachRooted(wOut vertex.WrappedOutput) (ok bool, isRooted bool) {
	a.tracef("attachRooted %s", wOut.IDShortString)

	consumedRooted := a.rooted[wOut.VID]
	if consumedRooted.Contains(wOut.Index) {
		// it means it is already covered. The double spends are checked by attachInputID
		return true, true
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
			ok = a.attachVertex(v, wOut.VID, parasiticChainHorizon, visited) // >>>>>>> recursion
		},
		VirtualTx: func(v *vertex.VirtualTransaction) {
			// add to the pending list
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
	inputOid := consumerVertex.Tx.MustInputAt(inputIdx)
	a.tracef("attachInputID: #%d in %s (oid = %s)", inputIdx, consumerTx.IDShortString, inputOid.StringShort)

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
	// CONFLICT DETECTION
	util.Assertf(a.isKnownVertex(consumerTx), "a.isKnownVertex(consumerTx)")

	a.tracef("before AttachConsumer of %s:\n       good: %s\n       undef: %s",
		inputOid.StringShort, vertex.VIDSetIDString(a.validPastVertices), vertex.VIDSetIDString(a.undefinedPastVertices))

	if !vidInputTx.AttachConsumer(inputOid.Index(), consumerTx, a.checkConflictsFunc(consumerTx)) {
		err := fmt.Errorf("input %s of consumer %s conflicts with existing consumers in the baseline state %s (double spend)",
			inputOid.StringShort(), consumerTx.IDShortString(), a.baselineBranch.IDShortString())
		a.setReason(err)
		a.tracef("%v", err)
		return false
	}
	a.tracef("attached consumer %s of %s", consumerTx.IDShortString, inputOid.StringShort)

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

func (a *attacher) checkConflictsFunc(consumerTx *vertex.WrappedTx) func(existingConsumers set.Set[*vertex.WrappedTx]) bool {
	return func(existingConsumers set.Set[*vertex.WrappedTx]) (conflict bool) {
		defer func() {
			exStr := vertex.VIDSetIDString(existingConsumers)
			a.tracef("checkConflicts: return %v\n    New consumer: %s\n    Existing consumers: %s", conflict, consumerTx.IDShortString, exStr)
		}()
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
		return
	}
}
