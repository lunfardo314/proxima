package multistate

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

// BuildUpgradeLibraries, with no state, gives byte for byte the libraries a node stores for the same
// genesis when it starts (InitLedgerFromStore): one per upgrade the release lists, at its slot.
func TestBuildUpgradeLibrariesMatchesNode(t *testing.T) {
	ledger.ResetForTesting()
	ledger.InitWithTestingLedgerData(ledger.WithoutFoldedUpgrades())
	t.Cleanup(ledger.ResetForTesting)

	store := common.NewInMemoryKVStore()
	InitStateStoreFromGlobals(store)
	genesisJSON, found := GetUpgradeLibraryDirect(store, 0)
	require.True(t, found)

	built, err := ledger.BuildUpgradeLibraries(genesisJSON)
	require.NoError(t, err)
	require.Len(t, built, len(ledger.Upgrades)+1)

	ledger.ResetForTesting()
	require.NoError(t, InitLedgerFromStore(store))
	for slot, libJSON := range built {
		stored, found := GetUpgradeLibraryDirect(store, slot)
		require.True(t, found, "the node stored no library at slot %d", slot)
		require.Equal(t, stored, libJSON, "library at slot %d differs", slot)
	}

	// the cache initialised from the built libraries applies each at its slot
	ledger.ResetForTesting()
	ledger.MustInitLibraryCacheFromMap(built)
	last := ledger.Upgrades[len(ledger.Upgrades)-1]
	require.EqualValues(t, len(ledger.Upgrades), ledger.L(last.Slot).UpgradeIndex())
	require.EqualValues(t, len(ledger.Upgrades)-1, ledger.L(last.Slot-1).UpgradeIndex())
}
