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

// attachVertex: vid corresponds to the vertex v
func (a *attacher) attachVertex(v *vertex.Vertex, vid *vertex.WrappedTx, parasiticChainHorizon core.LogicalTime) vertex.Status {
	a.tracef("attachVertex %s", vid.IDShortString)

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
	status := a.attachInputs(v, vid, parasiticChainHorizon) // recursive
	if status == vertex.Good {
		a.undefinedPastVertices.Remove(vid)
		a.goodPastVertices.Insert(vid)
	}
	return status
}

func (a *attacher) attachEndorsements(v *vertex.Vertex, parasiticChainHorizon core.LogicalTime) vertex.Status {
	a.tracef("attachVertex %s", v.Tx.IDShortString)

	allGood := true
	var status vertex.Status

	for i, vidEndorsed := range v.Endorsements {
		if vidEndorsed == nil {
			vidEndorsed = AttachTxID(v.Tx.EndorsementAt(byte(i)), a.env, true)
			v.Endorsements[i] = vidEndorsed
		}

		switch vidEndorsed.GetTxStatus() {
		case vertex.Bad:
			a.setReason(vidEndorsed.GetReason())
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
	a.tracef("attachInputs %s", vid.IDShortString)

	allGood := true
	for i := range v.Inputs {
		switch status = a.attachInputID(v, vid, byte(i)); status {
		case vertex.Bad:
			a.tracef("bad input %d", i)
			return
		case vertex.Undefined:
			allGood = false
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
		status = a.attachOutput(wOut, parasiticChainHorizon) // << recursion

		switch status {
		case vertex.Bad:
			a.tracef("failed to attach output %s of the input #%d in tx %s", wOut.IDShortString, i, v.Tx.IDShortString)
			return // Invalidate
		case vertex.Undefined:
			allGood = false
		}
	}
	if allGood {
		// TODO optimization: constraints can be validated even before the vertex becomes good (solidified).
		//  It is enough to have all inputs available, i.e. before solidification
		if err := v.ValidateConstraints(); err == nil {
			status = vertex.Good
		} else {
			a.setReason(fmt.Errorf("%s -> '%v'", v.Tx.IDShortString(), err))
			status = vertex.Bad
		}
	}
	return status
}

func (a *attacher) attachRooted(wOut vertex.WrappedOutput) vertex.Status {
	a.tracef("attachRooted IN %s", wOut.IDShortString)
	defer a.tracef("attachRooted OUT %s", wOut.IDShortString)

	status := vertex.Undefined
	consumedRooted := a.rooted[wOut.VID]
	stateReader := a.baselineStateReader()

	if len(consumedRooted) == 0 {
		if stateReader.KnowsCommittedTransaction(wOut.VID.ID()) {
			consumedRooted = set.New(wOut.Index)
			status = vertex.Good
		}
	} else {
		// transaction has consumed outputs -> it is rooted
		// <<<< TODO attach consumer even if conflict
		if consumedRooted.Contains(wOut.Index) {
			// double spend
			err := fmt.Errorf("fail: rooted output %s is already spent", wOut.IDShortString())
			a.tracef("%v", err)
			a.setReason(err)
			status = vertex.Bad
		} else {
			oid := wOut.DecodeID()
			if out := stateReader.GetOutput(oid); out != nil {
				consumedRooted.Insert(wOut.Index)
				ensured := wOut.VID.EnsureOutput(wOut.Index, out) // FIXME deadlock
				util.Assertf(ensured, "attachInputID: inconsistency")
				status = vertex.Good
			} else {
				// transaction is known, but output is already spent
				err := fmt.Errorf("output %s is not in the state", wOut.IDShortString())
				a.setReason(err)
				a.tracef("%v", err)
				status = vertex.Bad
			}
		}
	}
	if status == vertex.Good {
		a.rooted[wOut.VID] = consumedRooted
	}
	return status
}

func (a *attacher) attachOutput(wOut vertex.WrappedOutput, parasiticChainHorizon core.LogicalTime) vertex.Status {
	a.tracef("attachOutput %s", wOut.IDShortString)

	_, alreadyPending := a.pendingOutputs[wOut]
	util.Assertf(!alreadyPending, "inconsistency: unexpected wrapped output in the pending list")

	status := a.attachRooted(wOut)
	if status != vertex.Undefined {
		return status
	}
	if wOut.Timestamp().Before(parasiticChainHorizon) {
		// parasitic chain rule
		err := fmt.Errorf("parasitic chain threshold %s broken while attaching output %s", parasiticChainHorizon.String(), wOut.IDShortString())
		a.setReason(err)
		a.tracef("%v", err)
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
	a.tracef("attachInputID: tx: %s, inputIdx: %d", consumerTx.IDShortString, inputIdx)

	vidInputTx := consumerVertex.Inputs[inputIdx]
	if vidInputTx != nil {
		if vidInputTx.GetTxStatus() == vertex.Bad {
			a.setReason(vidInputTx.GetReason())
			a.tracef("input tx is bad: %s", vidInputTx.IDShortString)
			return vertex.Bad
		}
		if vidInputTx.IsSequencerMilestone() {
			if inputBaselineBranch := vidInputTx.BaselineBranch(); inputBaselineBranch != nil {
				if !a.branchesCompatible(a.baselineBranch, inputBaselineBranch) {
					err := fmt.Errorf("branches not compatible: %s and %s", a.baselineBranch.IDShortString(), inputBaselineBranch.IDShortString())
					a.setReason(err)
					a.tracef("%v", err)
					return vertex.Bad
				}
			}
			status := vidInputTx.GetTxStatus()
			if status == vertex.Bad {
				a.setReason(vidInputTx.GetReason())
			}
			return status
		}
	}

	inputOid := consumerVertex.Tx.MustInputAt(inputIdx)
	vidInputTx = AttachTxID(inputOid.TransactionID(), a.env, false)
	status := vidInputTx.GetTxStatus()
	if status == vertex.Bad {
		a.setReason(vidInputTx.GetReason())
		return vertex.Bad
	}
	if vidInputTx.IsSequencerMilestone() {
		if inputBaselineBranch := vidInputTx.BaselineBranch(); inputBaselineBranch != nil {
			if !a.branchesCompatible(a.baselineBranch, inputBaselineBranch) {
				err := fmt.Errorf("branches %s and %s not compatible", a.baselineBranch.IDShortString(), inputBaselineBranch.IDShortString())
				a.setReason(err)
				a.tracef("%v", err)
				return vertex.Bad
			}
		}
		consumerVertex.Inputs[inputIdx] = vidInputTx
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
		err := fmt.Errorf("input %s of consumer %s conflicts with exiting consumers in the baseline state %s",
			inputOid.StringShort(), consumerTx.IDShortString(), a.baselineBranch.IDShortString())
		a.setReason(err)
		a.tracef("%v", err)
		return vertex.Bad
	}
	consumerVertex.Inputs[inputIdx] = vidInputTx
	return status
}
