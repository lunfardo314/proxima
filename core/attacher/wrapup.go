package attacher

import (
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
)

func (a *milestoneAttacher) wrapUpAttacher() {
	a.Tracef(TraceTagAttachMilestone, "wrapUpAttacher")

	a.calculateSlotInflation()
	a.checkConsistencyWithMetadata()

	a.finals.baseline = &a.baseline.ID
	a.finals.numVertices = len(a.pastCone.Vertices)

	a.finals.coverage = a.accumulatedCoverage
	//a.Assertf(a.finals.accumulatedCoverage > 0, "final accumulatedCoverage must be positive")
	a.finals.slotInflation = a.slotInflation

	a.Tracef(TraceTagAttachMilestone, "set ledger baselineCoverage in %s to %s",
		a.vid.IDShortString, func() string { return util.Th(a.finals.coverage) })

	a.vid.SetLedgerCoverage(a.finals.coverage)

	if a.vid.IsBranchTransaction() {
		a.commitBranch()
		a.Tracef(TraceTagAttachMilestone, "finalized branch")
	} else {
		a.Tracef(TraceTagAttachMilestone, "finalized sequencer milestone")
	}

	calculatedMetadata := txmetadata.TransactionMetadata{
		LedgerCoverage: util.Ref(a.accumulatedCoverage),
		SlotInflation:  util.Ref(a.finals.slotInflation),
	}
	if a.metadata != nil {
		calculatedMetadata.SourceTypeNonPersistent = a.metadata.SourceTypeNonPersistent
	}
	if a.vid.IsBranchTransaction() {
		calculatedMetadata.StateRoot = a.finals.root
		calculatedMetadata.Supply = util.Ref(a.baselineSupply + a.slotInflation)
	}
	a.Tracef(TraceTagAttachMilestone, "%s: calculated metadata: %s", a.name, calculatedMetadata.String)

	a.vid.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
		// gossip tx if needed
		a.GossipAttachedTransaction(v.Tx, &calculatedMetadata)
	}})
}

func (a *milestoneAttacher) commitBranch() {
	a.Assertf(a.vid.IsBranchTransaction(), "a.vid.IsBranchTransaction()")

	muts := multistate.NewMutations()
	bsName := a.baseline.ID.StringShort

	// generate DEL mutations
	for vid, consumed := range a.pastCone.Rooted {
		for idx := range consumed {
			out := vid.MustOutputWithIDAt(idx)
			muts.InsertDelOutputMutation(out.ID)
			a.finals.numDeletedOutputs++
			a.TraceTx(&vid.ID, "commitBranch in attacher %s: output #%d consumed in the baseline state %s", a.name, idx, bsName)
		}
	}
	// generate ADD TX and ADD OUTPUT mutations
	a.finals.numNewTransactions = uint32(0)
	allVerticesSet := set.NewFromKeys(a.pastCone.Vertices)
	for vid := range a.pastCone.Vertices {
		if a.pastCone.IsKnownRooted(vid) {
			continue
		}
		muts.InsertAddTxMutation(vid.ID, a.vid.Slot(), byte(vid.NumProducedOutputs()-1))
		a.finals.numNewTransactions++

		a.TraceTx(&vid.ID, "commitBranch in attacher %s: added to the baseline state %s", a.name, bsName)
		// ADD OUTPUT mutations only for not consumed outputs
		producedOutputIndices := vid.NotConsumedOutputIndices(allVerticesSet)
		for _, idx := range producedOutputIndices {
			muts.InsertAddOutputMutation(vid.OutputID(idx), vid.MustOutputAt(idx))
			a.finals.numCreatedOutputs++
		}
	}

	seqID, stemOID := a.vid.MustSequencerIDAndStemID()
	upd := multistate.MustNewUpdatable(a.StateStore(), a.baselineStateReader().Root())
	a.finals.supply = a.baselineSupply + a.finals.slotInflation
	coverage := a.vid.GetLedgerCoverage()

	util.Assertf(a.slotInflation == a.finals.slotInflation, "a.slotInflation == a.finals.slotInflation")
	supply := a.FinalSupply()

	upd.MustUpdate(muts, &multistate.RootRecordParams{
		StemOutputID:    stemOID,
		SeqID:           seqID,
		Coverage:        coverage,
		SlotInflation:   a.slotInflation,
		Supply:          supply,
		NumTransactions: a.finals.numNewTransactions,
	})
	a.finals.root = upd.Root()

	a.EvidenceBranchSlot(a.vid.Slot(), global.IsHealthyCoverage(coverage, supply, global.FractionHealthyBranch))

	// check consistency with state root provided with metadata
	a.checkStateRootConsistentWithMetadata()
}
