package tests

import (
	"testing"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/stretchr/testify/require"
)

// Five sequencers, all producing branches, run across the slot of library upgrade 2: the case
// the rehearsal of upgrade 1 never had, competing branches at the upgrade slot. Checked:
//   - every branch committed at or after the slot holds the upgrade UTXO (the invariant broken
//     at slot 29000, when sibling branches of the slot were stored without it);
//   - no sequencer transaction other than a branch after the slot is before the post-branch
//     consolidation tick;
//   - the sequencers keep producing branches after the slot.
//
// The test ledger's slot is about a second and upgrades are at least MinSlotsBetweenUpgrades
// from genesis, so the test runs about two minutes. Upgrade 2 is listed alone, on the genesis
// library: every function it replaces is there too.
func TestSequencersAcrossUpgrade2(t *testing.T) {
	const (
		upgradeSlot  = multistate.MinSlotsBetweenUpgrades + 10
		runPastSlots = 12
	)
	ledger.Upgrades = []*ledger.UpgradeDefinition{{Slot: upgradeSlot, Build: ledger.BuildUpgrade2Library}}
	t.Cleanup(func() {
		ledger.Upgrades = nil
		reinitTestLedger()
	})

	testData := initMultiSequencerTest(t, 4, true)
	require.EqualValues(t, 0, ledger.L(upgradeSlot-1).PostBranchConsolidationTicks)
	require.EqualValues(t, 9, ledger.L(upgradeSlot).PostBranchConsolidationTicks)

	testData.startSequencersWithTimeout(1000)
	deadline := time.Now().Add(time.Duration(upgradeSlot+runPastSlots+20) * ledger.SlotDuration())
	for ledger.TimeNow().Slot < upgradeSlot+runPastSlots {
		require.True(t, time.Now().Before(deadline), "the ledger clock did not reach the end slot")
		time.Sleep(ledger.SlotDuration())
	}
	require.True(t, testData.stopAndWait(10*time.Second))

	store := testData.env.StateStore()
	upgradeOID := base.UpgradeOutputID(upgradeSlot)
	branchesAfter := 0
	for slot := uint32(upgradeSlot); slot <= multistate.FetchLatestCommittedSlot(store); slot++ {
		for _, rr := range multistate.FetchRootRecords(store, slot) {
			rdr, err := multistate.NewReadable(store, rr.Root)
			require.NoError(t, err)
			require.True(t, rdr.HasUTXO(upgradeOID), "a branch of slot %d lacks the upgrade UTXO", slot)
			branchesAfter++
		}
	}
	t.Logf("branches committed at or after the upgrade slot %d: %d", upgradeSlot, branchesAfter)
	require.Greater(t, branchesAfter, runPastSlots/2, "branches must keep coming after the upgrade slot")

	early, milestonesAfter := 0, 0
	testData.txStore.(*txstore.SimpleTxBytesStore).Iterator(nil).Iterate(func(k, _ []byte) bool {
		txid, err := base.TransactionIDFromBytes(k)
		if err != nil || !txid.IsSequencerTransaction() || txid.IsBranchTransaction() || txid.Slot() < upgradeSlot {
			return true
		}
		milestonesAfter++
		if txid.Timestamp().Tick < 9 {
			early++
			t.Logf("milestone before the post-branch consolidation tick: %s", txid.StringShort())
		}
		return true
	})
	t.Logf("milestones after the upgrade slot: %d", milestonesAfter)
	require.Greater(t, milestonesAfter, 0)
	require.Zero(t, early, "no sequencer transaction other than a branch before tick 9 after the upgrade")

	contested, maxBonusWon := lineageWinsByBonus(t, testData, upgradeSlot+1)
	t.Logf("slots after the upgrade with several branches: %d, won by the branch with the largest bonus: %d", contested, maxBonusWon)
	// The bootstrap sequencer holds far more capital than the others: if issuers stayed on their
	// own branches, capital would decide and the largest bonus would win about one slot in five.
	require.Greater(t, contested, 0)
	require.GreaterOrEqual(t, 3*maxBonusWon, 2*contested, "the largest bonus must win most contested slots")
}

// lineageWinsByBonus walks the lineage of the heaviest latest branch back to fromSlot and counts,
// over the slots that committed more than one branch, those whose lineage branch has the largest
// branch inflation bonus of its slot. With the branches bare at the first milestones and each
// sequencer maximising coverage, the bonus is what tells equal branches apart.
func lineageWinsByBonus(t *testing.T, td *workflowTestData, fromSlot uint32) (contested, maxBonusWon int) {
	store := td.env.StateStore()
	bonus := func(txid base.TransactionID) uint64 {
		txBytes := td.txStore.GetTxBytes(&txid)
		require.NotNil(t, txBytes)
		tx, err := transaction.ParseWithPartialValidation(txBytes)
		require.NoError(t, err)
		return tx.SequencerOutput().Output.Inflation()
	}
	latest := multistate.FetchLatestBranches(store)
	require.NotEmpty(t, latest)
	heaviest := latest[0]
	for _, bd := range latest[1:] {
		if bd.TotalCoverage > heaviest.TotalCoverage {
			heaviest = bd
		}
	}
	multistate.IterateBranchChainBack(store, heaviest, func(branchID *base.TransactionID, _ *multistate.BranchData) bool {
		slot := branchID.Slot()
		if slot < fromSlot {
			return false
		}
		siblings := multistate.FetchBranchDataMulti(store, multistate.FetchRootRecords(store, slot)...)
		if len(siblings) < 2 {
			return true
		}
		contested++
		won := bonus(*branchID)
		largest := true
		for _, s := range siblings {
			if id := s.TxID(); id != *branchID && bonus(id) > won {
				largest = false
			}
		}
		if largest {
			maxBonusWon++
		}
		return true
	})
	return
}
