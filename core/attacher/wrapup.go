package attacher

import (
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
)

func (a *milestoneAttacher) wrapUpAttacher() {
	a.Tracef(TraceTagAttachMilestone, "wrapUpAttacher")

	a.slotInflation = a.pastCone.CalculateSlotInflation()
	a.checkConsistencyWithMetadata()

	a.finals.baseline = &a.baseline.ID
	a.finals.numVertices = a.pastCone.NumVertices()

	a.finals.coverage = a.LedgerCoverage(a.vid.Timestamp())
	a.finals.slotInflation = a.slotInflation

	a.Tracef(TraceTagAttachMilestone, "set ledger baselineCoverage in %s to %s",
		a.vid.IDShortString, func() string { return util.Th(a.finals.coverage) })

	if a.vid.IsBranchTransaction() {
		a.commitBranch()
		a.Tracef(TraceTagAttachMilestone, "finalized branch")
	} else {
		a.Tracef(TraceTagAttachMilestone, "finalized sequencer milestone")
	}

	calculatedMetadata := txmetadata.TransactionMetadata{
		LedgerCoverage: util.Ref(a.finals.coverage),
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

	muts, stats := a.pastCone.Mutations(a.vid.Slot())

	a.finals.numNewTransactions, a.finals.numDeletedOutputs, a.finals.numCreatedOutputs = uint32(stats.NumTransactions), stats.NumDeleted, stats.NumCreated

	seqID, stemOID := a.vid.MustSequencerIDAndStemID()
	upd := multistate.MustNewUpdatable(a.StateStore(), a.baselineSugaredStateReader().Root())
	a.finals.supply = a.baselineSupply + a.finals.slotInflation
	coverage := a.LedgerCoverage(a.vid.Timestamp())

	util.Assertf(a.slotInflation == a.finals.slotInflation, "a.slotInflation == a.finals.slotInflation")
	supply := a.FinalSupply()

	err := upd.Update(muts, &multistate.RootRecordParams{
		StemOutputID:    stemOID,
		SeqID:           seqID,
		Coverage:        coverage,
		SlotInflation:   a.slotInflation,
		Supply:          supply,
		NumTransactions: a.finals.numNewTransactions,
	})
	if err != nil {
		err = fmt.Errorf("%w:\n-------- past cone of %s --------\n%s", err, a.Name(), a.pastCone.Lines("     ").Join("\n"))
		a.pastCone.SaveGraph(a.vid.ID.AsFileNameShort())
		a.SaveFullDAG("full_dag_failed_upd")
		time.Sleep(2 * time.Second)
	}
	a.AssertNoError(err)

	a.finals.root = upd.Root()

	a.EvidenceBranchSlot(a.vid.Slot(), global.IsHealthyCoverage(coverage, supply, global.FractionHealthyBranch))

	// check consistency with state root provided with metadata
	a.checkStateRootConsistentWithMetadata()
}
