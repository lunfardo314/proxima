package tests

import (
	"testing"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/stretchr/testify/require"
)

// Five sequencers, all producing branches, run across the slot of library upgrade 4: every
// endorsement a sequencer transaction, no chain origin as a sequencer output, and, on the node,
// the declared coverage delta of every milestone cross-checked, the non-branch ones on the
// branch of their own slot included (core/attacher, recomputedAgainstForeignBaseline). The
// sequencers already comply with the ledger rules, so the run shows that nothing they produce
// is refused from the slot on. Checked: every branch at or after the slot holds the upgrade
// UTXO, and the sequencers keep producing branches after it.
//
// Upgrade 4 replaces the sequencer constraint of upgrade 2, so upgrades 2 and 3 are listed
// before it, at the minimum spacing; the test runs about five and a half minutes.
func TestSequencersAcrossUpgrade4(t *testing.T) {
	const (
		upgrade2Slot = multistate.MinSlotsBetweenUpgrades + 10
		upgrade3Slot = upgrade2Slot + multistate.MinSlotsBetweenUpgrades
		upgradeSlot  = upgrade3Slot + multistate.MinSlotsBetweenUpgrades
		runPastSlots = 12
	)
	ledger.Upgrades = []*ledger.UpgradeDefinition{
		{Slot: upgrade2Slot, Build: ledger.BuildUpgrade2Library},
		{Slot: upgrade3Slot, Build: ledger.BuildUpgrade3Library},
		{Slot: upgradeSlot, Build: ledger.BuildUpgrade4Library},
	}
	t.Cleanup(func() {
		ledger.Upgrades = nil
		reinitTestLedger()
	})

	testData := initMultiSequencerTest(t, 4, true)
	require.False(t, ledger.L(upgradeSlot-1).HasUpgrade4())
	require.True(t, ledger.L(upgradeSlot).HasUpgrade4())

	testData.startSequencersWithTimeout(1000)
	deadline := time.Now().Add(time.Duration(upgradeSlot+runPastSlots+20) * ledger.SlotDuration())
	for ledger.TimeNow().Slot < upgradeSlot+runPastSlots {
		require.True(t, time.Now().Before(deadline), "the ledger clock did not reach the end slot")
		time.Sleep(ledger.SlotDuration())
	}
	require.True(t, testData.stopAndWait(10*time.Second))

	store := testData.env.StateStore()
	upgradeOID := base.UpgradeOutputID(upgradeSlot)
	branchesAfter, contested := 0, 0
	for slot := uint32(upgradeSlot); slot <= multistate.FetchLatestCommittedSlot(store); slot++ {
		roots := multistate.FetchRootRecords(store, slot)
		if len(roots) > 1 {
			contested++
		}
		for _, rr := range roots {
			rdr, err := multistate.NewReadable(store, rr.Root)
			require.NoError(t, err)
			require.True(t, rdr.HasUTXO(upgradeOID), "a branch of slot %d lacks the upgrade UTXO", slot)
			branchesAfter++
		}
	}
	t.Logf("branches committed at or after the upgrade slot %d: %d, slots with several: %d", upgradeSlot, branchesAfter, contested)
	require.Greater(t, branchesAfter, runPastSlots/2, "branches must keep coming after the upgrade slot")
}
