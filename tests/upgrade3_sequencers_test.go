package tests

import (
	"testing"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/stretchr/testify/require"
)

// Five sequencers, all producing branches, run across the slot of library upgrade 3, which
// changes the VRF message of a branch: from the slot on it is chained on the predecessor
// stem's VRF output instead of its proof. A branch whose proof is over the wrong message is
// refused, so branches committed after the slot, competing ones included, show that the
// sequencers and the ledger switch together. Checked: every branch at or after the slot holds
// the upgrade UTXO, and the sequencers keep producing branches after it.
//
// As for upgrade 2, the test runs about two minutes. Upgrade 3 is listed alone, on the
// genesis library: every function it replaces is there too.
func TestSequencersAcrossUpgrade3(t *testing.T) {
	const (
		upgradeSlot  = multistate.MinSlotsBetweenUpgrades + 10
		runPastSlots = 12
	)
	ledger.Upgrades = []*ledger.UpgradeDefinition{{Slot: upgradeSlot, Build: ledger.BuildUpgrade3Library}}
	t.Cleanup(func() {
		ledger.Upgrades = nil
		reinitTestLedger()
	})

	testData := initMultiSequencerTest(t, 4, true)
	_, err := ledger.L(upgradeSlot - 1).FunctionByName("branchVRFMessage")
	require.Error(t, err)
	_, err = ledger.L(upgradeSlot).FunctionByName("branchVRFMessage")
	require.NoError(t, err)

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
