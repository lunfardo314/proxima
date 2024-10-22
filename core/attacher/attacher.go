package attacher

import (
	"errors"
	"fmt"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lazyargs"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/lunfardo314/unitrie/common"
)

func newPastConeAttacher(env Environment, name string) attacher {
	ret := attacher{
		Environment: env,
		name:        name,
		pokeMe:      func(_ *vertex.WrappedTx) {},
		pastCone:    vertex.NewPastCone(env, name),
	}
	// standard conflict checker
	ret.checkConflictsFunc = func(_ *vertex.Vertex, consumerTx *vertex.WrappedTx) checkConflictingConsumersFunc {
		return ret.stdCheckConflictsFunc(consumerTx)
	}
	return ret
}

const (
	TraceTagAttach             = "attach"
	TraceTagAttachOutput       = "attachOutput"
	TraceTagAttachVertex       = "attachVertexUnwrapped"
	TraceTagCoverageAdjustment = "adjust"
)

func (a *attacher) Name() string {
	return a.name
}

func (a *attacher) baselineStateReader() multistate.SugaredStateReader {
	return multistate.MakeSugared(a.GetStateReaderForTheBranch(&a.baseline.ID))
}

func (a *attacher) setError(err error) {
	a.Tracef(TraceTagAttach, "set err: '%v'", err)
	a.err = err
}

// solidifyBaselineVertex directs attachment process down the MemDAG to reach the deterministically known baseline state
// for a sequencer milestone. Existence of it is guaranteed by the ledger constraints
// Success of the baseline solidification is when function returns true and v.BaselineBranch != nil
func (a *attacher) solidifyBaselineVertex(v *vertex.Vertex, vidUnwrapped *vertex.WrappedTx) (ok bool) {
	a.Assertf(a.baseline == nil, "a.baseline == nil")
	if v.Tx.IsBranchTransaction() {
		return a.solidifyStemOfTheVertex(v, vidUnwrapped)
	}
	return a.solidifySequencerBaseline(v, vidUnwrapped)
}

func (a *attacher) solidifyStemOfTheVertex(v *vertex.Vertex, vidUnwrapped *vertex.WrappedTx) (ok bool) {
	a.Assertf(v.BaselineBranch == nil, "v.BaselineBranchTxID == nil")

	stemInputIdx := v.StemInputIndex()
	stemInputOid := v.Tx.MustInputAt(stemInputIdx)
	stemTxID := stemInputOid.TransactionID()
	stemVid := AttachTxID(stemTxID, a,
		WithInvokedBy(a.name),
		WithAttachmentDepth(vidUnwrapped.GetAttachmentDepthNoLock()+1),
	)

	// here it is referenced from the attacher
	if !a.pastCone.MarkVertexUndefined(stemVid) {
		// failed to reference (pruned), but it is ok (rare event)
		return true
	}
	a.Assertf(stemVid.IsBranchTransaction(), "stemVid.IsBranchTransaction()")
	switch stemVid.GetTxStatus() {
	case vertex.Good:
		// it is 'good' and referenced branch -> make it baseline
		v.BaselineBranch = stemVid
		return true

	case vertex.Bad:
		err := stemVid.GetError()
		a.Assertf(err != nil, "err!=nil")
		a.setError(err)
		return false

	case vertex.Undefined:
		return a.pullIfNeeded(stemVid)
	}
	panic("wrong vertex state")
}

const TraceTagSolidifySequencerBaseline = "seqBase"

func (a *attacher) solidifySequencerBaseline(v *vertex.Vertex, vidUnwrapped *vertex.WrappedTx) (ok bool) {
	a.Tracef(TraceTagSolidifySequencerBaseline, "IN for %s", v.Tx.IDShortString)
	defer a.Tracef(TraceTagSolidifySequencerBaseline, "OUT for %s", v.Tx.IDShortString)

	// regular sequencer tx. Go to the direction of the baseline branch
	predOid, _ := v.Tx.SequencerChainPredecessor()
	a.Assertf(predOid != nil, "inconsistency: sequencer milestone cannot be a chain origin")

	var baselineDirection *vertex.WrappedTx

	// follow the endorsement if it is cross-slot or predecessor is not sequencer tx
	followTheEndorsement := predOid.Slot() != v.Tx.Slot() || !predOid.IsSequencerTransaction()
	if followTheEndorsement {
		// predecessor is on the earlier slot -> follow the first endorsement (guaranteed by the ledger constraint layer)
		a.Assertf(v.Tx.NumEndorsements() > 0, "v.Tx.NumEndorsements()>0")
		baselineDirection = AttachTxID(v.Tx.EndorsementAt(0), a,
			WithInvokedBy(a.name),
			WithAttachmentDepth(vidUnwrapped.GetAttachmentDepthNoLock()+1),
		)
		a.Tracef(TraceTagSolidifySequencerBaseline, "follow the endorsement %s", baselineDirection.IDShortString)
	} else {
		baselineDirection = AttachTxID(predOid.TransactionID(), a,
			WithInvokedBy(a.name),
			WithAttachmentDepth(vidUnwrapped.GetAttachmentDepthNoLock()+1),
		)
		a.Tracef(TraceTagSolidifySequencerBaseline, "follow the predecessor %s", baselineDirection.IDShortString)
	}
	// here we reference baseline direction
	if !a.pastCone.MarkVertexUndefined(baselineDirection) {
		// wasn't able to reference baseline direction (pruned) but it is ok
		return true
	}

	switch baselineDirection.GetTxStatus() {
	case vertex.Good:
		a.Tracef(TraceTagSolidifySequencerBaseline, "baselineDirection %s is GOOD", baselineDirection.IDShortString)

		baseline := baselineDirection.BaselineBranch()
		// 'good' and referenced baseline direction must have not-nil baseline
		a.Assertf(baseline != nil, "baseline != nil\n%s", func() string { return baselineDirection.Lines("    ").String() })
		a.Assertf(baseline.IsBranchTransaction(), "baseline.IsBranchTransaction()")

		v.BaselineBranch = baseline
		return true

	case vertex.Bad:
		a.Tracef(TraceTagSolidifySequencerBaseline, "baselineDirection %s is BAD", baselineDirection.IDShortString)

		err := baselineDirection.GetError()
		a.Assertf(err != nil, "err!=nil")
		a.setError(err)
		return false

	case vertex.Undefined:
		a.Tracef(TraceTagSolidifySequencerBaseline, "baselineDirection %s is UNDEF -> pullIfNeeded", baselineDirection.IDShortString)

		return a.pullIfNeeded(baselineDirection)
	}
	panic("wrong vertex state")
}

func (a *attacher) attachVertexNonBranch(vid *vertex.WrappedTx) (ok, defined bool) {
	a.Assertf(!vid.IsBranchTransaction(), "!vid.IsBranchTransaction(): %s", vid.IDShortString)

	if a.pastCone.IsKnownDefined(vid) {
		return true, true
	}
	a.pastCone.MarkVertexUndefined(vid)

	var deterministicPastCone *vertex.PastConeBase

	vid.Unwrap(vertex.UnwrapOptions{
		Vertex: func(v *vertex.Vertex) {
			switch vid.GetTxStatusNoLock() {
			case vertex.Undefined:
				if vid.IsSequencerMilestone() {
					// don't go deeper for undefined sequencers
					ok = true
					return
				}
				// non-sequencer transaction
				ok = a.attachVertexUnwrapped(v, vid)
				if ok && vid.FlagsUpNoLock(vertex.FlagVertexConstraintsValid) && a.pastCone.Flags(vid).FlagsUp(vertex.FlagAttachedVertexInputsSolid|vertex.FlagAttachedVertexEndorsementsSolid) {
					a.pastCone.MarkVertexDefinedDoNotEnforceRootedCheck(vid)
					defined = true
				}
			case vertex.Good:
				a.Assertf(vid.IsSequencerMilestone(), "vid.IsSequencerMilestone()")

				// here cut the recursion and merge 'good' past cone

				deterministicPastCone = vid.GetPastConeNoLock()
				a.Assertf(deterministicPastCone != nil, "deterministicPastCone!=nil")

				//ok = a.attachVertexUnwrapped(v, vid)
				//if ok {
				//	if a.pastCone.Flags(vid).FlagsUp(vertex.FlagAttachedVertexInputsSolid | vertex.FlagAttachedVertexEndorsementsSolid) {
				//		a.pastCone.MarkVertexDefinedDoNotEnforceRootedCheck(vid)
				//		defined = true
				//	}
				//}
			case vertex.Bad:
				a.setError(vid.GetErrorNoLock())

			default:
				a.Log().Fatalf("inconsistency: wrong tx status")
			}
		},
		VirtualTx: func(v *vertex.VirtualTransaction) {
			ok = a.pullIfNeededUnwrapped(v, vid)
		},
	})
	if deterministicPastCone != nil {
		conflict, coverageDelta := a.pastCone.AppendPastCone(deterministicPastCone, func(branch *vertex.WrappedTx) global.IndexedStateReader {
			return a.GetStateReaderForTheBranch(&branch.ID)
		})
		if conflict != nil {
			a.setError(fmt.Errorf("past cones conflicting due to %s", conflict.DecodeID().StringShort()))
			return false, false
		}
		a.accumulatedCoverage += coverageDelta
		ok = true
		defined = true
	}

	if ok && !defined {
		a.pokeMe(vid)
	}
	a.Assertf(ok || a.err != nil, "ok || a.err != nil")
	return
}

// attachVertexUnwrapped: vid corresponds to the vertex v
// it solidifies vertex by traversing the past cone down to Rooted outputs or undefined Vertices
// Repetitive calling of the function reaches all past Vertices down to the Rooted outputs
// The exit condition of the loop is fully determined states of the past cone.
// It results in all Vertices are vertex.Good
// Otherwise, repetition reaches conflict (double spend) or vertex.Bad vertex and exits
// Returns OK (= not bad)
func (a *attacher) attachVertexUnwrapped(v *vertex.Vertex, vidUnwrapped *vertex.WrappedTx) (ok bool) {
	a.Assertf(!v.Tx.IsSequencerMilestone() || a.baseline != nil, "!v.Tx.IsSequencerMilestone() || a.baseline != nil in %s", v.Tx.IDShortString)

	if vidUnwrapped.GetTxStatusNoLock() == vertex.Bad {
		a.setError(vidUnwrapped.GetErrorNoLock())
		a.Assertf(a.err != nil, "a.err != nil")
		return false
	}

	a.Tracef(TraceTagAttachVertex, " %s IN: %s", a.name, vidUnwrapped.IDShortString)
	a.Assertf(!util.IsNil(a.baselineStateReader), "!util.IsNil(a.baselineStateReader)")

	if !a.pastCone.Flags(vidUnwrapped).FlagsUp(vertex.FlagAttachedVertexEndorsementsSolid) {
		a.Tracef(TraceTagAttachVertex, "endorsements not all solidified in %s -> attachEndorsements", v.Tx.IDShortString)
		// depth-first along endorsements
		if !a.attachEndorsements(v, vidUnwrapped) { // <<< recursive
			// not ok -> leave attacher
			a.Assertf(a.err != nil, "a.err != nil")
			return false
		}
	}
	// check consistency
	if a.pastCone.Flags(vidUnwrapped).FlagsUp(vertex.FlagAttachedVertexEndorsementsSolid) {
		err := a.allEndorsementsDefined(v)
		a.Assertf(err == nil, "%w:\nVertices: %s", err, func() string { return a.pastCone.Lines("       ").String() })

		a.Tracef(TraceTagAttachVertex, "endorsements are all solid in %s", v.Tx.IDShortString)
	} else {
		a.Tracef(TraceTagAttachVertex, "endorsements NOT marked solid in %s", v.Tx.IDShortString)
	}

	inputsOk := a.attachInputsOfTheVertex(v, vidUnwrapped) // deep recursion
	if !inputsOk {
		a.Assertf(a.err != nil, "a.err!=nil")
		return false
	}

	if !v.Tx.IsSequencerMilestone() && a.pastCone.Flags(vidUnwrapped).FlagsUp(vertex.FlagAttachedVertexInputsSolid) {
		if !a.finalTouchNonSequencer(v, vidUnwrapped) {
			a.Assertf(a.err != nil, "a.err!=nil")
			return false
		}
	}
	a.Tracef(TraceTagAttachVertex, "return OK: %s", v.Tx.IDShortString)
	return true
}

func (a *attacher) finalTouchNonSequencer(v *vertex.Vertex, vid *vertex.WrappedTx) (ok bool) {
	a.Assertf(!vid.IsSequencerMilestone(), "non-sequencer tx expected, got %s", vid.IDShortString)

	glbFlags := vid.FlagsNoLock()
	if !glbFlags.FlagsUp(vertex.FlagVertexConstraintsValid) {
		// in either case, for non-sequencer transaction validation makes attachment
		// finished and transaction ready to be pruned from the memDAG
		vid.SetFlagsUpNoLock(vertex.FlagVertexTxAttachmentFinished)

		// constraints are not validated yet
		if err := v.ValidateConstraints(); err != nil {
			v.UnReferenceDependencies()
			a.setError(err)
			a.Tracef(TraceTagAttachVertex, "constraint validation failed in %s: '%v'", vid.IDShortString(), err)
			return false
		}
		// mark transaction validated
		vid.SetFlagsUpNoLock(vertex.FlagVertexConstraintsValid)

		a.Tracef(TraceTagAttachVertex, "constraints has been validated OK: %s", v.Tx.IDShortString)
		a.PokeAllWith(vid)
	}
	glbFlags = vid.FlagsNoLock()
	a.Assertf(glbFlags.FlagsUp(vertex.FlagVertexConstraintsValid), "glbFlags.FlagsUp(vertex.FlagConstraintsValid)")

	// non-sequencer, all inputs solid, constraints valid -> we can mark it 'defined' in the attacher
	a.pastCone.MarkVertexDefinedDoNotEnforceRootedCheck(vid)
	return true
}

const TraceTagAttachEndorsements = "attachEndorsements"

// Attaches endorsements of the vertex
// Return OK (== not bad)
func (a *attacher) attachEndorsements(v *vertex.Vertex, vid *vertex.WrappedTx) bool {
	a.Tracef(TraceTagAttachEndorsements, "attachEndorsements IN: of %s, num endorsements %d", vid.IDShortString, v.Tx.NumEndorsements)
	defer a.Tracef(TraceTagAttachEndorsements, "attachEndorsements OUT: of %s, num endorsements %d", vid.IDShortString, v.Tx.NumEndorsements)

	a.Assertf(!a.pastCone.Flags(vid).FlagsUp(vertex.FlagAttachedVertexEndorsementsSolid), "!v.FlagsUp(vertex.FlagAttachedvertexEndorsementsSolid)")

	numUndefined := len(v.Endorsements)
	for i := range v.Endorsements {
		ok, success := a.attachEndorsement(v, vid, byte(i))
		if !ok {
			a.Assertf(a.err != nil, "a.err!=nil")
			return false
		}
		if success {
			numUndefined--
		}
		a.Tracef(TraceTagAttachEndorsements, "attachEndorsement(%s) returned ok=%v, defined=%v",
			util.Ref(v.Tx.EndorsementAt(byte(i))).StringShort(), ok, success)
	}

	if numUndefined == 0 {
		a.AssertNoError(a.allEndorsementsDefined(v))
		a.pastCone.SetFlagsUp(vid, vertex.FlagAttachedVertexEndorsementsSolid)
		a.Tracef(TraceTagAttachEndorsements, "attachEndorsements(%s): endorsements are all good in %s", a.name, v.Tx.IDShortString)
	} else {
		a.Tracef(TraceTagAttachEndorsements, "attachEndorsements(%s): endorsements are NOT all good in %s", a.name, v.Tx.IDShortString)
	}
	return true
}

func (a *attacher) attachEndorsement(v *vertex.Vertex, vidUnwrapped *vertex.WrappedTx, index byte) (ok, defined bool) {
	vidEndorsed := v.Endorsements[index]

	if vidEndorsed == nil {
		vidEndorsed = AttachTxID(v.Tx.EndorsementAt(index), a,
			WithInvokedBy(a.name),
			WithAttachmentDepth(vidUnwrapped.GetAttachmentDepthNoLock()+1),
		)
		if !v.ReferenceEndorsement(index, vidEndorsed) {
			// if failed to reference, remains nil
			a.Tracef(TraceTagAttachEndorsements, "attachEndorsement: attaching endorsement %s of %s: failed to reference", vidEndorsed.IDShortString, vidUnwrapped.IDShortString)
			return true, false
		}
	}
	a.Assertf(vidEndorsed != nil, "vidEndorsed != nil")
	a.Tracef(TraceTagAttachEndorsements, "attachEndorsement: attaching endorsement %s of %s", vidEndorsed.IDShortString, vidUnwrapped.IDShortString)

	if a.pastCone.IsKnownDefined(vidEndorsed) {
		a.Tracef(TraceTagAttachEndorsements, "attachEndorsement: attaching endorsement %s of %s: is already known 'defined'",
			vidEndorsed.IDShortString, vidUnwrapped.IDShortString)
		return true, true
	}

	if vidEndorsed.GetTxStatus() == vertex.Bad {
		a.setError(vidEndorsed.GetError())
		a.Tracef(TraceTagAttachEndorsements, "attachEndorsement: attaching endorsement %s of %s: its is BAD",
			vidEndorsed.IDShortString, vidUnwrapped.IDShortString)
		return false, false
	}

	a.pastCone.MarkVertexUndefined(vidEndorsed)

	a.checkTransactionInTheState(vidEndorsed)

	if a.pastCone.IsKnownInTheState(vidEndorsed) {
		// definitely in the state -> fully defined
		a.pastCone.MarkVertexDefined(vidEndorsed)
		return true, true
	}

	if !a.pullIfNeeded(vidEndorsed) {
		return false, false
	}

	baselineBranch := vidEndorsed.BaselineBranch()
	if baselineBranch == nil {
		a.Tracef(TraceTagAttachEndorsements, "attachEndorsement: attaching endorsement %s of %s: baseline of the endorsement is nil",
			vidEndorsed.IDShortString, vidUnwrapped.IDShortString)
		return true, false
	}

	// baseline of the endorsement must be compatible with baseline of the attacher
	if !a.branchesCompatible(&a.baseline.ID, &baselineBranch.ID) {
		a.setError(fmt.Errorf("attachEndorsements: baseline %s of endorsement %s is incompatible with the baseline branch %s",
			baselineBranch.IDShortString(), vidEndorsed.IDShortString(), a.baseline.IDShortString()))
		a.Tracef(TraceTagAttachEndorsements, "attachEndorsements(%s): not compatible with baselines", a.name)
		return false, false
	}

	if vidEndorsed.IsBranchTransaction() {
		// consistently endorsing branch makes it defined
		a.Assertf(a.baseline == vidEndorsed, "a.baseline == vidEndorsed")
		return true, true
	}

	a.Assertf(!vidEndorsed.IsBranchTransaction(), "attachEndorsements: !vidEndorsed.IsBranchTransaction(): %s", vidEndorsed.IDShortString)

	ok, defined = a.attachVertexNonBranch(vidEndorsed)
	if !ok {
		a.Tracef(TraceTagAttachEndorsements, "attachEndorsements(%s): attachVertexNonBranch returned: endorsement %s -> %s NOT OK",
			a.name, vidUnwrapped.IDShortString, vidEndorsed.IDShortString)
		a.Assertf(a.err != nil, "a.err!=nil")
		return false, false
	}
	a.AssertNoError(a.err)
	return true, defined
}

// checkTransactionInTheState checks if dependency is rooted and marks it 'rooted' if defined
func (a *attacher) checkTransactionInTheState(vid *vertex.WrappedTx) {
	if a.pastCone.Flags(vid).FlagsUp(vertex.FlagAttachedVertexCheckedInTheState) {
		// already checked
		return
	}
	if a.baseline == nil {
		return
	}
	if vid.Timestamp().After(a.baseline.Timestamp()) {
		// output is later than baseline -> can't be Rooted in it
		a.pastCone.MustMarkVertexNotInTheState(vid)
		return
	}
	if a.baselineStateReader().KnowsCommittedTransaction(&vid.ID) {
		a.pastCone.MustMarkVertexInTheState(vid)
	} else {
		a.pastCone.MustMarkVertexNotInTheState(vid)
	}
}

func (a *attacher) attachInputsOfTheVertex(v *vertex.Vertex, vidUnwrapped *vertex.WrappedTx) (ok bool) {
	numUndefined := v.Tx.NumInputs()
	var success bool
	for i := range v.Inputs {
		ok, success = a.attachInput(v, byte(i), vidUnwrapped)
		if !ok {
			a.Assertf(a.err != nil, "a.err != nil")
			return false
		}
		if success {
			numUndefined--
		}
	}
	if numUndefined == 0 {
		a.pastCone.SetFlagsUp(vidUnwrapped, vertex.FlagAttachedVertexInputsSolid)
	}
	return true
}

func (a *attacher) attachInput(v *vertex.Vertex, inputIdx byte, vidUnwrapped *vertex.WrappedTx) (ok, defined bool) {
	vidInputTx, ok := a.attachInputID(v, vidUnwrapped, inputIdx)
	if !ok {
		a.Tracef(TraceTagAttachVertex, "bad input %d", inputIdx)
		return false, false
	}
	// only will become solid if successfully referencedSet
	if v.Inputs[inputIdx] == nil {
		if refOk := v.ReferenceInput(inputIdx, vidInputTx); !refOk {
			return true, false
		}
	}
	a.Assertf(v.Inputs[inputIdx] != nil, "v.Inputs[i] != nil")

	wOut := vertex.WrappedOutput{
		VID:   v.Inputs[inputIdx],
		Index: v.Tx.MustOutputIndexOfTheInput(inputIdx),
	}

	ok, defined = a.attachOutput(wOut)
	if !ok {
		return false, false
	}
	if defined {
		a.Assertf(a.pastCone.Flags(wOut.VID).FlagsUp(vertex.FlagAttachedVertexDefined|vertex.FlagAttachedVertexCheckedInTheState), "must be checked 'rooted' status")
		a.Tracef(TraceTagAttachVertex, "input #%d (%s) has been solidified", inputIdx, wOut.IDShortString)
	}
	return true, defined
}

func (a *attacher) attachIfRooted(wOut vertex.WrappedOutput) (ok bool, defined bool) {
	a.Tracef(TraceTagAttachOutput, "attachIfRooted %s IN", wOut.IDShortString)
	a.checkTransactionInTheState(wOut.VID)
	if a.pastCone.IsKnownInTheState(wOut.VID) {
		a.Tracef(TraceTagAttachOutput, "attachIfRooted %s Rooted status undefined", wOut.IDShortString)
		return true, false
	}

	if a.pastCone.IsNotInTheState(wOut.VID) {
		// it is definitely not in the state
		return true, true
	}
	if a.pastCone.IsRootedOutput(wOut) {
		// transaction is known in the state and output is consumed in the state
		return true, true
	}

	// transaction is known in the state -> check if output is in the state (i.e. not consumed yet)
	stateReader := a.baselineStateReader()
	out, err := stateReader.GetOutputWithID(wOut.DecodeID())
	if errors.Is(err, multistate.ErrNotFound) {
		// output has not been found in the state -> Bad (already consumed)
		err = fmt.Errorf("output %s is already consumed in the baseline state %s", wOut.IDShortString(), a.baseline.IDShortString())
		a.setError(err)
		a.Tracef(TraceTagAttachOutput, "%v", err)
		return false, true
	}
	if err != nil {
		a.setError(err)
		a.Tracef(TraceTagAttachOutput, "%v", err)
		return false, false
	}
	// output has been found in the state -> Good
	if err = wOut.VID.EnsureOutputWithID(out); err != nil {
		a.setError(err)
		a.Tracef(TraceTagAttachOutput, "%v", err)
		return false, true
	}

	// this is new rooted output -> add to the accumulatedCoverage
	a.accumulatedCoverage += out.Output.Amount()
	a.pastCone.MustMarkVertexInTheState(wOut.VID)

	return true, true
}

func (a *attacher) attachOutput(wOut vertex.WrappedOutput) (ok, defined bool) {
	a.Tracef(TraceTagAttachOutput, "IN %s", wOut.IDShortString)

	ok, definedRootedStatus := a.attachIfRooted(wOut)
	if !definedRootedStatus {
		return true, false
	}
	if !ok {
		return false, false
	}
	if a.pastCone.IsRootedOutput(wOut) {
		a.Tracef(TraceTagAttachOutput, "%s is Rooted", wOut.IDShortString)
		return true, true
	}
	// not Rooted
	a.Tracef(TraceTagAttachOutput, "%s is NOT Rooted", wOut.IDShortString)

	if wOut.VID.IsBranchTransaction() {
		// not Rooted branch output -> BAD
		err := fmt.Errorf("attachOutput: branch output %s is expected to be Rooted in the baseline %s", wOut.IDShortString(), a.baseline.IDShortString())
		a.setError(err)
		return false, false
	}

	a.Assertf(!wOut.VID.IsBranchTransaction(), "attachOutput: !wOut.VID.IsBranchTransaction(): %s", wOut.IDShortString)

	// input is not Rooted, attach input transaction
	ok, defined = a.attachVertexNonBranch(wOut.VID)
	if defined {
		o, err := wOut.VID.OutputAt(wOut.Index)
		if err != nil || o == nil {
			a.setError(fmt.Errorf("attachOutput: output %s not available", wOut.IDShortString()))
			return false, false
		}
	}
	return
}

func (a *attacher) branchesCompatible(txid1, txid2 *ledger.TransactionID) bool {
	a.Assertf(txid1 != nil && txid2 != nil, "txid1 != nil && txid2 != nil")
	a.Assertf(txid1.IsBranchTransaction() && txid2.IsBranchTransaction(), "txid1.IsBranchTransaction() && txid2.IsBranchTransaction()")
	switch {
	case *txid1 == *txid2:
		return true
	case txid1.Slot() == txid2.Slot():
		// two different branches on the same slot conflicts
		return false
	case txid1.Slot() < txid2.Slot():
		return multistate.BranchKnowsTransaction(txid2, txid1, func() common.KVReader { return a.StateStore() })
	default:
		return multistate.BranchKnowsTransaction(txid1, txid2, func() common.KVReader { return a.StateStore() })
	}
}

// attachInputID links input vertex with the consumer, detects conflicts in the scope of the attacher
func (a *attacher) attachInputID(consumerVertex *vertex.Vertex, consumerTxUnwrapped *vertex.WrappedTx, inputIdx byte) (vidInputTx *vertex.WrappedTx, ok bool) {
	inputOid := consumerVertex.Tx.MustInputAt(inputIdx)

	vidInputTx = consumerVertex.Inputs[inputIdx]
	if vidInputTx == nil {
		vidInputTx = AttachTxID(inputOid.TransactionID(), a,
			WithInvokedBy(a.name),
			WithAttachmentDepth(consumerTxUnwrapped.GetAttachmentDepthNoLock()+1),
		)
	}
	a.Assertf(vidInputTx != nil, "vidInputTx != nil")

	if vidInputTx.GetTxStatus() == vertex.Bad {
		a.setError(vidInputTx.GetError())
		return nil, false
	}

	if vidInputTx.IsSequencerMilestone() {
		// if input is a sequencer milestones, check if baselines are compatible
		if inputBaselineBranch := vidInputTx.BaselineBranch(); inputBaselineBranch != nil {
			if !a.branchesCompatible(&a.baseline.ID, &inputBaselineBranch.ID) {
				err := fmt.Errorf("branches %s and %s not compatible", a.baseline.IDShortString(), inputBaselineBranch.IDShortString())
				a.setError(err)
				return nil, false
			}
		}
	}

	// not pulling, no need for the transaction to check double-spends of its outputs

	// conflict detection. We check if input is not consumed by some known transaction in the attacher scope
	if conflict := vidInputTx.AttachConsumer(inputOid.Index(), consumerTxUnwrapped, a.checkConflictsFunc(consumerVertex, consumerTxUnwrapped)); conflict != nil {
		err := fmt.Errorf("input %s of consumer %s conflicts with another consumer %s in the baseline state %s (double spend)",
			inputOid.StringShort(), consumerTxUnwrapped.IDShortString(), conflict.IDShortString(), a.baseline.IDShortString())
		a.setError(err)
		return nil, false
	}
	return vidInputTx, true
}

// stdCheckConflictsFunc standard conflict checker. For the milestone attacher
// In incremental attacher is replaced with the extended one
func (a *attacher) stdCheckConflictsFunc(consumerTx *vertex.WrappedTx) checkConflictingConsumersFunc {
	return func(existingConsumers set.Set[*vertex.WrappedTx]) (conflict *vertex.WrappedTx) {
		existingConsumers.ForEach(func(potentialConflicts *vertex.WrappedTx) bool {
			if potentialConflicts == consumerTx {
				return true
			}
			if a.pastCone.IsKnown(potentialConflicts) {
				conflict = potentialConflicts
			}
			return conflict == nil
		})
		return
	}
}

func (a *attacher) isKnownConsumed(wOut vertex.WrappedOutput) (isConsumed bool) {
	wOut.VID.ConsumersOf(wOut.Index).ForEach(func(consumer *vertex.WrappedTx) bool {
		isConsumed = a.pastCone.IsKnown(consumer)
		return !isConsumed
	})
	return
}

// setBaseline sets baseline, references it from the attacher, fetches its baseline accumulatedCoverage
// and initializes attacher's accumulatedCoverage according to the currentTS
// For sequencer transaction baseline will be on the same slot, for branch transactions it can be further in the past
func (a *attacher) setBaseline(baselineVID *vertex.WrappedTx, currentTS ledger.Time) bool {
	a.Assertf(baselineVID.IsBranchTransaction(), "setBaseline: baselineVID.IsBranchTransaction()")

	// it may already be referenced but this ensures it is done only once
	if !a.pastCone.ReferenceBaseline(baselineVID) {
		return false
	}

	rr, found := multistate.FetchRootRecord(a.StateStore(), baselineVID.ID)
	a.Assertf(found, "setBaseline: can't fetch root record for %s", baselineVID.IDShortString)

	a.baseline = baselineVID
	a.baselineSupply = rr.Supply
	a.accumulatedCoverage = rr.LedgerCoverage >> (int(currentTS.Slot() - baselineVID.Slot()))

	if currentTS.IsSlotBoundary() {
		a.Assertf(baselineVID.Slot() < currentTS.Slot(), "baselineVID.Slot() < currentTS.Slot()")
	} else {
		a.Assertf(baselineVID.Slot() == currentTS.Slot(), "baselineVID.Slot() == currentTS.Slot()")
		a.accumulatedCoverage >>= 1
	}
	return true
}

const TraceTagAdjustCoverage = "adjustCoverage"

// adjustCoverage for accumulatedCoverage. Adjustment ensures that branch inflation bonus on the chain output
// of the baseline branch is always included into the coverage exactly once.
// Details and motivation see Proxima WP
func (a *attacher) adjustCoverage() {
	// adjustCoverage must be called exactly once
	a.Assertf(!a.coverageAdjusted, "adjustCoverage: already adjusted")
	a.coverageAdjusted = true

	baseSeqOut := a.baseline.SequencerWrappedOutput()
	a.Tracef(TraceTagAdjustCoverage, "base seq out: %s", baseSeqOut.IDShortString)

	if a.pastCone.IsRootedOutput(baseSeqOut) {
		// the baseline branch sequencer output is Rooted -> it is already included -> no need for adjustment
		a.Tracef(TraceTagAdjustCoverage, " is rooted %s", baseSeqOut.IDShortString)
		return
	}
	a.Tracef(TraceTagAdjustCoverage, " is NOT rooted %s", baseSeqOut.IDShortString)

	// sequencer output is not Rooted (branch is just endorsed) -> add its inflation to the accumulatedCoverage
	seqOut := multistate.MustSequencerOutputOfBranch(a.StateStore(), baseSeqOut.VID.ID).Output

	a.coverageAdjustment = seqOut.Inflation(true)
	a.Tracef(TraceTagAdjustCoverage, "coverage adjustment: %s", util.Th(a.coverageAdjustment))
	a.accumulatedCoverage += a.coverageAdjustment
}

// IsCoverageAdjusted for consistency assertions
func (a *attacher) IsCoverageAdjusted() bool {
	return a.coverageAdjusted
}

// dumpLines beware deadlocks
func (a *attacher) dumpLines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	ret.Add("attacher %s", a.name)
	ret.Add("   baseline: %s", a.baseline.IDShortString())
	ret.Add("   accumulatedCoverage: %s", util.Th(a.accumulatedCoverage))
	ret.Add("   baselineSupply: %s", util.Th(a.baselineSupply))
	ret.Add("   Past cone:")
	//ret.Append(a.pastCone.Lines(prefix...))
	return ret
}

func (a *attacher) dumpLinesString(prefix ...string) string {
	return a.dumpLines(prefix...).String()
}

func (a *attacher) allEndorsementsDefined(v *vertex.Vertex) (err error) {
	v.ForEachEndorsement(func(i byte, vidEndorsed *vertex.WrappedTx) bool {
		if !a.pastCone.IsKnownDefined(vidEndorsed) {
			err = fmt.Errorf("attacher %s: endorsement by %s must be 'defined' %s", a.name, v.Tx.IDShortString(), vidEndorsed.String())
		}
		return err == nil
	})
	return
}

func (a *attacher) SetTraceAttacher(name string) {
	a.forceTrace = name
}

func (a *attacher) Tracef(traceLabel string, format string, args ...any) {
	if a.forceTrace != "" {
		lazyArgs := fmt.Sprintf(format, lazyargs.Eval(args...)...)
		a.Log().Infof("LOCAL TRACE(%s//%s) %s", traceLabel, a.forceTrace, lazyArgs)
		return
	}
	a.Environment.Tracef(traceLabel, a.name+format+" ", args...)
}

func (a *attacher) SlotInflation() uint64 {
	return a.slotInflation
}

func (a *attacher) FinalSupply() uint64 {
	return a.baselineSupply + a.slotInflation
}
