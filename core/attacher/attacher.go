package attacher

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lazyargs"
	"github.com/lunfardo314/proxima/util/lines"
)

func newPastConeAttacher(env Environment, tip *vertex.WrappedTx, txTs base.LedgerTime, name string) attacher {
	util.Assertf(txTs != base.LedgerTime{}, "newPastConeAttacher: txTs must be a non-zero value")

	ret := attacher{
		Environment: env,
		Library:     ledger.L(txTs.Slot),
		name:        name,
		pokeMe:      func(_ *vertex.WrappedTx) {},
		pastCone:    vertex.NewPastCone(env, tip, txTs, name),
	}
	// default: use committing state reader (triggers lazy DB commit for pending branches).
	// IncrementalAttacher overrides this with virtual state reader.
	ret.getBaselineStateReader = func(id base.TransactionID) multistate.StateReader {
		return ret.Branches().GetStateReaderForTheBranch(id)
	}
	return ret
}

const (
	TraceTagAttach       = "attach"
	TraceTagAttachVertex = "attachVertex"
)

func (a *attacher) Name() string {
	return a.name
}

func (a *attacher) BaselineSugaredStateReader() multistate.SugaredStateReader {
	branchID := a.pastCone.GetBaseline()
	if branchID == nil {
		return multistate.SugaredStateReader{}
	}
	return multistate.MakeSugared(a.Branches().GetStateReaderForTheBranch(*branchID))
}

func (a *attacher) baselineStateReader() multistate.StateReader {
	branchID := a.pastCone.GetBaseline()
	if branchID == nil {
		return nil
	}
	return a.getBaselineStateReader(*branchID)
}

func (a *attacher) setError(err error) {
	a.err = err
}

const TraceTagSolidifySequencerBaseline = "seqBase"

// solidifyBaselineUnwrapped directs the attachment process down the MemDAG to reach the deterministically known baseline state
// for a sequencer milestone. Existence of it is guaranteed by the ledger constraints
// Success of the baseline solidification is when the function returns true and v.BaselineBranchID != nil
// Special edge case: when the baseline branch is before the snapshot state, it has to be taken into account if
// it can be used as a baseline or not
func (a *attacher) solidifyBaselineUnwrapped(v *vertex.Vertex, vidUnwrapped *vertex.WrappedTx) (ok bool) {
	a.Tracef(TraceTagSolidifySequencerBaseline, "IN for %s", v.IDShortString)
	defer a.Tracef(TraceTagSolidifySequencerBaseline, "OUT for %s", v.IDShortString)

	// determine the baseline
	baselineDirectionID := v.BaselineDirection()
	util.Assertf(baselineDirectionID != base.TransactionID{}, "baselineDirectionID!=base.TransactionID()")

	if a.Branches().SnapshotKnowsTransaction(baselineDirectionID) {
		v.BaselineBranchID = util.Ref(a.Branches().SnapshotBranchID())
		return true
	}

	baselineDirection := AttachTxID(baselineDirectionID, a,
		WithInvokedBy(a.name),
		WithAttachmentDepth(vidUnwrapped.GetAttachmentDepthNoLock()+1),
	)
	a.pastCone.MarkVertexKnown(baselineDirection)

	switch baselineDirection.GetTxStatus() {
	case vertex.Good:
		// in case the baseline is already detached, we provide a reattach function for the branch
		baseline, ok := baselineDirection.BaselineBranch()
		a.Assertf(ok, "baseline is not known for %s. Baseline direction:\n%s",
			a.name, func() string { return baselineDirection.Lines("    ").String() })

		v.BaselineBranchID = util.Ref(baseline)
		a.Tracef(TraceTagSolidifySequencerBaseline, "solidifyBaselineUnwrapped 1 %s. BaselineBranchID: %s", v.IDShortString, v.BaselineBranchID.StringShort)
		return true

	case vertex.Bad:
		a.setError(baselineDirection.GetError())
		a.Tracef(TraceTagSolidifySequencerBaseline, "solidifyBaselineUnwrapped 2 %s %v", v.IDShortString, baselineDirection.GetError)
		return false

	case vertex.Undefined:
		a.Tracef(TraceTagSolidifySequencerBaseline, "solidifyBaselineUnwrapped 3 %s", v.IDShortString)
		return a.pullIfNeeded(baselineDirection, "solidifyBaselineUnwrapped")
	}
	panic("wrong vertex state")
}

// attachVertexNonBranch if vertex undefined, recursively attaches past cone
// Does not check for past cone consistency -> resulting past cone may contain double spends util attacher solidifies all of it
// For non-sequencer vertices that are already validated (solid), uses RUnwrap (read lock) instead
// of Unwrap (write lock) to eliminate write lock contention on overlapping past cones.
func (a *attacher) attachVertexNonBranch(vid *vertex.WrappedTx) (ok bool) {
	a.Assertf(!vid.IsBranchTransaction(), "!vid.IsBranchTransaction(): %s", vid.IDShortString)

	if a.pastCone.IsKnownDefined(vid) {
		return true
	}

	// For already-validated non-sequencer vertices, the vertex state is immutable:
	// no writes happen during traversal, only reads + building the attacher's own pastCone.
	// Use RUnwrap (read lock) to allow concurrent traversal by multiple attachers.
	// FlagVertexConstraintsValid is monotonic (once set, never cleared), so the check is safe.
	if !vid.IsSequencerTransaction() && vid.FlagsUp(vertex.FlagVertexConstraintsValid) {
		return a.attachVertexNonBranchSolid(vid)
	}

	defined := false
	vid.Unwrap(vertex.UnwrapOptions{
		Vertex: func(v *vertex.Vertex) {
			switch vid.GetTxStatusNoLock() {
			case vertex.Undefined:
				if vid.IsSequencerTransaction() {
					// don't go deeper for undefined sequencers
					ok = true
					return
				}
				// non-sequencer transaction
				ok = a.attachVertexUnwrapped(v, vid)
				if ok && vid.FlagsUpNoLock(vertex.FlagVertexConstraintsValid) && a.pastCone.Flags(vid).FlagsUp(vertex.FlagPastConeVertexInputsSolid|vertex.FlagPastConeVertexEndorsementsSolid) {
					a.pastCone.SetFlagsUp(vid, vertex.FlagPastConeVertexDefined)
					defined = true
				}

			case vertex.Good:
				// dependency is GOOD, so merge its (deterministic) past cone into the current attacher.
				// Note that MergePastCone checks the compatibility of baselines and swaps them if necessary,
				// however, does not check for double-spends here.
				// Past cone may be nil for transactions marked GOOD from snapshot state (no attacher ran)
				// or for vertices detached by GC.
				pcb := vid.GetPastConeNoLock()
				if pcb != nil {
					if !a.pastCone.MergePastCone(pcb, a.Branches()) {
						a.setError(fmt.Errorf("conflicting baselines %s and %s", a.pastCone.GetBaseline().StringShort(), vid.IDShortString()))
						return
					}
				} else if vid.IsSequencerTransaction() {
					// past cone is nil (detached or snapshot vertex). For sequencer transactions,
					// still check baseline compatibility to prevent mixing forks in the past cone.
					// Without this check, a detached vertex from a losing fork can pull its
					// fork's branch into the past cone alongside the winning fork's baseline.
					if baseline := a.pastCone.GetBaseline(); baseline != nil {
						if vidBaseline, hasBaseline := vid.BaselineBranch(); hasBaseline {
							if !a.branchesCompatible(baseline, &vidBaseline) {
								a.setError(fmt.Errorf("incompatible baseline for detached vertex %s: attacher baseline %s vs vertex baseline %s",
									vid.IDShortString(), baseline.StringShort(), vidBaseline.StringShort()))
								return
							}
						}
					}
				}
				ok = true
				defined = true

			case vertex.Bad:
				a.setError(vid.GetErrorNoLock())

			default:
				a.Log().Fatalf("inconsistency: wrong tx status")
			}
		},
		DetachedVertex: func(v *vertex.DetachedVertex) {
			// vertex was detached by GC — don't reattach (stale flags/coverage).
			// The attacher will treat this as unresolved and poke/pull.
			a.LogTx(time.Now(), fmt.Sprintf("attacher %s: encountered DetachedVertex (non-branch path), NOT reattaching", a.name), vid.ID())
			ok = true
		},
		VirtualTx: func(_ *vertex.VirtualTransaction) {
			ok = true
		},
	})
	if !ok {
		a.Assertf(a.err != nil, "a.err != nil: %s", vid.IDShortString())
		return
	}

	if defined {
		a.pastCone.SetFlagsUp(vid, vertex.FlagPastConeVertexDefined)
	} else if a.pokeMe != nil {
		a.pokeMe(vid)
	}
	return
}

// attachVertexNonBranchSolid is the fast path for already-validated non-sequencer vertices.
// Uses RUnwrap (read lock) since the vertex state is immutable after validation.
// If the vertex was detached between the flag check and the RUnwrap, falls back to the
// write-lock path via the regular attachVertexNonBranch Unwrap logic.
func (a *attacher) attachVertexNonBranchSolid(vid *vertex.WrappedTx) (ok bool) {
	needFallback := false
	defined := false

	vid.RUnwrap(vertex.UnwrapOptions{
		Vertex: func(v *vertex.Vertex) {
			ok = a.attachVertexUnwrapped(v, vid)
			if ok && vid.FlagsUpNoLock(vertex.FlagVertexConstraintsValid) && a.pastCone.Flags(vid).FlagsUp(vertex.FlagPastConeVertexInputsSolid|vertex.FlagPastConeVertexEndorsementsSolid) {
				defined = true
			}
		},
		DetachedVertex: func(v *vertex.DetachedVertex) {
			// vertex was detached after our solid check — fall back to write-lock path
			needFallback = true
		},
		VirtualTx: func(_ *vertex.VirtualTransaction) {
			// shouldn't happen for a validated vertex, but handle gracefully
			needFallback = true
		},
	})

	if needFallback {
		// vertex was detached or virtual after our solid check — log and return ok=true
		// so the attacher doesn't treat this as a fatal error.
		a.LogTx(time.Now(), fmt.Sprintf("attacher %s: encountered DetachedVertex (solid path), NOT reattaching", a.name), vid.ID())
		if a.pokeMe != nil {
			a.pokeMe(vid)
		}
		ok = true
		return
	}

	if !ok {
		a.Assertf(a.err != nil, "a.err != nil: %s", vid.IDShortString())
		return
	}

	if defined {
		a.pastCone.SetFlagsUp(vid, vertex.FlagPastConeVertexDefined)
	} else if a.pokeMe != nil {
		a.pokeMe(vid)
	}
	return
}

// attachVertexUnwrapped: vid corresponds to the vertex v
// it solidifies vertex by traversing the past cone down to rooted outputs or undefined Vertices
// Repetitive calling of the function reaches all past vertices down to the rooted outputs
// The exit condition of the loop: fully determined states of the vertices in the past cone.
// It results in all vertices are vertex.Good
// Otherwise, repetition reaches vertex.Bad vertex and exits
// Returns OK (== not bad)
//
// Solidification attack prevention:
//   - Parameter 'depth' is incremented with every call to 'attachVertexUnwrapped'
//   - Upon reaching constant limit, function returns failed transaction duo to recursions depth.
//     This trick prevents unbounded chains of non-sequencer transactions in the past cone: an attack vector
//   - this is deterministic, i.e. same on all nodes
func (a *attacher) attachVertexUnwrapped(v *vertex.Vertex, vidUnwrapped *vertex.WrappedTx) (ok bool) {
	a.Assertf(!v.IsSequencerTransaction() || a.pastCone.GetBaseline() != nil, "!v.Tx.IsSequencerTransaction() || a.baseline != nil in %s", v.IDShortString)

	if vidUnwrapped.GetTxStatusNoLock() == vertex.Bad {
		a.setError(vidUnwrapped.GetErrorNoLock())
		a.Assertf(a.err != nil, "a.err != nil")
		return false
	}

	a.Tracef(TraceTagAttachVertex, " %s IN: %s", a.name, vidUnwrapped.IDShortString)
	a.Assertf(!util.IsNil(a.BaselineSugaredStateReader), "!util.IsNil(a.BaselineSugaredStateReader)")

	// --  attach endorsements if needed (results in recursion)

	if !a.pastCone.Flags(vidUnwrapped).FlagsUp(vertex.FlagPastConeVertexEndorsementsSolid) {
		a.Tracef(TraceTagAttachVertex, "endorsements not all solidified in %s -> attachEndorsements", v.IDShortString)
		// depth-first along endorsements
		if !a.attachEndorsements(v, vidUnwrapped) { // <<< recursive
			// not ok -> leave attacher
			a.Assertf(a.err != nil, "a.err != nil")
			return false
		}
	}
	// check consistency
	if a.pastCone.Flags(vidUnwrapped).FlagsUp(vertex.FlagPastConeVertexEndorsementsSolid) {
		a.Assertf(a.allEndorsementsDefined(v), "not all endorsements defined:\n%s", func() string { return a.pastCone.Lines("       ").String() })

		a.Tracef(TraceTagAttachVertex, "endorsements are all solid in %s", v.IDShortString)
	} else {
		a.Tracef(TraceTagAttachVertex, "endorsements NOT marked solid in %s", v.IDShortString)
	}

	// --  attach inputs if needed (results in recursion)

	if !a.pastCone.Flags(vidUnwrapped).FlagsUp(vertex.FlagPastConeVertexInputsSolid) {
		a.Tracef(TraceTagAttachVertex, "BEFORE attachInputs(%s)", v.IDShortString)
		if !a.attachInputs(v, vidUnwrapped) {
			a.Assertf(a.err != nil, "a.err!=nil")
			return false
		}
	}

	if a.pastCone.Flags(vidUnwrapped).FlagsUp(vertex.FlagPastConeVertexInputsSolid) {
		a.Tracef(TraceTagAttachVertex, "inputs solid (%s)", v.IDShortString)
		a.Assertf(a.allInputsDefined(v), "a.allInputsDefined(v)")

		if !v.IsSequencerTransaction() {
			if !a.finalTouchNonSequencer(v, vidUnwrapped) {
				a.Assertf(a.err != nil, "a.err!=nil")
				return false
			}
		}
	} else {
		a.Tracef(TraceTagAttachVertex, "attachVertexUnwrapped(%s) not all inputs solid", v.IDShortString)
	}

	a.Tracef(TraceTagAttachVertex, "attachVertexUnwrapped(%s) return OK", v.IDShortString)
	return true
}

// finalTouchNonSequencer finishes validation of non-sequencer transactions
func (a *attacher) finalTouchNonSequencer(v *vertex.Vertex, vid *vertex.WrappedTx) (ok bool) {
	a.Assertf(!vid.IsSequencerTransaction(), "non-sequencer tx expected, got %s", vid.IDShortString)

	glbFlags := vid.FlagsNoLock()
	if !glbFlags.FlagsUp(vertex.FlagVertexConstraintsValid) {
		// in either case, for non-sequencer transaction validation makes attachment
		// finished and transaction ready to be pruned from the memDAG
		vid.SetFlagsUpNoLock(vertex.FlagVertexTxAttachmentFinished)

		//{ // debug
		//	a.Log().Infof(">>>>>>> finalTouchNonSequencer:\n%s", v.Lines("     ").String())
		//}

		// constraints are not validated yet
		if err := a.validateVertex(v); err != nil {
			a.LogTx(time.Now(), fmt.Sprintf("validation failed: '%v'", err), v.ID())

			v.UnReferenceDependencies()
			a.setError(err)
			a.Tracef(TraceTagAttachVertex, "constraint validation failed in %s: '%v'", vid.IDShortString(), err)
			return false
		}
		a.LogTx(time.Now(), "validation OK", v.ID())
		// mark transaction validated
		vid.SetFlagsUpNoLock(vertex.FlagVertexConstraintsValid)

		a.Tracef(TraceTagAttachVertex, "constraints has been validated OK: %s", v.IDShortString)
		a.PokeAllWith(vid)
	}
	glbFlags = vid.FlagsNoLock()
	a.Assertf(glbFlags.FlagsUp(vertex.FlagVertexConstraintsValid), "glbFlags.FlagsUp(vertex.FlagConstraintsValid)")

	// non-sequencer, all inputs solid, constraints valid -> we can mark it 'defined' in the attacher
	a.pastCone.SetFlagsUp(vid, vertex.FlagPastConeVertexDefined)
	return true
}

func (a *attacher) validateVertex(v *vertex.Vertex) (err error) {
	start := time.Now()
	if err = v.ValidateConstraints(); err == nil {
		a.EvidenceTxValidationStats(time.Since(start), v.NumInputs(), v.NumProducedOutputs())
	}
	return
}

// refreshDependencyStatus ensures it is known in the past cone, checks in the state status, pulls if needed
func (a *attacher) refreshDependencyStatus(vidDep *vertex.WrappedTx) (ok bool) {
	if vidDep.GetTxStatus() == vertex.Bad {
		a.setError(vidDep.GetError())
		return false
	}
	a.pastCone.MarkVertexKnown(vidDep)
	a.defineInTheStateStatus(vidDep)

	// Fail-fast budget check: immediately check if attachment cost budget is exceeded
	// This prevents attacks where the attacher traverses a huge past cone before failing
	// Note: for incremental attacher, seqTxCost is 0 and budget check happens in atomicCheck instead
	if !a.checkAttachmentCostBudget() {
		return false
	}

	if !a.pullIfNeeded(vidDep, "refreshDependencyStatus") {
		return false
	}
	return true
}

// checkAttachmentCostBudget checks if the total attachment cost (pastCone + seqTx) exceeds the budget.
// Returns true if within budget, false if exceeded (sets error).
// For incremental attacher (seqTxCost == 0), this always returns true as the budget check
// happens in the atomicCheck callback instead.
func (a *attacher) checkAttachmentCostBudget() bool {
	if a.seqTxCost == 0 {
		// Incremental attacher: budget check happens in atomicCheck callback
		return true
	}
	totalCost := a.pastCone.AttachmentCost() + a.seqTxCost
	// Use AttachmentCostBudget as budget for now (will be replaced with AttachmentCostBudget)
	if totalCost > a.AttachmentCostBudget {
		a.setError(fmt.Errorf("attachment cost budget %d exceeded (pastCone=%d, seqTx=%d)",
			a.AttachmentCostBudget, a.pastCone.AttachmentCost(), a.seqTxCost))
		return false
	}
	return true
}

// defineInTheStateStatus checks if dependency is in the baseline state and marks it correspondingly, if possible.
// For non-sequencer transactions not in the state, it also adds attachment cost tracking.
// Handles TxID TTL expiry: very old transactions whose txID entry has been deleted from the
// trie are still treated as "in the state" if they are older than the TTL relative to the baseline.
func (a *attacher) defineInTheStateStatus(vid *vertex.WrappedTx) {
	a.Assertf(a.pastCone.IsKnown(vid), "a.pastCone.IsKnown(vid): %s", vid.IDShortString)
	a.Assertf(a.pastCone.GetBaseline() != nil, "a.baseline != nil")

	if a.pastCone.Flags(vid).FlagsUp(vertex.FlagPastConeVertexCheckedInTheState) {
		return
	}

	baselineID := *a.pastCone.GetBaseline()
	txid := vid.ID()

	if a.Branches().BranchKnowsTransaction(baselineID, txid) {
		a.pastCone.SetFlagsUp(vid, vertex.FlagPastConeVertexCheckedInTheState|vertex.FlagPastConeVertexInTheState|vertex.FlagPastConeVertexDefined)
	} else if txidMayHaveExpired(baselineID, txid) {
		// The txID entry was deleted from the trie due to TTL expiry, but the transaction
		// is legitimately committed. Treat it as "in the state".
		a.pastCone.SetFlagsUp(vid, vertex.FlagPastConeVertexCheckedInTheState|vertex.FlagPastConeVertexInTheState|vertex.FlagPastConeVertexDefined)
	} else {
		// not in the state, so it is not defined yet
		// use MustMarkVertexNotInTheState to properly track attachment cost for non-sequencer transactions
		a.pastCone.MustMarkVertexNotInTheState(vid)
	}
}

// txidMayHaveExpired returns true if the transaction is old enough relative to the baseline
// branch that its txID entry may have been deleted from the trie due to TTL expiry.
func txidMayHaveExpired(baselineID, txid base.TransactionID) bool {
	txSlot := txid.Slot()
	baselineSlot := baselineID.Slot()
	if txSlot >= baselineSlot {
		return false
	}
	ttl := ledger.L(baselineSlot).TxIDStateTTLSlots
	return baselineSlot-txSlot > ttl
}

func (a *attacher) attachEndorsements(v *vertex.Vertex, vid *vertex.WrappedTx) (ok bool) {
	if a.pastCone.Flags(vid).FlagsUp(vertex.FlagPastConeVertexEndorsementsSolid) {
		return true
	}
	for i := range v.Endorsements {
		if !a.attachEndorsement(v, vid, byte(i)) {
			return false
		}
	}

	if a.allEndorsementsDefined(v) {
		a.pastCone.SetFlagsUp(vid, vertex.FlagPastConeVertexEndorsementsSolid)
	}
	return true
}

func (a *attacher) attachEndorsement(v *vertex.Vertex, vidUnwrapped *vertex.WrappedTx, index byte) bool {
	vidEndorsed := v.Endorsements[index]
	if vidEndorsed == nil {
		vidEndorsed = AttachTxID(v.MustEndorsementAt(index), a,
			WithInvokedBy(a.name),
			WithAttachmentDepth(vidUnwrapped.GetAttachmentDepthNoLock()+1),
		)
		v.ReferenceEndorsement(index, vidEndorsed)
	}
	a.Assertf(vidEndorsed != nil, "vidEndorsed!=nil")

	return a.attachEndorsementDependency(vidEndorsed)
}

func (a *attacher) attachEndorsementDependency(vidEndorsed *vertex.WrappedTx) bool {
	if !a.refreshDependencyStatus(vidEndorsed) {
		return false
	}
	if vidEndorsed.IsBranchTransaction() {
		if vidEndorsed.ID() != *a.pastCone.GetBaseline() {
			a.setError(fmt.Errorf("conflicting branch endorsement %s", vidEndorsed.IDShortString()))
			return false
		}
		a.Assertf(a.pastCone.IsKnownDefined(vidEndorsed), "expected to be 'defined': %s", vidEndorsed.IDShortString)
		return true
	}
	return a.attachVertexNonBranch(vidEndorsed)
}

func (a *attacher) attachInput(v *vertex.Vertex, vidUnwrapped *vertex.WrappedTx, inputIdx byte) bool {
	oid := v.MustInputAt(inputIdx)

	a.Tracef(TraceTagAttachVertex, "attachInput(%s): %s", v.IDShortString, oid.StringShort)

	vidDep := v.Inputs[inputIdx]

	var ok bool
	if vidDep == nil {
		vidDep = AttachTxID(oid.TransactionID(), a,
			WithInvokedBy(a.name),
			WithAttachmentDepth(vidUnwrapped.GetAttachmentDepthNoLock()+1),
		)
		v.ReferenceInput(inputIdx, vidDep)
	}
	a.Assertf(vidDep != nil, "vidDep!=nil")

	if !a.refreshDependencyStatus(vidDep) {
		return false
	}
	vidDep.AddConsumer(oid.Index(), vidUnwrapped)

	wOut := vertex.WrappedOutput{
		VID:   vidDep,
		Index: oid.Index(),
	}
	a.Tracef(TraceTagAttachVertex, "before attachOutput(%s): %s", wOut.IDStringShort, a.pastCone.Flags(vidDep).String())
	ok = a.attachOutput(wOut)
	if !ok {
		return false
	}
	a.Tracef(TraceTagAttachVertex, "after attachOutput(%s): %s", wOut.IDStringShort, a.pastCone.Flags(vidDep).String())
	return true
}

func (a *attacher) attachInputs(v *vertex.Vertex, vidUnwrapped *vertex.WrappedTx) (ok bool) {
	for i := range v.Inputs {
		if !a.attachInput(v, vidUnwrapped, byte(i)) {
			a.Assertf(a.err != nil, "a.err!=nil in %s, idx %d", a.name, i)
			return false
		}
	}
	if a.allInputsDefined(v) {
		a.pastCone.SetFlagsUp(vidUnwrapped, vertex.FlagPastConeVertexInputsSolid)
	}
	return true
}

func (a *attacher) allInputsDefined(v *vertex.Vertex) bool {
	for _, vidInp := range v.Inputs {
		if vidInp == nil {
			return false
		}
		if !a.pastCone.IsKnownDefined(vidInp) {
			return false
		}
	}
	return true
}

// checkOutputInTheState expects the produced UTXO ChainID of the transaction is in the state.
// If it is not, sets an error that UTXO is already consumed
func (a *attacher) checkOutputInTheState(vid *vertex.WrappedTx, inputID base.OutputID) bool {
	a.Assertf(a.pastCone.IsInTheState(vid), "a.pastCone.IsInTheState(wOut.VID)")
	o, err := multistate.GetOutputWithIDFromStateReader(a.baselineStateReader(), inputID)
	if errors.Is(err, multistate.ErrNotFound) {
		a.setError(fmt.Errorf("checkOutputInTheState: output %s is already consumed", inputID.StringShort()))
		return false
	}
	a.AssertNoError(err)
	vid.MustEnsureOutput(o.Output, o.ID.Index())
	return true
}

func (a *attacher) attachOutput(wOut vertex.WrappedOutput) bool {
	if !wOut.ValidID() {
		return false
	}
	a.Assertf(a.pastCone.IsKnown(wOut.VID), "a.pastCone.IsKnown(wOut.VID)")

	if a.pastCone.IsInTheState(wOut.VID) {
		// transaction is marked 'is in the state, aka 'rooted'
		if !a.checkOutputInTheState(wOut.VID, wOut.DecodeID()) {
			// output is not in the state -> is consumed
			return false
		}
	}
	// output is available in the baseline state
	if a.pastCone.Flags(wOut.VID).FlagsUp(vertex.FlagPastConeVertexDefined) {
		return true
	}
	// not marked yet as defined
	if wOut.VID.IsBranchTransaction() {
		// if it is on the branch tx, it must be marked as defined
		a.pastCone.SetFlagsUp(wOut.VID, vertex.FlagPastConeVertexDefined)
		return true
	}
	// not defined, not branch, not in the state or unknown
	return a.attachVertexNonBranch(wOut.VID)
}

func (a *attacher) branchesCompatible(branchID1, branchID2 *base.TransactionID) bool {
	a.Assertf(branchID1 != nil && branchID2 != nil, "branchID1 != nil && branchID2 != nil")
	a.Assertf(branchID1.IsBranchTransaction() && branchID2.IsBranchTransaction(), "branchID1.IsBranchTransaction() && branchID2.IsBranchTransaction()")

	switch {
	case *branchID1 == *branchID2:
		return true
	case branchID1.Slot() == branchID2.Slot():
		// two different branches on the same slot conflicts
		return false
	case branchID1.Slot() < branchID2.Slot():
		return a.Branches().BranchKnowsTransaction(*branchID2, *branchID1)
		//return multistate.BranchKnowsTransaction(*branchID2, *branchID1, func() common.KVReader { return a.StateStore() })
	default:
		return a.Branches().BranchKnowsTransaction(*branchID1, *branchID2)
		//return multistate.BranchKnowsTransaction(*branchID1, *branchID2, func() common.KVReader { return a.StateStore() })
	}
}

// setBaseline sets baseline, references it from the attacher
// For sequencer transaction baseline will be on the same slot, for branch transactions it can be further in the past
func (a *attacher) setBaseline(baselineID *base.TransactionID) {
	a.Tracef(TraceTagSolidifySequencerBaseline, "IN setBaseline(%s)", baselineID.StringShort)
	defer a.Tracef(TraceTagSolidifySequencerBaseline, "OUT setBaseline(%s)", baselineID.StringShort)

	a.Assertf(baselineID.IsBranchTransaction(), "setBaseline: baselineVID.IsBranchTransaction()")
	a.pastCone.SetBaseline(baselineID)
}

// dumpLines beware deadlocks
func (a *attacher) dumpLines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	ret.Add("attacher %s", a.name).
		Add("   baseline: %s", a.pastCone.GetBaseline().StringShort()).
		Add("   Past cone:").
		Append(a.pastCone.Lines(prefix...))
	return ret
}

func (a *attacher) dumpLinesString(prefix ...string) string {
	return a.dumpLines(prefix...).String()
}

func (a *attacher) allEndorsementsDefined(v *vertex.Vertex) bool {
	for _, vid := range v.Endorsements {
		if vid == nil {
			return false
		}
		if !a.pastCone.IsKnownDefined(vid) {
			return false
		}
	}
	return true
}

func (a *attacher) SetTraceAttacher(name string) {
	a.forceTrace = name
}

func (a *attacher) Tracef(traceLabel string, format string, args ...any) {
	if a.forceTrace != "" {
		lazyArgs := fmt.Sprintf(format, lazyargs.Eval(args...)...)
		a.Log().Infof("%s LOCAL TRACE(%s//%s) %s", a.name, traceLabel, a.forceTrace, lazyArgs)
		return
	}
	a.Environment.Tracef(traceLabel, a.name+format+" ", args...)
}

func (a *attacher) BaselineSupply() uint64 {
	return a.Branches().Supply(*a.pastCone.GetBaseline())
}

// FinalLedgerCoverage calculates full ledger coverage for the attacher.
// Timestamp is not always defined in the generic attacher, so it is supplied as an argument
// Timestamp is used to determine slot of the attacher and calculate coverage correctly on slot boundaries
func (a *attacher) FinalLedgerCoverage(ts base.LedgerTime, delta ...uint64) uint64 {
	var baselineLC uint64

	// note that timestamp of the transaction can be before the baseline when baseline is snapshot
	if bl := a.pastCone.GetBaseline(); bl != nil && ts.After(bl.Timestamp()) {
		baselineLC = a.Branches().LedgerCoverage(*bl) >> uint64(ts.Slot-bl.Slot())
		if !ts.IsSlotBoundary() {
			baselineLC >>= 1
		}
	}
	var d uint64
	if len(delta) > 0 {
		d = delta[0]
	} else {
		d, _ = a.CoverageDelta()
	}
	return baselineLC + d
}

// CoverageDelta returns
// - coverage delta (including frozen part)
// - frozen part separately
func (a *attacher) CoverageDelta() (delta uint64, frozen uint64) {
	delta, frozen, _ = a.pastCone.CoverageDeltaRaw(context.Background(), a.getBaselineStateReader)
	delta += a.coverageDeltaAdjustment()
	return
}

func (a *attacher) CoverageDeltaWithContext(ctx context.Context) (delta uint64, frozen uint64, err error) {
	delta, frozen, err = a.pastCone.CoverageDeltaRaw(ctx, a.getBaselineStateReader)
	if err != nil {
		return
	}
	delta += a.coverageDeltaAdjustment()
	return
}

// coverageDeltaAdjustment is equal:
// - zero if the sequencer output of the baseline is consumed
// - inflation of the branch, if the output is not consumed
// This makes the minimum value of the coverage delta equal to the inflation (branch bonus inflation) of the baseline branch
func (a *attacher) coverageDeltaAdjustment() uint64 {
	bl := a.pastCone.GetBaseline()
	a.Assertf(bl != nil, "baseline != nil")
	seqOutID, ok := a.Branches().SequencerOutputID(*bl)
	a.Assertf(ok, "can't find sequencer output for baseline %s", bl.StringShort)

	if wOut := AttachOutputID(seqOutID, a); !a.pastCone.IsConsumed(wOut) {
		return wOut.Output().Inflation()
	}
	return 0
}

func (a *attacher) CheckConflicts(ctx context.Context) (*vertex.WrappedOutput, error) {
	return a.pastCone.CheckConflicts(ctx, a.getBaselineStateReader)
}

// SlotInflation sums all inflation amounts in the past cone structure.
// For the incremental attacher inflation at the tip is not included
func (a *attacher) SlotInflation() uint64 {
	return a.pastCone.SlotInflation()
}
