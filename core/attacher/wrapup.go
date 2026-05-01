package attacher

import (
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/core/core_modules/branches"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/util"
)

func (a *milestoneAttacher) wrapUpAttacher() {
	a.finals.baseline = *a.pastCone.GetBaseline()
	a.finals.numVertices = a.pastCone.NumVertices()

	delta, frozen := a.CoverageDelta()
	slotInflation := a.SlotInflation()
	a.finals.TransactionMetadata = txmetadata.TransactionMetadata{
		CoverageDelta:  util.Ref(delta),
		FrozenCoverage: util.Ref(frozen),
		LedgerCoverage: util.Ref(a.FinalLedgerCoverage(a.vid.Timestamp(), delta)),
		SlotInflation:  util.Ref(slotInflation),
		Supply:         util.Ref(a.BaselineSupply() + slotInflation),
	}
	if a.vid.IsBranchTransaction() {
		a.commitBranch()
	}
	a.checkConsistencyWithMetadata()
}

// commitBranch prepares a deferred branch commit. The actual DB write is deferred
// until the branch state is requested via Branches.GetStateReaderForTheBranch().
func (a *milestoneAttacher) commitBranch() {
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
	previousBranchID := stemLock.PredecessorOutputID.TransactionID()

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
		SlotInflation: *a.finals.SlotInflation,
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
		CoverageDelta:    stemLock.CoverageDelta,
		FrozenCoverage:   stemLock.FrozenCoverage,
		SlotInflation:    stemLock.SlotInflation,
		NumTransactions:  stemLock.NumTransactions,
		BaselineRoot:     stemLock.BaselineRoot,
	}, stemOutput, seqOutput)

	// register branch vertex set for fine-grained pruning (before PastCone is discarded)
	a.RegisterBranchVertices(a.vid.ID(), previousBranchID, a.pastCone.PastConeBase.VertexSet())

	// evidence branch slot eagerly (not deferred) — needed for network progress tracking
	a.EvidenceBranchSlot(a.vid.Slot(), global.IsHealthyCoverageDelta(*a.finals.CoverageDelta, *a.finals.Supply, global.FractionHealthyBranch()))

	// stats still set locally for logging
	a.finals.MutationStats = stats
	// a.finals.StateRoot is NOT set — it will be computed at deferred commit time

	branchID := a.vid.ID()
	a.LogTx(time.Now(), fmt.Sprintf("included in pending branch %s", branchID.StringShort()), committedTxs...)
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
