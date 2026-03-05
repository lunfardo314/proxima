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
	a.Tracef(TraceTagAttachMilestone, "wrapUpAttacher")

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
	muts, stats, committedTxs := a.pastCone.Mutations(a.vid.Slot())

	seqID, stemOID := a.vid.MustSequencerIDAndStemID()

	// extract stem and sequencer outputs from the branch transaction (before detach)
	stemOutput, seqOutput := a.extractBranchOutputs(stemOID, seqID)

	// build root record params for deferred commit
	params := &multistate.RootRecordParams{
		StemOutputID:    stemOID,
		SeqID:           seqID,
		CoverageDelta:   *a.finals.CoverageDelta,
		FrozenCoverage:  *a.finals.FrozenCoverage,
		SlotInflation:   *a.finals.SlotInflation,
		Supply:          *a.finals.Supply,
		NumTransactions: uint32(stats.NumTransactions),
	}

	// submit to Branches as a pending (deferred) commit
	a.Branches().AddPendingBranch(a.vid.ID(), &branches.PendingBranchCommit{
		Mutations:        muts,
		RootRecParams:    params,
		BaselineBranchID: a.finals.baseline,
		TxIDTTLSlots:     a.TxIDStateTTLSlots,
		CommittedTxs:     committedTxs,
		SequencerName:    a.vid.SequencerName(),
	}, stemOutput, seqOutput)

	// evidence branch slot eagerly (not deferred) — needed for network progress tracking
	a.EvidenceBranchSlot(a.vid.Slot(), global.IsHealthyCoverageDelta(*a.finals.CoverageDelta, *a.finals.Supply, global.FractionHealthyBranch))

	// stats still set locally for logging
	a.finals.MutationStats = stats
	// a.finals.StateRoot is NOT set — it will be computed at deferred commit time

	branchID := a.vid.ID()
	a.LogTx(time.Now(), fmt.Sprintf("pending branch %s", branchID.StringShort()), committedTxs...)
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
