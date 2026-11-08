package multistate

import (
	"fmt"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

// A pending library upgrade, stored at start but committed by no state until the branches of
// its slot, may still be changed by a later release: amended rules, a moved slot or a cancelled
// upgrade replace what the database holds. Once a branch at or after the slot exists, the
// stored library is committed and any difference is refused, as before.

const pendingSlot = 1000

// amendedUpgrade1 is upgrade 1 with one more definition: other rules, another library hash.
func amendedUpgrade1(prev []byte) ([]byte, error) {
	lib, err := ledger.BuildUpgrade1Library(prev)
	if err != nil {
		return nil, err
	}
	return ledger.BuildUpgradeFromSource(lib, "amended", "func constPendingAmendment : u64/7")
}

// pendingStore is a store holding an unfolded genesis library, started once by a release that
// lists upgrade 1 at pendingSlot, which it stores.
func pendingStore(t *testing.T) *common.InMemoryKVStore {
	t.Helper()
	params, _ := ledger.GetTestingLedgerParams(1)
	params.FoldUpgrades = nil
	store := common.NewInMemoryKVStore()
	require.NoError(t, WriteUpgradeLibrary(store, 0, ledger.LibraryJSONFromParameters(params, true)))
	_, err := startWith(t, store, []*ledger.UpgradeDefinition{{Slot: pendingSlot, Build: ledger.BuildUpgrade1Library}})
	require.NoError(t, err)
	require.True(t, HasUpgradeLibrary(store, pendingSlot))
	return store
}

// startWith runs the node's start-up path on store with the release listing upgrades.
func startWith(t *testing.T, store *common.InMemoryKVStore, upgrades []*ledger.UpgradeDefinition) ([]string, error) {
	t.Helper()
	saved := ledger.Upgrades
	ledger.Upgrades = upgrades
	t.Cleanup(func() {
		ledger.Upgrades = saved
		ledger.ResetForTesting()
	})
	var warnings []string
	ledger.ResetForTesting()
	err := InitLedgerFromStore(store, func(format string, args ...any) {
		warnings = append(warnings, fmt.Sprintf(format, args...))
	})
	return warnings, err
}

func storedHash(t *testing.T, store *common.InMemoryKVStore, slot uint32) [32]byte {
	t.Helper()
	b, found := GetUpgradeLibraryDirect(store, slot)
	require.True(t, found)
	h, err := libraryHashFromJSON(b)
	require.NoError(t, err)
	return h
}

func TestPendingUpgradeReplaced(t *testing.T) {
	t.Run("amended rules replace the stored library", func(t *testing.T) {
		store := pendingStore(t)
		before := storedHash(t, store, pendingSlot)
		warnings, err := startWith(t, store, []*ledger.UpgradeDefinition{{Slot: pendingSlot, Build: amendedUpgrade1}})
		require.NoError(t, err)
		require.NotEqual(t, before, storedHash(t, store, pendingSlot))
		require.Len(t, warnings, 1)
		require.Contains(t, warnings[0], "PENDING LIBRARY UPGRADE REPLACED")
		require.Contains(t, warnings[0], "other definitions")
		// a restart on the same release changes nothing more
		warnings, err = startWith(t, store, []*ledger.UpgradeDefinition{{Slot: pendingSlot, Build: amendedUpgrade1}})
		require.NoError(t, err)
		require.Empty(t, warnings)
	})
	t.Run("a moved slot drops the old one and stores the new one", func(t *testing.T) {
		store := pendingStore(t)
		_, err := startWith(t, store, []*ledger.UpgradeDefinition{{Slot: pendingSlot + 200, Build: ledger.BuildUpgrade1Library}})
		require.NoError(t, err)
		require.False(t, HasUpgradeLibrary(store, pendingSlot))
		require.True(t, HasUpgradeLibrary(store, pendingSlot+200))
	})
	t.Run("a cancelled upgrade is dropped", func(t *testing.T) {
		store := pendingStore(t)
		_, err := startWith(t, store, nil)
		require.NoError(t, err)
		require.False(t, HasUpgradeLibrary(store, pendingSlot))
	})
	t.Run("once a branch at the slot exists, a difference is refused", func(t *testing.T) {
		store := pendingStore(t)
		batch := store.BatchedWriter()
		batch.Set([]byte{latestSlotDBPartition}, base.Slot2Bytes(pendingSlot))
		require.NoError(t, batch.Commit())
		before := storedHash(t, store, pendingSlot)
		_, err := startWith(t, store, []*ledger.UpgradeDefinition{{Slot: pendingSlot, Build: amendedUpgrade1}})
		require.ErrorContains(t, err, "this release builds")
		require.Equal(t, before, storedHash(t, store, pendingSlot), "the committed library stays")
		require.Error(t, deletePendingUpgradeLibrary(store, pendingSlot))
	})
}
