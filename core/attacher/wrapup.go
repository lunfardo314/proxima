package attacher

import (
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
)

func (a *milestoneAttacher) wrapUpAttacher() {
	a.Tracef(TraceTagAttachMilestone, "wrapUpAttacher")

	a.calculateSlotInflation()
	a.checkConsistencyWithMetadata()

	a.finals.baseline = &a.baseline.ID
	a.finals.numTransactions = len(a.vertices)

	a.finals.coverage = a.coverage
	a.Assertf(a.finals.coverage.LatestDelta() > 0, "final coverage must be positive")
	a.finals.slotInflation = a.slotInflation

	a.Tracef(TraceTagAttachMilestone, "set ledger baselineCoverage in %s to %s", a.vid.IDShortString(), a.finals.coverage.String())
	a.vid.SetLedgerCoverage(a.finals.coverage)

	if a.vid.IsBranchTransaction() {
		a.commitBranch()
		a.EvidenceBookedBranch(&a.vid.ID, a.vid.MustSequencerID())
		a.Tracef(TraceTagAttachMilestone, "finalized branch")
	} else {
		a.Tracef(TraceTagAttachMilestone, "finalized sequencer milestone")
	}

	calculatedMetadata := txmetadata.TransactionMetadata{
		LedgerCoverageDelta: util.Ref(a.coverage.LatestDelta()),
		SlotInflation:       util.Ref(a.finals.slotInflation),
	}
	if a.metadata != nil {
		calculatedMetadata.DoNotNeedGossiping = a.metadata.DoNotNeedGossiping
	}
	if a.vid.IsBranchTransaction() {
		calculatedMetadata.StateRoot = a.finals.root
		calculatedMetadata.Supply = util.Ref(a.baselineSupply + a.slotInflation)
	}
	a.Tracef(TraceTagAttachMilestone, "%s: calculated metadata: %s", a.name, calculatedMetadata.String)

	a.vid.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
		// persist transaction bytes, if needed
		if a.metadata == nil || a.metadata.SourceTypeNonPersistent != txmetadata.SourceTypeTxStore {
			flags := a.vid.FlagsNoLock()
			if !flags.FlagsUp(vertex.FlagVertexTxBytesPersisted) {
				a.AsyncPersistTxBytesWithMetadata(v.Tx.Bytes(), &calculatedMetadata)
				a.vid.SetFlagsUpNoLock(vertex.FlagVertexTxBytesPersisted)
			}
		}
		// gossip tx if needed
		a.GossipAttachedTransaction(v.Tx, &calculatedMetadata)
	}})
}

func (a *milestoneAttacher) commitBranch() {
	a.Assertf(a.vid.IsBranchTransaction(), "a.vid.IsBranchTransaction()")

	muts := multistate.NewMutations()
	bsName := a.baseline.ID.StringShort

	// generate DEL mutations
	for vid, consumed := range a.rooted {
		for idx := range consumed {
			out := vid.MustOutputWithIDAt(idx)
			muts.InsertDelOutputMutation(out.ID)
			a.finals.numDeletedOutputs++
			a.TraceTx(&vid.ID, "commitBranch in attacher %s: output #%d consumed in the baseline state %s", a.name, idx, bsName)
		}
	}
	// generate ADD TX and ADD OUTPUT mutations
	allVerticesSet := set.NewFromKeys(a.vertices)
	for vid := range a.vertices {
		if a.isKnownRooted(vid) {
			continue
		}
		muts.InsertAddTxMutation(vid.ID, a.vid.Slot(), byte(vid.NumProducedOutputs()-1))
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
	upd.MustUpdate(muts, &multistate.RootRecordParams{
		StemOutputID:  stemOID,
		SeqID:         seqID,
		Coverage:      *a.vid.GetLedgerCoverage(),
		SlotInflation: a.slotInflation,
		Supply:        a.baselineSupply + a.finals.slotInflation,
	})
	a.finals.root = upd.Root()
	// check consistency with state root provided with metadata
	a.checkStateRootConsistentWithMetadata()
}
