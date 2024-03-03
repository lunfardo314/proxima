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

	a.checkMetadataLedgerCoverage()

	a.finals.baseline = &a.baseline.ID
	a.finals.numTransactions = len(a.vertices)

	a.finals.coverage = a.coverage
	util.Assertf(a.finals.coverage.LatestDelta() > 0, "final coverage must be positive")

	a.Tracef(TraceTagAttachMilestone, "set ledger baselineCoverage in %s to %s", a.vid.IDShortString(), a.finals.coverage.String())
	a.vid.SetLedgerCoverage(a.finals.coverage)

	if a.vid.IsBranchTransaction() {
		a.commitBranch()
		a.EvidenceBookedBranch(&a.vid.ID, a.vid.MustSequencerID())
		a.Tracef(TraceTagAttachMilestone, "finalized branch")
	} else {
		a.finals.slotInflation = a.calculateSlotInflation()
		a.Tracef(TraceTagAttachMilestone, "finalized sequencer milestone")
	}

	// persist transaction bytes, if needed
	if a.metadata == nil || a.metadata.SourceTypeNonPersistent != txmetadata.SourceTypeTxStore {
		a.vid.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
			flags := a.vid.FlagsNoLock()
			if !flags.FlagsUp(vertex.FlagVertexTxBytesPersisted) {
				persistentMetadata := txmetadata.TransactionMetadata{
					StateRoot:           a.finals.root,
					LedgerCoverageDelta: util.Ref(a.coverage.LatestDelta()),
					SlotInflation:       util.Ref(a.finals.slotInflation),
				}
				a.AsyncPersistTxBytesWithMetadata(v.Tx.Bytes(), &persistentMetadata)
				a.vid.SetFlagsUpNoLock(vertex.FlagVertexTxBytesPersisted)
			}
		}})
	}
}

func (a *milestoneAttacher) commitBranch() {
	util.Assertf(a.vid.IsBranchTransaction(), "a.vid.IsBranchTransaction()")

	muts := multistate.NewMutations()

	// generate DEL mutations
	for vid, consumed := range a.rooted {
		for idx := range consumed {
			out := vid.MustOutputWithIDAt(idx)
			muts.InsertDelOutputMutation(out.ID)
			a.finals.numDeletedOutputs++
		}
	}
	// generate ADD TX and ADD OUTPUT mutations
	allVerticesSet := set.NewFromKeys(a.vertices)
	for vid := range a.vertices {
		if a.isKnownRooted(vid) {
			continue
		}
		muts.InsertAddTxMutation(vid.ID, a.vid.Slot(), byte(vid.NumProducedOutputs()-1))
		// ADD OUTPUT mutations only for not consumed outputs
		producedOutputIndices := vid.NotConsumedOutputIndices(allVerticesSet)
		for _, idx := range producedOutputIndices {
			muts.InsertAddOutputMutation(vid.OutputID(idx), vid.MustOutputAt(idx))
			a.finals.numCreatedOutputs++
		}
	}

	seqID, stemOID := a.vid.MustSequencerIDAndStemID()
	upd := multistate.MustNewUpdatable(a.StateStore(), a.baselineStateReader().Root())
	a.finals.slotInflation = a.calculateSlotInflation()
	a.finals.supply = a.baselineSupply + a.finals.slotInflation
	upd.MustUpdate(muts, &multistate.RootRecordParams{
		StemOutputID:  stemOID,
		SeqID:         seqID,
		Coverage:      *a.vid.GetLedgerCoverage(),
		SlotInflation: a.finals.slotInflation,
		Supply:        a.baselineSupply + a.finals.slotInflation,
	})
	a.finals.root = upd.Root()

	// check consistency with state root provided with metadata
	a.checkMetadataStateRoot()
}
