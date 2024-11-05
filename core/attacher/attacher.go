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
	//ret.checkConflictsFunc = func(_ *vertex.Vertex, consumerTx *vertex.WrappedTx) checkConflictingConsumersFunc {
	//	return ret.stdCheckConflictsFunc(consumerTx)
	//}
	return ret
}

const (
	TraceTagAttach       = "attach"
	TraceTagAttachOutput = "attachOutputOld"
	TraceTagAttachVertex = "attachVertexUnwrapped"
)

func (a *attacher) Name() string {
	return a.name
}

func (a *attacher) baselineSugaredStateReader() multistate.SugaredStateReader {
	return multistate.MakeSugared(a.baselineStateReader())
}

func (a *attacher) baselineStateReader() global.IndexedStateReader {
	return a.GetStateReaderForTheBranch(&a.baseline.ID)
}

func (a *attacher) setError(err error) {
	fmt.Printf(">>>>>>>>>>>>>>>>>> set err (%s): %v", a.name, err)
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
	a.Assertf(v.BaselineBranch == nil, "v.BaselineBranch == nil")

	stemInputIdx := v.StemInputIndex()
	stemInputOid := v.Tx.MustInputAt(stemInputIdx)
	stemTxID := stemInputOid.TransactionID()
	stemVid := AttachTxID(stemTxID, a,
		WithInvokedBy(a.name),
		WithAttachmentDepth(vidUnwrapped.GetAttachmentDepthNoLock()+1),
	)

	// here it is referenced from the attacher
	if !a.pastCone.MarkVertexKnown(stemVid) {
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
	if !a.pastCone.MarkVertexKnown(baselineDirection) {
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

func (a *attacher) attachVertexNonBranch(vid *vertex.WrappedTx) (ok bool) {
	a.Assertf(!vid.IsBranchTransaction(), "!vid.IsBranchTransaction(): %s", vid.IDShortString)

	if a.pastCone.IsKnownDefined(vid) {
		return true
	}
	var deterministicPastCone *vertex.PastConeBase

	defined := false
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
				if ok && vid.FlagsUpNoLock(vertex.FlagVertexConstraintsValid) && a.pastCone.Flags(vid).FlagsUp(vertex.FlagPastConeVertexInputsSolid|vertex.FlagPastConeVertexEndorsementsSolid) {
					a.pastCone.MarkVertexDefinedDoNotEnforceRootedCheck(vid)
					defined = true
				}
			case vertex.Good:
				a.Assertf(vid.IsSequencerMilestone(), "vid.IsSequencerMilestone()")
				if !a.branchesCompatible(a.baseline, vid.BaselineBranch()) {
					a.setError(fmt.Errorf("conflicting baseline of %s", vid.IDShortString()))
					return
				}
				ok = true
				// here cut the recursion and merge 'good' past cone
				deterministicPastCone = vid.GetPastConeNoLock()
				a.Assertf(deterministicPastCone != nil, "deterministicPastCone!=nil")
			case vertex.Bad:
				a.setError(vid.GetErrorNoLock())

			default:
				a.Log().Fatalf("inconsistency: wrong tx status")
			}
		},
		VirtualTx: func(v *vertex.VirtualTransaction) {
			ok = true
			//ok = a.pullIfNeededUnwrapped(v, vid)
		},
	})
	if !ok {
		a.Assertf(a.err != nil, "a.err != nil: %s", vid.IDShortString())
		return
	}
	if deterministicPastCone != nil {
		a.pastCone.AppendPastCone(deterministicPastCone, a.baselineStateReader)
		ok = true
		defined = true
	}

	if defined {
		a.pastCone.SetFlagsUp(vid, vertex.FlagPastConeVertexDefined)
	} else {
		a.pokeMe(vid)
	}
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
	a.Assertf(!util.IsNil(a.baselineSugaredStateReader), "!util.IsNil(a.baselineSugaredStateReader)")

	if !a.pastCone.Flags(vidUnwrapped).FlagsUp(vertex.FlagPastConeVertexEndorsementsSolid) {
		a.Tracef(TraceTagAttachVertex, "endorsements not all solidified in %s -> attachEndorsementsOld", v.Tx.IDShortString)
		// depth-first along endorsements
		if !a.attachEndorsements(v, vidUnwrapped) { // <<< recursive
			// not ok -> leave attacher
			a.Assertf(a.err != nil, "a.err != nil")
			return false
		}
	}
	// check consistency
	if a.pastCone.Flags(vidUnwrapped).FlagsUp(vertex.FlagPastConeVertexEndorsementsSolid) {
		err := a.allEndorsementsDefined(v)
		a.Assertf(err == nil, "%w:\nVertices: %s", err, func() string { return a.pastCone.Lines("       ").String() })

		a.Tracef(TraceTagAttachVertex, "endorsements are all solid in %s", v.Tx.IDShortString)
	} else {
		a.Tracef(TraceTagAttachVertex, "endorsements NOT marked solid in %s", v.Tx.IDShortString)
	}
	inputsOk := a.attachInputs(v, vidUnwrapped) // deep recursion
	if !inputsOk {
		a.Assertf(a.err != nil, "a.err!=nil")
		return false
	}

	if !v.Tx.IsSequencerMilestone() && a.pastCone.Flags(vidUnwrapped).FlagsUp(vertex.FlagPastConeVertexInputsSolid) {
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

func (a *attacher) refreshDependencyStatus(vidDep *vertex.WrappedTx) (ok bool) {
	if vidDep.GetTxStatus() == vertex.Bad {
		a.setError(vidDep.GetError())
		return false
	}
	if !a.pastCone.MarkVertexKnown(vidDep) {
		// not referenced but it is ok
		return true
	}
	a.defineInTheStateStatus(vidDep)
	if !a.pullIfNeeded(vidDep) {
		return false
	}
	return true
}

func (a *attacher) referenceDependencyTxID(txid ledger.TransactionID, referencingVid *vertex.WrappedTx) (vidDep *vertex.WrappedTx, ok bool) {
	if referencingVid != nil {
		vidDep = AttachTxID(txid, a,
			WithInvokedBy(a.name),
			WithAttachmentDepth(referencingVid.GetAttachmentDepthNoLock()+1),
		)
	} else {
		vidDep = AttachTxID(txid, a, WithInvokedBy(a.name))
	}
	if a.refreshDependencyStatus(vidDep) {
		return vidDep, true
	}
	return nil, false
}

func (a *attacher) referenceEndorsement(v *vertex.Vertex, vidUnwrapped *vertex.WrappedTx, index byte) (ok bool) {
	vidEndorsed := v.Endorsements[index]

	if vidEndorsed == nil {
		vidEndorsed, ok = a.referenceDependencyTxID(v.Tx.EndorsementAt(index), vidUnwrapped)
		if !ok {
			return false
		}
		if vidEndorsed == nil {
			return true
		}
		if !v.ReferenceEndorsement(index, vidEndorsed) {
			// remains nil but it is ok
			return true
		}
	} else {
		if a.refreshDependencyStatus(vidEndorsed) {
			return false
		}
	}
	return
}

func (a *attacher) attachEndorsements(v *vertex.Vertex, vid *vertex.WrappedTx) (ok bool) {
	if a.pastCone.Flags(vid).FlagsUp(vertex.FlagPastConeVertexEndorsementsSolid) {
		return true
	}

	for i := range v.Endorsements {
		if !a.referenceEndorsement(v, vid, byte(i)) {
			return false
		}
	}

	allDefined := true
	for _, vidEndorsed := range v.Endorsements {
		if vidEndorsed == nil {
			allDefined = false
			continue
		}
		if vidEndorsed.IsBranchTransaction() {
			if vidEndorsed != a.baseline {
				a.setError(fmt.Errorf("conflicting branch endorsement %s", vidEndorsed.IDShortString()))
				return false
			}
			a.Assertf(a.pastCone.IsKnownDefined(vidEndorsed), "expected to be 'defined': %s", vidEndorsed.IDShortString())
			continue
		}
		ok1 := a.attachVertexNonBranch(vidEndorsed)
		if !ok1 {
			return false
		}
		if !a.pastCone.Flags(vidEndorsed).FlagsUp(vertex.FlagPastConeVertexDefined) {
			allDefined = false
		}
	}
	if allDefined {
		a.pastCone.SetFlagsUp(vid, vertex.FlagPastConeVertexEndorsementsSolid)
	}
	return true
}

// defineInTheStateStatus checks if dependency is in the baseline state and marks it correspondingly, if possible
func (a *attacher) defineInTheStateStatus(vid *vertex.WrappedTx) {
	a.Assertf(a.pastCone.IsKnown(vid), "a.pastCone.IsKnown(vid): %s", vid.IDShortString)

	if a.pastCone.Flags(vid).FlagsUp(vertex.FlagPastConeVertexCheckedInTheState) || a.baseline == nil {
		return
	}
	if a.baselineSugaredStateReader().KnowsCommittedTransaction(&vid.ID) {
		// once endorsement is on the baseline, it is fully defined
		a.pastCone.SetFlagsUp(vid, vertex.FlagPastConeVertexCheckedInTheState|vertex.FlagPastConeVertexInTheState|vertex.FlagPastConeVertexDefined)
	} else {
		// not on the state, so it is not defined
		a.pastCone.SetFlagsUp(vid, vertex.FlagPastConeVertexCheckedInTheState)
	}
}

func (a *attacher) attachInputs(v *vertex.Vertex, vidUnwrapped *vertex.WrappedTx) (ok bool) {
	for i := range v.Inputs {
		if !a.attachInput(v, vidUnwrapped, byte(i)) {
			return false
		}
	}
	allDefined := true
	for _, vidDep := range v.Inputs {
		if vidDep == nil {
			allDefined = false
			continue
		}
		isDefined := a.pastCone.Flags(vidDep).FlagsUp(vertex.FlagPastConeVertexDefined)
		if vidDep.IsBranchTransaction() {
			a.Assertf(isDefined, "branch output must be 'defined'")
			continue
		}
		if ok = a.attachVertexNonBranch(vidDep); !ok {
			return
		}
		if !a.pastCone.Flags(vidDep).FlagsUp(vertex.FlagPastConeVertexDefined) {
			allDefined = false
		}
	}
	if allDefined {
		a.pastCone.SetFlagsUp(vidUnwrapped, vertex.FlagPastConeVertexInputsSolid)
	}
	return true
}

// attachInput
func (a *attacher) attachInput(v *vertex.Vertex, vidUnwrapped *vertex.WrappedTx, idx byte) (ok bool) {
	vidDep := v.Inputs[idx]
	oid := v.Tx.MustInputAt(idx)

	if !oid.Valid() {
		a.setError(fmt.Errorf("wrong input %s", oid.String()))
		return false
	}

	if vidDep == nil {
		vidDep, ok = a.referenceDependencyTxID(oid.TransactionID(), vidUnwrapped)
		if !ok {
			return
		}
		if vidDep == nil {
			return true
		}
		if !v.ReferenceInput(idx, vidDep) {
			// remains nil but it is ok
			return true
		}
	} else {
		if !a.refreshDependencyStatus(vidDep) {
			return false
		}
	}
	if a.pastCone.IsInTheState(vidDep) {
		if !a.checkOutputInTheState(vidDep, idx) {
			return false
		}
	}
	vidDep.AddConsumer(oid.Index(), vidUnwrapped)
	return true
}

func (a *attacher) checkOutputInTheState(vid *vertex.WrappedTx, idx byte) bool {
	a.Assertf(a.pastCone.IsInTheState(vid), "a.pastCone.IsInTheState(wOut.VID)")
	oid := vid.OutputID(idx)
	o, err := a.baselineSugaredStateReader().GetOutputWithID(&oid)
	if errors.Is(err, multistate.ErrNotFound) {
		a.setError(fmt.Errorf("output %s is already consumed", oid.StringShort()))
		return false
	}
	a.AssertNoError(err)

	if err = vid.EnsureOutputWithID(o); err != nil {
		a.setError(err)
		return false
	}
	return true
}

func (a *attacher) branchesCompatible(vidBranch1, vidBranch2 *vertex.WrappedTx) bool {
	a.Assertf(vidBranch1.IsBranchTransaction() && vidBranch1.IsBranchTransaction(), "vidBranch1.IsBranchTransaction() && vidBranch1.IsBranchTransaction()")
	switch {
	case vidBranch1 == vidBranch2:
		return true
	case vidBranch1.Slot() == vidBranch2.Slot():
		// two different branches on the same slot conflicts
		return false
	case vidBranch1.Slot() < vidBranch2.Slot():
		return multistate.BranchKnowsTransaction(&vidBranch2.ID, &vidBranch1.ID, func() common.KVReader { return a.StateStore() })
	default:
		return multistate.BranchKnowsTransaction(&vidBranch1.ID, &vidBranch2.ID, func() common.KVReader { return a.StateStore() })
	}
}

// setBaseline sets baseline, references it from the attacher
// For sequencer transaction baseline will be on the same slot, for branch transactions it can be further in the past
func (a *attacher) setBaseline(baselineVID *vertex.WrappedTx, currentTS ledger.Time) bool {
	a.Assertf(baselineVID.IsBranchTransaction(), "setBaseline: baselineVID.IsBranchTransaction()")

	// it may already be referenced but this ensures it is done only once
	if !a.pastCone.SetBaseline(baselineVID) {
		return false
	}

	rr, found := multistate.FetchRootRecord(a.StateStore(), baselineVID.ID)
	a.Assertf(found, "setBaseline: can't fetch root record for %s", baselineVID.IDShortString)

	a.baseline = baselineVID
	a.baselineSupply = rr.Supply

	if currentTS.IsSlotBoundary() {
		a.Assertf(baselineVID.Slot() < currentTS.Slot(), "baselineVID.Slot() < currentTS.Slot()")
	} else {
		a.Assertf(baselineVID.Slot() == currentTS.Slot(), "baselineVID.Slot() == currentTS.Slot()")
	}
	return true
}

// dumpLines beware deadlocks
func (a *attacher) dumpLines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	ret.Add("attacher %s", a.name).
		Add("   baseline: %s", a.baseline.IDShortString()).
		Add("   baselineSupply: %s", util.Th(a.baselineSupply)).
		Add("   Past cone:").
		Append(a.pastCone.Lines(prefix...))
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

func (a *attacher) CoverageDelta(currentTs ledger.Time) (coverage, delta uint64) {
	return a.pastCone.CoverageAndDelta(currentTs)
}

func (a *attacher) LedgerCoverage(currentTs ledger.Time) uint64 {
	return a.pastCone.LedgerCoverage(currentTs)
}

func (a *attacher) PastConeForDebugOnly(env global.Logging, name string) *vertex.PastCone {
	return a.pastCone.CloneForDebugOnly(env, name)
}
