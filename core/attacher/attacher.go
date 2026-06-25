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
	"github.com/lunfardo314/proxima/util/lines"
)

// newPastConeAttacher creates the base attacher. A non-nil baseline (provided via AttachTxID(WithBaseline)
// and carried on the vid) puts it in known-baseline mode: the past cone is rooted at that committed branch
// and the milestone attacher skips baseline solidification.
func newPastConeAttacher(env Environment, tip *vertex.WrappedTx, txTs base.LedgerTime, name string, baseline *base.TransactionID) attacher {
	util.Assertf(txTs != base.LedgerTime{}, "newPastConeAttacher: txTs must be a non-zero value")

	ret := attacher{
		Environment: env,
		Library:     ledger.L(txTs.Slot),
		name:        name,
		pokeMe:      func(_ *vertex.WrappedTx) {},
		pastCone:    vertex.NewPastCone(env, tip, txTs, name),
	}
	if baseline != nil {
		ret.pastCone.SetBaseline(baseline)
	}
	// opt the past cone into runtime diagnostic cross-checks (gated by TraceTagPastConeDiag)
	ret.pastCone.SetDiagBranches(env.Branches())
	// default: use committing state reader (triggers lazy DB commit for pending branches).
	// IncrementalAttacher overrides this with virtual state reader.
	ret.getBaselineStateReader = func(id base.TransactionID) multistate.StateReader {
		return ret.Branches().GetStateReaderForTheBranch(id)
	}
	return ret
}

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

// solidifyBaselineUnwrapped directs the attachment process down the MemDAG to reach the deterministically known baseline state
// for a sequencer milestone. Existence of it is guaranteed by the ledger constraints
// Success of the baseline solidification is when the function returns true and the vid's baselineBranchID is set
// Special edge case: when the baseline branch is before the snapshot state, it has to be taken into account if
// it can be used as a baseline or not
func (a *attacher) solidifyBaselineUnwrapped(v *vertex.Vertex, vidUnwrapped *vertex.WrappedTx) (ok bool) {
	// determine the baseline
	baselineDirectionID := v.BaselineDirection()
	util.Assertf(baselineDirectionID != base.TransactionID{}, "baselineDirectionID!=base.TransactionID()")

	if a.Branches().SnapshotKnowsTransaction(baselineDirectionID) {
		vidUnwrapped.SetBaselineBranchIDNoLock(util.Ref(a.Branches().SnapshotBranchID()))
		return true
	}

	baselineDirection := AttachTxID(baselineDirectionID, a,
		WithInvokedBy(a.name),
		WithAttachmentDepth(childAttachmentDepth(vidUnwrapped.GetAttachmentDepthNoLock(), baselineDirectionID)),
	)
	a.pastCone.MarkVertexKnown(baselineDirection)

	switch baselineDirection.GetTxStatus() {
	case vertex.Good:
		// in case the baseline is already detached, we provide a reattach function for the branch
		baseline, ok := baselineDirection.BaselineBranch()
		a.Assertf(ok, "baseline is not known for %s. Baseline direction:\n%s",
			a.name, func() string { return baselineDirection.Lines("    ").String() })

		vidUnwrapped.SetBaselineBranchIDNoLock(util.Ref(baseline))
		a.Tracef(TraceTagSyncDiag, "baseline of %s: dir %s GOOD -> baseline %s",
			vidUnwrapped.IDShortString(), baselineDirectionID.StringShort(), baseline.StringShort())
		return true

	case vertex.Bad:
		a.setError(baselineDirection.GetError())
		return false

	case vertex.Undefined:
		// baseline still undetermined — the attacher waits/pulls baselineDirection. Repeated lines here
		// for the same vid mean the baseline cannot be resolved (the N/A baselines behind the flood).
		a.Tracef(TraceTagSyncDiag, "baseline of %s: dir %s UNDEFINED (depth %d) -> pull/wait",
			vidUnwrapped.IDShortString(), baselineDirectionID.StringShort(), vidUnwrapped.GetAttachmentDepthNoLock())
		return a.pullIfNeeded(baselineDirection)
	}
	panic("wrong vertex state")
}

// depAttachOpts builds AttachTxID options for a dependency reached during past-cone traversal. For a
// non-branch sequencer dependency it adds the attacher's known baseline (WithBaseline), so AttachTxID
// either roots it at the committed branch (Good, no attacher spawned) or starts its attacher in
// known-baseline mode — bounding the recursion instead of re-solidifying each dependency's baseline.
func (a *attacher) depAttachOpts(parentVid *vertex.WrappedTx, depID base.TransactionID) []AttachTxOption {
	opts := []AttachTxOption{
		WithInvokedBy(a.name),
		WithAttachmentDepth(childAttachmentDepth(parentVid.GetAttachmentDepthNoLock(), depID)),
	}
	if bl := a.pastCone.GetBaseline(); bl != nil && depID.IsSequencerTransaction() && !depID.IsBranchTransaction() {
		opts = append(opts, WithBaseline(*bl))
	}
	return opts
}

// attachVertexNonBranch attaches a non-branch vertex by traversing its past cone.
// Uses RUnwrap (read lock) first for all cases. Escalates to Unwrap (write lock) only
// for Undefined non-sequencer vertices that need mutation (referencing deps + validation).
// This eliminates write lock contention on overlapping past cones between concurrent attachers.
func (a *attacher) attachVertexNonBranch(vid *vertex.WrappedTx) (ok bool) {
	a.Assertf(!vid.IsBranchTransaction(), "!vid.IsBranchTransaction(): %s", vid.IDShortString)

	if a.pastCone.IsKnownDefined(vid) {
		return true
	}

	needWriteLock := false
	defined := false

	// Step 1: RUnwrap — read lock first for all cases
	vid.RUnwrap(vertex.UnwrapOptions{
		Vertex: func(v *vertex.Vertex) {
			switch vid.GetTxStatusNoLock() {
			case vertex.Undefined:
				if vid.IsSequencerTransaction() {
					// don't go deeper for undefined sequencers
					ok = true
					return
				}
				// FlagVertexConstraintsValid is monotonic (once set, never cleared).
				// If set, the vertex is immutable — read-only traversal is safe under read lock.
				if vid.FlagsUpNoLock(vertex.FlagVertexConstraintsValid) {
					ok = a.attachVertexUnwrapped(v, vid)
					if ok && a.allInputsDefined(v) && a.allEndorsementsDefined(v) {
						defined = true
					}
				} else {
					// Needs write access for referencing deps + validation
					needWriteLock = true
					ok = true
				}

			case vertex.Good:
				// Only sequencer transactions become Good. Non-seq are either Undefined or Bad.
				// Merge the PastConeBase if available. If nil (snapshot path or GC),
				// handle based on InTheState status.
				pcb := vid.GetPastConeNoLock()
				if pcb != nil {
					if !a.pastCone.MergePastCone(pcb, a.Branches()) {
						a.setError(fmt.Errorf("conflicting baselines %s and %s", a.pastCone.GetBaseline().StringShort(), vid.IDShortString()))
						return
					}
					ok = true
					defined = true
				} else if a.pastCone.IsInTheState(vid) {
					// InTheState with nil PastConeBase: safe — state boundary, subtree is committed
					ok = true
					defined = true
				} else {
					// NOT InTheState, nil PastConeBase (snapshot path or FlagVertexIgnoreAbsenceOfPastCone).
					// The subtree is needed but missing — do NOT mark Defined.
					// Check baseline compatibility, then trigger reattachment or return error.
					if baseline := a.pastCone.GetBaseline(); baseline != nil {
						if vidBaseline, hasBaseline := vid.BaselineBranch(); hasBaseline {
							if !a.branchesCompatible(baseline, &vidBaseline) {
								a.setError(fmt.Errorf("incompatible baseline for vertex %s with nil PastConeBase: attacher baseline %s vs vertex baseline %s",
									vid.IDShortString(), baseline.StringShort(), vidBaseline.StringShort()))
								return
							}
						}
					}
					if a.onDetachedVertex != nil {
						a.Log().Infof("REATTACH (nil PastCone) triggered for %s by attacher %s", vid.IDShortString(), a.name)
						a.onDetachedVertex(vid, v.Transaction)
					} else {
						a.setError(fmt.Errorf("attacher %s: vertex %s has nil PastConeBase and is not InTheState", a.name, vid.IDShortString()))
						return
					}
					ok = true
					// defined remains false — poke will be registered
				}

			case vertex.Bad:
				a.setError(vid.GetErrorNoLock())

			default:
				a.Log().Fatalf("inconsistency: wrong tx status")
			}
		},
		DetachedVertex: func(v *vertex.DetachedVertex) {
			if a.onDetachedVertex != nil {
				a.onDetachedVertex(vid, v.Transaction)
				ok = true // not defined — poke will be registered below
			} else {
				a.setError(fmt.Errorf("attacher %s: detached vertex %s: dependency unavailable", a.name, vid.IDShortString()))
			}
		},
		VirtualTx: func(_ *vertex.VirtualTransaction) {
			ok = true
		},
	})

	if !ok {
		a.Assertf(a.err != nil, "a.err != nil: %s", vid.IDShortString())
		return
	}

	// Step 2: Escalate to write lock only for Undefined non-seq that needs mutation
	if needWriteLock {
		vid.Unwrap(vertex.UnwrapOptions{
			Vertex: func(v *vertex.Vertex) {
				// Re-check: another attacher may have validated between RUnwrap release and Unwrap acquire.
				// FlagVertexConstraintsValid is monotonic, so if true now, vertex is immutable.
				if vid.FlagsUpNoLock(vertex.FlagVertexConstraintsValid) {
					ok = a.attachVertexUnwrapped(v, vid)
					if ok && a.allInputsDefined(v) && a.allEndorsementsDefined(v) {
						defined = true
					}
					return
				}
				// Still Undefined — do the write work (reference deps + validate)
				ok = a.attachVertexUnwrapped(v, vid)
				if ok && vid.FlagsUpNoLock(vertex.FlagVertexConstraintsValid) && a.allInputsDefined(v) && a.allEndorsementsDefined(v) {
					defined = true
				}
			},
			DetachedVertex: func(v *vertex.DetachedVertex) {
				// Race: converted between RUnwrap and Unwrap
				if a.onDetachedVertex != nil {
					a.onDetachedVertex(vid, v.Transaction)
					ok = true
				} else {
					a.setError(fmt.Errorf("attacher %s: detached vertex %s: dependency unavailable", a.name, vid.IDShortString()))
					ok = false
				}
			},
			VirtualTx: func(_ *vertex.VirtualTransaction) {
				ok = true
			},
		})
		if !ok {
			a.Assertf(a.err != nil, "a.err != nil: %s", vid.IDShortString())
			return
		}
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

	a.Assertf(!util.IsNil(a.BaselineSugaredStateReader), "!util.IsNil(a.BaselineSugaredStateReader)")

	// --  attach endorsements if needed (results in recursion)

	if !a.allEndorsementsDefined(v) {
		// depth-first along endorsements
		if !a.attachEndorsements(v, vidUnwrapped) { // <<< recursive
			// not ok -> leave attacher
			a.Assertf(a.err != nil, "a.err != nil")
			return false
		}
	}

	// --  attach inputs if needed (results in recursion)

	if !a.allInputsDefined(v) {
		if !a.attachInputs(v, vidUnwrapped) {
			a.Assertf(a.err != nil, "a.err!=nil")
			return false
		}
	}

	if a.allInputsDefined(v) {
		if !v.IsSequencerTransaction() {
			if !a.finalTouchNonSequencer(v, vidUnwrapped) {
				a.Assertf(a.err != nil, "a.err!=nil")
				return false
			}
		}
	}
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
			return false
		}
		a.LogTx(time.Now(), "validation OK", v.ID())
		// mark transaction validated
		vid.SetFlagsUpNoLock(vertex.FlagVertexConstraintsValid)

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

	// trace tag TraceTagSyncDiag: per dependency visited during past-cone traversal, whether it was
	// recognised as in-state. A committed (below-frontier) dep showing inState=false repeatedly means
	// the in-state check is NOT terminating the recursion — the source of the flood below the frontier.
	a.Tracef(TraceTagSyncDiag, "refreshDep %s inState=%v depth=%d",
		vidDep.IDShortString, a.pastCone.IsInTheState(vidDep), vidDep.GetAttachmentDepthNoLock())

	// Fail-fast budget check: immediately check if attachment cost budget is exceeded
	// This prevents attacks where the attacher traverses a huge past cone before failing
	// Note: for incremental attacher, seqTxCost is 0 and budget check happens in atomicCheck instead
	if !a.checkAttachmentCostBudget() {
		return false
	}

	if !a.pullIfNeeded(vidDep) {
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
//
// A positive "in the state" result is monotonic: if a tx is in baseline B1's state, it is in
// any descendant B2's state. A negative result is NOT monotonic: a tx absent from B1's state
// may be present in descendant B2's state. Therefore, when CheckedInTheState is already set
// (possibly from a PastConeBase merge that used an older baseline), we trust positives but
// re-check negatives against the current baseline.
func (a *attacher) defineInTheStateStatus(vid *vertex.WrappedTx) {
	a.Assertf(a.pastCone.IsKnown(vid), "a.pastCone.IsKnown(vid): %s", vid.IDShortString)
	a.Assertf(a.pastCone.GetBaseline() != nil, "a.baseline != nil")

	flags := a.pastCone.Flags(vid)
	if flags.FlagsUp(vertex.FlagPastConeVertexCheckedInTheState) {
		if flags.FlagsUp(vertex.FlagPastConeVertexInTheState) {
			return // positive is monotonic — always valid for descendant baselines
		}
		// Negative "not in the state" may be stale from a merge with an older baseline.
		// Re-check against the current baseline; only upgrade, never downgrade.
		baselineID := *a.pastCone.GetBaseline()
		txid := vid.ID()
		if a.Branches().BranchKnowsTransaction(baselineID, txid) {
			a.pastCone.UpgradeToInTheState(vid)
		} else if txidMayHaveExpired(baselineID, txid) {
			a.Tracef(vertex.TraceTagPastConeDiag, "TTL bless upgrade: baseline=%s vid=%s (txid record pruned per TxIDStateTTLSlots; treating as in-state without proof)",
				baselineID.StringShort, vid.IDShortString)
			a.pastCone.UpgradeToInTheState(vid)
		}
		return
	}

	baselineID := *a.pastCone.GetBaseline()
	txid := vid.ID()

	if a.Branches().BranchKnowsTransaction(baselineID, txid) {
		a.pastCone.SetFlagsUp(vid, vertex.FlagPastConeVertexCheckedInTheState|vertex.FlagPastConeVertexInTheState|vertex.FlagPastConeVertexDefined)
	} else if txidMayHaveExpired(baselineID, txid) {
		// The txID entry was deleted from the trie due to TTL expiry, but the transaction
		// is legitimately committed. Treat it as "in the state".
		a.Tracef(vertex.TraceTagPastConeDiag, "TTL bless: baseline=%s vid=%s (txid record pruned per TxIDStateTTLSlots; treating as in-state without proof)",
			baselineID.StringShort, vid.IDShortString)
		a.pastCone.SetFlagsUp(vid, vertex.FlagPastConeVertexCheckedInTheState|vertex.FlagPastConeVertexInTheState|vertex.FlagPastConeVertexDefined)
	} else {
		// provisionally not in the state — may be upgraded later by a re-check
		a.pastCone.MarkVertexNotInTheState(vid)
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
	if a.allEndorsementsDefined(v) {
		return true
	}
	for i := range v.Endorsements {
		if !a.attachEndorsement(v, vid, byte(i)) {
			return false
		}
	}
	return true
}

func (a *attacher) attachEndorsement(v *vertex.Vertex, vidUnwrapped *vertex.WrappedTx, index byte) bool {
	vidEndorsed := v.Endorsements[index]
	if vidEndorsed == nil {
		endorsedID := v.MustEndorsementAt(index)
		vidEndorsed = AttachTxID(endorsedID, a, a.depAttachOpts(vidUnwrapped, endorsedID)...)
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

	vidDep := v.Inputs[inputIdx]

	if vidDep == nil {
		inputID := oid.TransactionID()
		vidDep = AttachTxID(inputID, a, a.depAttachOpts(vidUnwrapped, inputID)...)
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
	return a.attachOutput(wOut)
}

func (a *attacher) attachInputs(v *vertex.Vertex, vidUnwrapped *vertex.WrappedTx) (ok bool) {
	if a.allInputsDefined(v) {
		return true
	}
	for i := range v.Inputs {
		if !a.attachInput(v, vidUnwrapped, byte(i)) {
			a.Assertf(a.err != nil, "a.err!=nil in %s, idx %d", a.name, i)
			return false
		}
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
	rdr := a.baselineStateReader()
	if rdr == nil {
		a.setError(fmt.Errorf("checkOutputInTheState: baseline state reader unavailable for %s", inputID.StringShort()))
		return false
	}
	o, err := multistate.GetOutputWithIDFromStateReader(rdr, inputID)
	if errors.Is(err, multistate.ErrNotFound) {
		baselineID := a.pastCone.GetBaseline()
		baselineHex, baselineIsPending, baselineRootHex := "", false, ""
		if baselineID != nil {
			baselineHex = baselineID.StringHex()
			baselineIsPending = a.Branches().IsPending(*baselineID)
			baselineRootHex = a.Branches().GetRootHex(*baselineID)
		}
		a.setError(fmt.Errorf("checkOutputInTheState: output %s is already consumed (baselineHex=%s baselineIsPending=%v baselineRoot=%s)",
			inputID.StringShort(), baselineHex, baselineIsPending, baselineRootHex))
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

func (a *attacher) Tracef(traceLabel string, format string, args ...any) {
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
		d = a.CoverageDelta()
	}
	return baselineLC + d
}

// CoverageDelta returns the coverage delta of the past cone.
// Uses the global node context (a.Ctx()) rather than context.Background() so that
// CoverageDeltaRaw — which reads state via BadgerDB — bails out cleanly on node
// shutdown instead of racing with a closed DB and panicking. Any ctx error is
// intentionally swallowed: during shutdown the result is unused (vertex is abandoned).
func (a *attacher) CoverageDelta() (delta uint64) {
	delta, _ = a.pastCone.CoverageDeltaRaw(a.Ctx(), a.getBaselineStateReader)
	delta += a.coverageDeltaAdjustment()
	return
}

func (a *attacher) CoverageDeltaWithContext(ctx context.Context) (delta uint64, err error) {
	delta, err = a.pastCone.CoverageDeltaRaw(ctx, a.getBaselineStateReader)
	if err != nil {
		return
	}
	delta += a.coverageDeltaAdjustment()
	return
}

// SequencerFrozenCoverageDelta returns the signed change in total frozen-by-
// delegation tokens (across all sequencers) over this past cone's delta. See
// PastCone.SequencerFrozenCoverageDelta.
func (a *attacher) SequencerFrozenCoverageDelta() int64 {
	return a.pastCone.SequencerFrozenCoverageDelta()
}

// BaselineFrozenCoverage returns the total frozen-by-delegation tokens recorded
// on the baseline branch — the value onto which this branch's
// SequencerFrozenCoverageDelta is accumulated.
func (a *attacher) BaselineFrozenCoverage() uint64 {
	return a.Branches().FrozenCoverage(*a.pastCone.GetBaseline())
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

// NumNewTransactionsInPastCone returns the count of new (non-rooted)
// transactions in the past cone — i.e. txs that this branch is committing
// for the first time. For the incremental attacher the tip is not included.
func (a *attacher) NumNewTransactionsInPastCone() int {
	return a.pastCone.NumNewTransactions()
}

// NumNewTransactionStatsInPastCone returns, in a single pass, the new-tx count,
// the new sequencer-tx count, and the distinct-sequencer count of the past
// cone (StemData numTransactions / numSeqTransactions / numSeq). For the
// incremental attacher the tip is not included; the branch builder passes its
// own sequencer ID via includeSeq so the predicted numSeq matches the verifier.
func (a *attacher) NumNewTransactionStatsInPastCone(includeSeq ...base.ChainID) (numTx, numSeqTx, numSeq int) {
	return a.pastCone.NumNewTransactionStats(includeSeq...)
}
