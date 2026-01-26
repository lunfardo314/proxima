package attacher

import (
	"encoding/hex"
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
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
		root, stats := a.commitBranch()
		a.finals.StateRoot = root
		a.finals.MutationStats = stats
	}
	a.checkConsistencyWithMetadata()
}

func (a *milestoneAttacher) commitBranch() (common.VCommitment, vertex.MutationStats) {
	a.Assertf(a.vid.IsBranchTransaction(), "a.vid.IsBranchTransaction()")

	muts, stats, committedTxs := a.pastCone.Mutations(a.vid.Slot())

	seqID, stemOID := a.vid.MustSequencerIDAndStemID()
	upd := multistate.MustNewUpdatable(a.StateStore(), a.BaselineSugaredStateReader().Root())

	// Inject any missing upgrade UTXOs for upgrade slots up to this branch
	injectedUpgrades := multistate.InjectMissingUpgradeUTXOs(muts, a.BaselineSugaredStateReader(), a.vid.Slot())

	// Log highlighted message when upgrades are activated
	for _, upg := range injectedUpgrades {
		a.Log().Infof("\n"+
			"***************************************************************\n"+
			"***         LEDGER UPGRADE ACTIVATED AT SLOT %-6d         ***\n"+
			"***************************************************************\n"+
			" Library Hash: %s\n"+
			"***************************************************************",
			upg.Slot, hex.EncodeToString(upg.LibraryHash[:]))
	}

	// GC-ing txids old enough. This is a deterministic operation on the state
	if a.vid.Slot() > a.TxIDStateTTLSlots {
		gcSlot := a.vid.Slot() - a.TxIDStateTTLSlots
		gcTxIDs := upd.Readable().KnownCommittedTxIDs(gcSlot)
		muts.DeleteTxIDs(gcTxIDs...)
	}

	err := upd.Update(muts, &multistate.RootRecordParams{
		StemOutputID:    stemOID,
		SeqID:           seqID,
		CoverageDelta:   *a.finals.CoverageDelta,
		FrozenCoverage:  *a.finals.FrozenCoverage,
		SlotInflation:   *a.finals.SlotInflation,
		Supply:          *a.finals.Supply,
		NumTransactions: uint32(a.finals.MutationStats.NumTransactions),
	})
	if err != nil {
		err = fmt.Errorf("attacher wrapup (%s) -> %w:\n------ tx\n%s\n-------- past cone --------\n%s",
			a.Name(), err, a.vid.TxLines("    ").String(), a.pastCone.Lines("     ").Join("\n"))
	}
	a.AssertNoError(err)
	a.EvidenceBranchSlot(a.vid.Slot(), global.IsHealthyCoverageDelta(*a.finals.CoverageDelta, *a.finals.Supply, global.FractionHealthyBranch))

	branchID := a.vid.ID()
	a.LogTx(time.Now(), fmt.Sprintf("committed in branch %s", branchID.String()), committedTxs...)
	return upd.Root(), stats
}
