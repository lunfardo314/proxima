package attacher

import (
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/core/core_modules/branches"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/util"
)

func (a *milestoneAttacher) wrapUpAttacher() error {
	a.finals.baseline = *a.pastCone.GetBaseline()
	a.finals.numVertices = a.pastCone.NumVertices()

	delta := a.CoverageDelta()
	slotInflation := a.SlotInflation()
	a.finals.CoverageDelta = delta
	a.finals.LedgerCoverage = a.FinalLedgerCoverage(a.vid.Timestamp(), delta)
	a.finals.SlotInflation = slotInflation
	a.finals.Supply = a.BaselineSupply() + slotInflation

	// Cross-check the coverageDelta declared on this milestone's produced
	// sequencer constraint against what the attacher computed from its past
	// cone. Runs for EVERY milestone (branch and non-branch). On mismatch the
	// milestone is rejected (the value comes from the wire / a remote producer).
	if err := a.enforceSeqCoverageDelta(delta); err != nil {
		return err
	}

	if a.vid.IsBranchTransaction() {
		return a.commitBranch()
	}
	return nil
}

// enforceSeqCoverageDelta cross-checks the coverageDelta declared on this
// milestone's produced sequencer output (sequencer constraint, arg 2) against
// the attacher-computed value. We do NOT panic on mismatch: the declared value
// comes from the wire and a malformed remote milestone must not crash the node.
// The vertex is rejected instead (caller marks it Bad). Gated by
// constEnforceCoverageDeltaMonotonicity (off in certain hand-built attacher
// tests that cannot declare the attacher-computed coverage).
//
// coverageDelta is only meaningful relative to the milestone's OWN canonical
// baseline. During snapshot restore + forward-sync, a milestone gets re-attached
// against the snapshot anchor branch, whose slot is >= the milestone's own slot
// (baseline slot > milestone slot for pre-anchor milestones, or == for milestones
// in the anchor's own slot whose canonical baseline is an earlier branch). In
// real-time a milestone matching its canonical baseline yields declared==computed
// and never reaches here; a non-match with baseline slot >= milestone slot is the
// foreign-baseline sync re-attach, so the recomputed value is meaningless and we
// skip the cross-check instead of rejecting — otherwise the sync path wedges
// permanently (the rejected milestone cascades BAD to every branch pulled behind
// it). The skip is silent unless the "sync" log topic is verbose. The
// strict-increase invariant is still enforced on-chain by _enforceCoverageAdvance
// (declared-vs-declared, baseline-agnostic).
func (a *milestoneAttacher) enforceSeqCoverageDelta(delta uint64) error {
	if !ledger.L(a.vid.Slot()).EnforceCoverageDeltaMonotonicity {
		return nil
	}
	var declared uint64
	var ok bool
	a.vid.RUnwrap(vertex.UnwrapOptions{
		Vertex: func(v *vertex.Vertex) {
			seqData := v.SequencerTransactionData()
			if seqData == nil {
				return
			}
			seqO := v.MustProducedOutputAt(seqData.SequencerOutputIndex)
			if sc, idx := seqO.SequencerConstraint(); idx != 0xff {
				declared = sc.CoverageDelta
				ok = true
			}
		},
	})
	if !ok {
		return fmt.Errorf("milestone %s rejected: cannot read coverageDelta from sequencer constraint", a.vid.IDShortString())
	}
	if declared == delta {
		return nil
	}
	// Sync re-attachment against a foreign baseline whose slot is >= the
	// milestone's own (newer-than-self for pre-anchor milestones, or the same slot
	// as the snapshot anchor): the recomputed value is meaningless, so do not
	// reject — skip the cross-check. This is an expected, benign consequence of
	// snapshot-restore + forward-sync, not an anomaly, so it is silent unless the
	// "sync" log topic is configured verbose in node config (logger.topics.sync >= 1).
	if a.finals.baseline.Slot() >= a.vid.Slot() {
		a.WarnTopicf("sync", 1, "coverageDelta cross-check skipped for milestone %s re-attached against newer baseline %s: computed=%s seqConstraint=%s",
			a.vid.IDShortString(), a.finals.baseline.StringShort(), util.Th(delta), util.Th(declared))
		return nil
	}
	a.Log().Errorf(">>>>>>>> **************** VIOLATION OF DETERMINISM ****************** coverageDelta mismatch in milestone %s: computed=%s seqConstraint=%s",
		a.vid.IDShortString(), util.Th(delta), util.Th(declared))
	return fmt.Errorf("milestone %s rejected: coverageDelta mismatch computed=%s seqConstraint=%s",
		a.vid.IDShortString(), util.Th(delta), util.Th(declared))
}

// commitBranch prepares a deferred branch commit. The actual DB write is deferred
// until the branch state is requested via Branches.GetStateReaderForTheBranch().
// Returns an error if the produced stem's declared aggregates disagree with
// what the attacher computed from its past cone (metadata-refactor §6 D1,
// §9.6 — the branch is invalidated rather than crashing the node).
func (a *milestoneAttacher) commitBranch() error {
	a.Assertf(a.vid.IsBranchTransaction(), "a.vid.IsBranchTransaction()")

	// compute mutations from past cone (same as before)
	muts, stats, committedTxs := a.pastCone.Mutations()

	seqID, stemOID := a.vid.MustSequencerIDAndStemID()

	// extract stem and sequencer outputs from the branch transaction (before detach)
	stemOutput, seqOutput := a.extractBranchOutputs(stemOID, seqID)

	// derive previous branch ID from the stem link, and read the on-chain
	// aggregates the produced stem carries (post metadata-refactor).
	stemLock, ok := stemOutput.Output.StemLock()
	util.Assertf(ok, "commitBranch: stem lock not found")
	stemData, ok := stemOutput.Output.StemData()
	util.Assertf(ok, "commitBranch: stem data not found")
	previousBranchID := stemLock.PredecessorOutputID.TransactionID()

	// Cross-check the stem's declared deterministic values against what this
	// attacher computed from its past cone (metadata-refactor §6 D1). Mismatch
	// invalidates the branch — return the error so the runner marks it Bad.
	if err := a.enforceStemValues(stemLock, stemData); err != nil {
		return err
	}

	// Branch health is enforced HERE, in Go, not on the immutable ledger (a health
	// gate baked into the stem constraint can deadlock a restart from an old
	// snapshot once frozen coverage expires). An unhealthy branch is rejected
	// unless health enforcement is suppressed node-wide (suppress_health_enforcement,
	// for a coordinated restart) or it belongs to the bootstrap chain (always
	// exempt, as elsewhere). The `healthy` bool is reused below for the branch-slot
	// evidence so metrics and enforcement always agree.
	//
	// Enforcement applies ONLY to real-time attachment, never to sync re-attachment.
	// During snapshot-restore + forward-sync a branch is re-attached against a
	// foreign baseline (the snapshot anchor) whose slot is >= the branch's own; the
	// attacher-computed coverageDelta is then meaningless (enforceSeqCoverageDelta
	// skips its cross-check for exactly this reason), and the branch is historical,
	// already governed by LRB selection (which respects health at the consensus
	// level). Re-rejecting it here would wedge sync.
	healthy := global.IsHealthyCoverageDelta(a.finals.CoverageDelta, a.finals.Supply, global.FractionHealthyBranch())
	realTimeAttachment := a.finals.baseline.Slot() < a.vid.Slot()
	if realTimeAttachment && !healthy && seqID != base.BoostrapSequencerID && !a.SuppressHealthEnforcement() {
		return fmt.Errorf("branch %s rejected: unhealthy coverage delta %s of total supply %s",
			a.vid.IDShortString(), util.Th(a.finals.CoverageDelta), util.Th(a.finals.Supply))
	}

	// Per-sequencer coverage lower bound is also enforced HERE in Go, not on the
	// ledger: the bound CONSTANT (coverageContributionLowerBound) stays on the
	// ledger, but enforcement is mutable/suppressible because a small-balance
	// sequencer restarting after its frozen coverage expired could be permanently
	// stuck below it (same restart-deadlock class as branch health). The seq
	// coverage is tokenBalance + frozenCoverage[epoch 0] of the branch's own
	// sequencer output (= SeqTxBuilder.CurrentCoverageContribution). Real-time only
	// and bootstrap-exempt, like the health gate; suppressible independently via
	// suppress_coverage_contribution_lower_bound. The UPPER bound remains a ledger constraint
	// (no deadlock risk).
	if realTimeAttachment && seqID != base.BoostrapSequencerID && !a.SuppressCoverageContributionLowerBound() {
		seqCoverage := seqOutput.Output.TokenBalance() + uint64(seqOutput.Output.FrozenCoverage(0))
		lower := a.CoverageContributionLowerBound(a.vid.Slot())
		if seqCoverage < lower {
			return fmt.Errorf("branch %s rejected: sequencer coverage %s below lower bound %s",
				a.vid.IDShortString(), util.Th(seqCoverage), util.Th(lower))
		}
	}

	// build root record params for deferred commit. SlotInflation here is the
	// updateTrie input/output amount invariant only (consumed + slotInflation
	// == produced). It must match the actual mutations the attacher saw — i.e.
	// the milestone attacher's past-cone slot inflation, which can differ from
	// the sequencer-declared stem.SlotInflation if the attacher's past cone
	// has extra vertices (consensus mismatch between Go and stem is a separate
	// concern surfaced in Phase D).
	params := &multistate.RootRecordParams{
		StemOutputID:  stemOID,
		SeqID:         seqID,
		SlotInflation: a.finals.SlotInflation,
	}

	// submit to Branches as a pending (deferred) commit. Aggregates are passed
	// directly so the cached BranchData can answer queries before commit.
	a.Branches().AddPendingBranch(a.vid.ID(), &branches.PendingBranchCommit{
		Mutations:        muts,
		RootRecParams:    params,
		BaselineBranchID: a.finals.baseline,
		PreviousBranchID: previousBranchID,
		TxIDTTLSlots:     a.TxIDStateTTLSlots,
		CommittedTxs:     committedTxs,
		SequencerName:    a.vid.SequencerName(),
		Supply:           stemLock.TotalSupply,
		TotalCoverage:    stemLock.TotalCoverage,
		// coverageDelta lives on the sequencer constraint now; a.finals.CoverageDelta
		// is the attacher-computed value, already cross-checked against it.
		CoverageDelta:    a.finals.CoverageDelta,
		FrozenCoverage:   stemData.FrozenCoverage,
		SlotInflation:    stemLock.SlotInflation,
		NumConfirmedTransactions:  stemData.NumConfirmedTransactions,
		NumSeqTransactions: stemData.NumSeqTransactions,
		NumSeq:             stemData.NumSeq,
		BaselineRoot:     stemData.BaselineRoot,
	}, stemOutput, seqOutput)

	// register branch vertex set for fine-grained pruning (before PastCone is discarded)
	a.RegisterBranchVertices(a.vid.ID(), previousBranchID, a.pastCone.PastConeBase.VertexSet())

	// evidence branch slot eagerly (not deferred) — needed for network progress tracking
	a.EvidenceBranchSlot(a.vid.Slot(), healthy)

	// stats still set locally for logging
	a.finals.MutationStats = stats
	// a.finals.StateRoot is NOT set — it will be computed at deferred commit time

	branchID := a.vid.ID()
	a.LogTx(time.Now(), fmt.Sprintf("included in pending branch %s", branchID.StringShort()), committedTxs...)
	return nil
}

// extractBranchOutputs extracts stem and sequencer outputs from the branch transaction vertex.
// Must be called before the vertex is detached (ConvertToDetached).
func (a *milestoneAttacher) extractBranchOutputs(stemOID base.OutputID, seqID base.ChainID) (stem, seqOut *ledger.OutputWithID) {
	a.vid.RUnwrap(vertex.UnwrapOptions{
		Vertex: func(v *vertex.Vertex) {
			seqData := v.SequencerTransactionData()
			util.Assertf(seqData != nil, "extractBranchOutputs: sequencer data is nil")

			// stem output
			stemO := v.MustProducedOutputAt(stemOID.Index())
			stem = &ledger.OutputWithID{Output: stemO.Clone(), ID: stemOID}

			// sequencer output
			seqIdx := seqData.SequencerOutputIndex
			seqO := v.MustProducedOutputAt(seqIdx)
			seqOID := a.vid.OutputID(seqIdx)
			seqOut = &ledger.OutputWithID{Output: seqO.Clone(), ID: seqOID}
		},
	})
	util.Assertf(stem != nil && seqOut != nil, "extractBranchOutputs: failed to extract outputs from %s", a.vid.IDShortString)
	return
}
