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
	if declared != delta {
		a.Log().Errorf(">>>>>>>> coverageDelta mismatch in milestone %s: computed=%s seqConstraint=%s",
			a.vid.IDShortString(), util.Th(delta), util.Th(declared))
		return fmt.Errorf("milestone %s rejected: coverageDelta mismatch computed=%s seqConstraint=%s",
			a.vid.IDShortString(), util.Th(delta), util.Th(declared))
	}
	return nil
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
	a.EvidenceBranchSlot(a.vid.Slot(), global.IsHealthyCoverageDelta(a.finals.CoverageDelta, a.finals.Supply, global.FractionHealthyBranch()))

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
