package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/stretchr/testify/require"
)

// Several branches of the upgrade slot, each built on a baseline from before it, must
// all receive the upgrade UTXO, however many of them a node commits and in whatever
// order. A process-wide "upgrade already injected" shortcut once skipped every branch
// after the first, so nodes that committed competing branches in different orders
// stored different states for the same branch and rejected each other's next branches.
func TestUpgradeUTXOInjectedIntoEveryBranchOfTheSlot(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	withUpgrade1(t, u)

	baseline := u.StateReader() // genesis state: no upgrade UTXO yet
	upgradeOID := base.UpgradeOutputID(upgrade1Slot)
	require.False(t, baseline.HasUTXO(upgradeOID))

	// a branch before the upgrade slot gets nothing
	require.Empty(t, multistate.InjectMissingUpgradeUTXOs(multistate.NewMutations(), baseline, upgrade1Slot-1))

	// three competing branches of the upgrade slot and one of a later slot, all on the
	// same old baseline: each one gets the upgrade UTXO
	for _, slot := range []uint32{upgrade1Slot, upgrade1Slot, upgrade1Slot, upgrade1Slot + 2} {
		muts := multistate.NewMutations()
		injected := multistate.InjectMissingUpgradeUTXOs(muts, baseline, slot)
		require.Len(t, injected, 1, "branch of slot %d", slot)
		require.EqualValues(t, upgrade1Slot, injected[0].Slot)
		_, found := muts.FindAddedOutput(upgradeOID)
		require.True(t, found, "branch of slot %d carries the upgrade UTXO", slot)
	}
}
