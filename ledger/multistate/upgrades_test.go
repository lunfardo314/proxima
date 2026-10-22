package multistate

import (
	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger/base"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

// Tests for the upgrade library storage layer.
// These tests verify the DB partition for storing compiled library JSON blobs.

func TestUpgradeLibraryStorage_WriteAndReadDirect(t *testing.T) {
	// Test basic write and direct read of a library at a specific slot
	store := common.NewInMemoryKVStore()

	// Write a library at slot 0 (genesis upgrade)
	libraryJSON := []byte("test library JSON for slot 0")
	err := WriteUpgradeLibrary(store, 0, libraryJSON)
	require.NoError(t, err)

	// Read it back directly
	readJSON, found := GetUpgradeLibraryDirect(store, 0)
	require.True(t, found, "should find library at slot 0")
	require.Equal(t, libraryJSON, readJSON, "library JSON should match")

	// Try to read from a non-existent slot
	_, found = GetUpgradeLibraryDirect(store, 100)
	require.False(t, found, "should not find library at non-existent slot")
}

func TestUpgradeLibraryStorage_MultipleUpgrades(t *testing.T) {
	// Test storing multiple upgrade versions with proper spacing
	// Uses WriteUpgradeLibraryUnchecked to bypass identity validation (test uses placeholder data)
	store := common.NewInMemoryKVStore()

	// Write libraries at different upgrade slots (respecting MinSlotsBetweenUpgrades)
	lib0 := []byte("library version 0")
	lib1000 := []byte("library version at slot 1000")
	lib5000 := []byte("library version at slot 5000")

	require.NoError(t, WriteUpgradeLibraryUnchecked(store, 0, lib0))
	require.NoError(t, WriteUpgradeLibraryUnchecked(store, 1000, lib1000)) // 1000 > MinSlotsBetweenUpgrades from 0
	require.NoError(t, WriteUpgradeLibraryUnchecked(store, 5000, lib5000)) // 4000 > MinSlotsBetweenUpgrades from 1000

	// Verify count
	count := CountUpgradeLibraries(store)
	require.Equal(t, 3, count, "should have 3 libraries")

	// Read each directly
	readLib0, found := GetUpgradeLibraryDirect(store, 0)
	require.True(t, found)
	require.Equal(t, lib0, readLib0)

	readLib1000, found := GetUpgradeLibraryDirect(store, 1000)
	require.True(t, found)
	require.Equal(t, lib1000, readLib1000)

	readLib5000, found := GetUpgradeLibraryDirect(store, 5000)
	require.True(t, found)
	require.Equal(t, lib5000, readLib5000)
}

func TestUpgradeLibraryStorage_GetForSlot(t *testing.T) {
	// Test finding the applicable library for any given slot
	// Uses WriteUpgradeLibraryUnchecked to bypass identity validation (test uses placeholder data)
	store := common.NewInMemoryKVStore()

	// Set up upgrade schedule (with proper spacing):
	// - slot 0: version 0
	// - slot 1000: version 1
	// - slot 5000: version 2
	lib0 := []byte("library version 0")
	lib1000 := []byte("library version 1")
	lib5000 := []byte("library version 2")

	require.NoError(t, WriteUpgradeLibraryUnchecked(store, 0, lib0))
	require.NoError(t, WriteUpgradeLibraryUnchecked(store, 1000, lib1000))
	require.NoError(t, WriteUpgradeLibraryUnchecked(store, 5000, lib5000))

	testCases := []struct {
		querySlot       uint32
		expectedJSON    []byte
		expectedUpgSlot uint32
		expectedFound   bool
		description     string
	}{
		{0, lib0, 0, true, "exactly at genesis upgrade"},
		{1, lib0, 0, true, "after genesis, before first upgrade"},
		{500, lib0, 0, true, "midway before first upgrade"},
		{999, lib0, 0, true, "just before first upgrade"},
		{1000, lib1000, 1000, true, "exactly at first upgrade"},
		{1001, lib1000, 1000, true, "just after first upgrade"},
		{3000, lib1000, 1000, true, "midway between upgrades"},
		{4999, lib1000, 1000, true, "just before second upgrade"},
		{5000, lib5000, 5000, true, "exactly at second upgrade"},
		{5001, lib5000, 5000, true, "after second upgrade"},
		{10000, lib5000, 5000, true, "far future slot"},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			jsonData, upgSlot, found := GetUpgradeLibraryForSlot(store, tc.querySlot)
			require.Equal(t, tc.expectedFound, found, "found mismatch for slot %d", tc.querySlot)
			if tc.expectedFound {
				require.Equal(t, tc.expectedJSON, jsonData, "JSON mismatch for slot %d", tc.querySlot)
				require.Equal(t, tc.expectedUpgSlot, upgSlot, "upgrade slot mismatch for slot %d", tc.querySlot)
			}
		})
	}
}

func TestUpgradeLibraryStorage_EmptyStore(t *testing.T) {
	// Test behavior with empty store
	store := common.NewInMemoryKVStore()

	// Count should be 0
	count := CountUpgradeLibraries(store)
	require.Equal(t, 0, count)

	// GetForSlot should return not found
	_, _, found := GetUpgradeLibraryForSlot(store, 100)
	require.False(t, found, "should not find library in empty store")

	// GetLatestUpgradeSlot should return not found
	_, found = GetLatestUpgradeSlot(store)
	require.False(t, found, "should not have latest upgrade slot in empty store")
}

func TestUpgradeLibraryStorage_GetLatestUpgradeSlot(t *testing.T) {
	// Test getting the latest upgrade slot
	// Uses WriteUpgradeLibraryUnchecked to bypass identity validation (test uses placeholder data)
	store := common.NewInMemoryKVStore()

	// Add upgrades in sequential order (required by constraints)
	require.NoError(t, WriteUpgradeLibraryUnchecked(store, 0, []byte("v0")))
	require.NoError(t, WriteUpgradeLibraryUnchecked(store, 1000, []byte("v1")))
	require.NoError(t, WriteUpgradeLibraryUnchecked(store, 5000, []byte("v2")))

	latestSlot, found := GetLatestUpgradeSlot(store)
	require.True(t, found)
	require.Equal(t, uint32(5000), latestSlot, "should return highest upgrade slot")
}

func TestUpgradeLibraryStorage_Iterate(t *testing.T) {
	// Test that iteration visits all libraries (order depends on underlying store)
	// Uses WriteUpgradeLibraryUnchecked to bypass identity validation (test uses placeholder data)
	store := common.NewInMemoryKVStore()

	// Add upgrades in sequential order with proper spacing
	require.NoError(t, WriteUpgradeLibraryUnchecked(store, 0, []byte("v0")))
	require.NoError(t, WriteUpgradeLibraryUnchecked(store, 1000, []byte("v1")))
	require.NoError(t, WriteUpgradeLibraryUnchecked(store, 3000, []byte("v1.5")))
	require.NoError(t, WriteUpgradeLibraryUnchecked(store, 5000, []byte("v2")))

	// Collect slots during iteration
	slotsFound := make(map[uint32]bool)
	IterateUpgradeLibraries(store, func(upgradeSlot uint32, _ []byte) bool {
		slotsFound[upgradeSlot] = true
		return true
	})

	require.Equal(t, 4, len(slotsFound), "should iterate all 4 libraries")
	require.True(t, slotsFound[0], "should find slot 0")
	require.True(t, slotsFound[1000], "should find slot 1000")
	require.True(t, slotsFound[3000], "should find slot 3000")
	require.True(t, slotsFound[5000], "should find slot 5000")
}

func TestUpgradeLibraryStorage_Delete(t *testing.T) {
	// Test deletion of a library (test-only function)
	// Uses WriteUpgradeLibraryUnchecked to bypass identity validation (test uses placeholder data)
	store := common.NewInMemoryKVStore()

	require.NoError(t, WriteUpgradeLibraryUnchecked(store, 0, []byte("v0")))
	require.NoError(t, WriteUpgradeLibraryUnchecked(store, 1000, []byte("v1")))

	require.Equal(t, 2, CountUpgradeLibraries(store))
	require.True(t, HasUpgradeLibrary(store, 1000))

	// Delete one (using test-only function)
	DeleteUpgradeLibraryForTesting(store, 1000)

	require.Equal(t, 1, CountUpgradeLibraries(store))
	require.False(t, HasUpgradeLibrary(store, 1000))
	require.True(t, HasUpgradeLibrary(store, 0))
}

func TestUpgradeLibraryStorage_ConstraintViolations(t *testing.T) {
	// Test that constraint violations are properly rejected
	// Uses WriteUpgradeLibraryUnchecked to bypass identity validation (test uses placeholder data)

	t.Run("cannot write same slot twice", func(t *testing.T) {
		store := common.NewInMemoryKVStore()
		require.NoError(t, WriteUpgradeLibraryUnchecked(store, 0, []byte("v0")))

		// Attempting to write to same slot should fail
		err := WriteUpgradeLibraryUnchecked(store, 0, []byte("v0 updated"))
		require.Error(t, err)
		require.Contains(t, err.Error(), "must be greater than")
	})

	t.Run("cannot write earlier slot", func(t *testing.T) {
		store := common.NewInMemoryKVStore()
		require.NoError(t, WriteUpgradeLibraryUnchecked(store, 0, []byte("v0")))
		require.NoError(t, WriteUpgradeLibraryUnchecked(store, 1000, []byte("v1")))

		// Attempting to write an earlier slot should fail
		err := WriteUpgradeLibraryUnchecked(store, 500, []byte("v0.5"))
		require.Error(t, err)
		require.Contains(t, err.Error(), "must be greater than")
	})

	t.Run("cannot write slot too close", func(t *testing.T) {
		store := common.NewInMemoryKVStore()
		require.NoError(t, WriteUpgradeLibraryUnchecked(store, 0, []byte("v0")))
		require.NoError(t, WriteUpgradeLibraryUnchecked(store, 1000, []byte("v1")))

		// Attempting to write too close to previous upgrade should fail
		err := WriteUpgradeLibraryUnchecked(store, 1050, []byte("v2")) // Only 50 slots after 1000
		require.Error(t, err)
		require.Contains(t, err.Error(), "too close")
		require.Contains(t, err.Error(), "minimum distance")
	})

	t.Run("first upgrade after genesis can be close", func(t *testing.T) {
		// The first upgrade after slot 0 only needs to be > 0
		// (minimum distance check is skipped when previous slot is 0)
		store := common.NewInMemoryKVStore()
		require.NoError(t, WriteUpgradeLibraryUnchecked(store, 0, []byte("v0")))

		// Can write at slot 100 even though < MinSlotsBetweenUpgrades from 0
		err := WriteUpgradeLibraryUnchecked(store, 100, []byte("v1"))
		require.NoError(t, err)
	})
}

func TestUpgradeLibraryStorage_HasUpgradeLibrary(t *testing.T) {
	// Test the HasUpgradeLibrary function
	store := common.NewInMemoryKVStore()

	require.False(t, HasUpgradeLibrary(store, 0))
	require.False(t, HasUpgradeLibrary(store, 1000))

	require.NoError(t, WriteUpgradeLibrary(store, 0, []byte("v0")))

	require.True(t, HasUpgradeLibrary(store, 0))
	require.False(t, HasUpgradeLibrary(store, 1000))
}

func TestUpgradeLibraryStorage_LargeJSON(t *testing.T) {
	// Test with a larger JSON payload similar to real library size
	store := common.NewInMemoryKVStore()

	// Create a larger JSON blob (simulating a real compiled library)
	largeJSON := make([]byte, 100*1024) // 100KB
	for i := range largeJSON {
		largeJSON[i] = byte(i % 256)
	}

	require.NoError(t, WriteUpgradeLibrary(store, 0, largeJSON))

	readJSON, found := GetUpgradeLibraryDirect(store, 0)
	require.True(t, found)
	require.Equal(t, largeJSON, readJSON)
}

// Tests for pending upgrade registration

func TestFindPreviousLibrary_Basic(t *testing.T) {
	store := common.NewInMemoryKVStore()

	// No libraries - should return nil
	_, jsonData := findPreviousLibrary(store, 100)
	require.Nil(t, jsonData, "should return nil when no libraries exist")

	// Add genesis library
	require.NoError(t, WriteUpgradeLibraryUnchecked(store, 0, []byte("v0")))

	// Find previous for slot 100 - should return genesis
	slot, jsonData := findPreviousLibrary(store, 100)
	require.NotNil(t, jsonData)
	require.Equal(t, uint32(0), slot)
	require.Equal(t, []byte("v0"), jsonData)

	// Find previous for slot 0 - should return nil (no library before 0)
	_, jsonData = findPreviousLibrary(store, 0)
	require.Nil(t, jsonData, "should return nil when looking for library before slot 0")
}

func TestFindPreviousLibrary_MultipleUpgrades(t *testing.T) {
	store := common.NewInMemoryKVStore()

	// Add multiple upgrades
	require.NoError(t, WriteUpgradeLibraryUnchecked(store, 0, []byte("v0")))
	require.NoError(t, WriteUpgradeLibraryUnchecked(store, 1000, []byte("v1")))
	require.NoError(t, WriteUpgradeLibraryUnchecked(store, 5000, []byte("v2")))

	testCases := []struct {
		beforeSlot   uint32
		expectedSlot uint32
		expectedJSON []byte
		expectNil    bool
		description  string
	}{
		{0, 0, nil, true, "before slot 0"},
		{1, 0, []byte("v0"), false, "just after genesis"},
		{999, 0, []byte("v0"), false, "just before first upgrade"},
		{1000, 0, []byte("v0"), false, "exactly at first upgrade (finds previous)"},
		{1001, 1000, []byte("v1"), false, "just after first upgrade"},
		{4999, 1000, []byte("v1"), false, "just before second upgrade"},
		{5000, 1000, []byte("v1"), false, "exactly at second upgrade (finds previous)"},
		{5001, 5000, []byte("v2"), false, "just after second upgrade"},
		{10000, 5000, []byte("v2"), false, "far future"},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			slot, jsonData := findPreviousLibrary(store, tc.beforeSlot)
			if tc.expectNil {
				require.Nil(t, jsonData, "expected nil for slot %d", tc.beforeSlot)
			} else {
				require.NotNil(t, jsonData, "expected non-nil for slot %d", tc.beforeSlot)
				require.Equal(t, tc.expectedSlot, slot, "slot mismatch")
				require.Equal(t, tc.expectedJSON, jsonData, "JSON mismatch")
			}
		})
	}
}

// testLibraryJSON is a real compiled library: storeUpgrade writes with the checked
// writer, which parses both libraries to keep the genesis identity immutable.
func testLibraryJSON(t *testing.T) []byte {
	t.Helper()
	params, _ := ledger.GetTestingLedgerParams(1)
	return easyfl.ToJSON(ledger.LibraryFromParameters(params).Library, true, false)
}

// An upgrade the store does not hold is built on the library before it and stored.
func TestStoreUpgrade_NewUpgrade(t *testing.T) {
	store := common.NewInMemoryKVStore()
	genesis := testLibraryJSON(t)
	require.NoError(t, WriteUpgradeLibrary(store, 0, genesis))

	var builtOn []byte
	upgrade := &ledger.UpgradeDefinition{
		Slot: 1000,
		Build: func(prevJSON []byte) ([]byte, error) {
			builtOn = prevJSON
			return prevJSON, nil
		},
	}
	require.NoError(t, storeUpgrade(store, upgrade))
	require.Equal(t, genesis, builtOn, "built on the genesis library")
	jsonData, found := GetUpgradeLibraryDirect(store, 1000)
	require.True(t, found, "upgrade should be stored")
	require.Equal(t, genesis, jsonData)
}

// upgradeTestStore is a store with a genesis library whose genesis time is hoursAgo hours
// in the past, and upgrade 1 built on it: what a node holds after storing upgrade 1.
func upgradeTestStore(t *testing.T, hoursAgo int) (store global.Store, genesisJSON, upgradedJSON []byte) {
	t.Helper()
	params, _ := ledger.GetTestingLedgerParams(1)
	params.GenesisTimeUnix -= uint32(hoursAgo * 3600)
	genesisJSON = easyfl.ToJSON(ledger.LibraryFromParameters(params).Library, true, false)
	upgradedJSON, err := ledger.BuildUpgrade1Library(genesisJSON)
	require.NoError(t, err)
	store = common.NewInMemoryKVStore()
	require.NoError(t, WriteUpgradeLibrary(store, 0, genesisJSON))
	return store, genesisJSON, upgradedJSON
}

// stateWithUpgradeUTXO is a state holding the upgrade UTXO of slot, committing to libraryHash.
func stateWithUpgradeUTXO(t *testing.T, store global.Store, slot uint32, libraryHash, prevHash [32]byte) *Readable {
	t.Helper()
	genesisJSON, _ := GetUpgradeLibraryDirect(store, 0)
	// writing a state needs the library cache; the check under test does not
	ledger.MustInitLibraryCacheFromJSON(genesisJSON)
	lib, err := ledger.ParseLibraryFromJSON(genesisJSON, ledger.GetEmbeddedFunctionResolver)
	require.NoError(t, err)
	c := ledger.ConstantsFromLibrary(lib)
	root, err := WriteEmptyRootWithLedgerIdentity(ledger.NewLedgerIdentity(c.GenesisTimeUnix, c.Description), store)
	require.NoError(t, err)
	upd := MustNewUpdatable(store, root)
	muts := NewMutations()
	u := ledger.UpgradeUTXO(slot, libraryHash, prevHash, 0)
	muts.InsertAddOutputMutationRaw(u.ID, u.Output)
	require.NoError(t, upd.Update(muts, nil))
	rdr, err := NewReadable(store, upd.Root())
	require.NoError(t, err)
	return rdr
}

func hashOf(t *testing.T, libraryJSON []byte) [32]byte {
	h, err := libraryHashFromJSON(libraryJSON)
	require.NoError(t, err)
	return h
}

// An active upgrade is checked against the state, not the release: the stored library
// must have the hash the state's upgrade UTXO commits to, whether the release lists the
// upgrade or not.
func TestStoredUpgrade_CommittedInState(t *testing.T) {
	store, genesisJSON, upgradedJSON := upgradeTestStore(t, 0)
	require.NoError(t, WriteUpgradeLibrary(store, 1000, upgradedJSON))
	saved := ledger.Upgrades
	ledger.Upgrades = nil // the release no longer lists the upgrade
	defer func() { ledger.Upgrades = saved }()

	good := stateWithUpgradeUTXO(t, store, 1000, hashOf(t, upgradedJSON), hashOf(t, genesisJSON))
	require.NoError(t, checkStoredUpgrade(store, 1000, upgradedJSON, []*Readable{good}))

	// the state commits to another library: the stored one is not the network's
	other := stateWithUpgradeUTXO(t, store, 1000, hashOf(t, genesisJSON), hashOf(t, genesisJSON))
	require.ErrorContains(t, checkStoredUpgrade(store, 1000, upgradedJSON, []*Readable{other}), "the state commits to")
}

// A node that ran another build across a listed upgrade's slot has that build's library both
// stored and committed in its own state: the two agree, but they are a fork. The release
// lists the upgrade, so its build decides, and the node is refused (seen in the upgrade 2
// rehearsal, where the state check alone let such a node start).
func TestStoredUpgrade_ListedCommittedForeign(t *testing.T) {
	store, genesisJSON, _ := upgradeTestStore(t, 0)
	foreign := genesisJSON // what another build stored and committed at slot 1000
	require.NoError(t, WriteUpgradeLibrary(store, 1000, foreign))
	saved := ledger.Upgrades
	ledger.Upgrades = []*ledger.UpgradeDefinition{{Slot: 1000, Build: ledger.BuildUpgrade1Library}}
	defer func() { ledger.Upgrades = saved }()

	own := stateWithUpgradeUTXO(t, store, 1000, hashOf(t, foreign), hashOf(t, genesisJSON))
	require.ErrorContains(t, checkStoredUpgrade(store, 1000, foreign, []*Readable{own}), "this release builds")
}

// A listed upgrade not active yet must be the library the release builds.
func TestStoredUpgrade_ListedPending(t *testing.T) {
	store, genesisJSON, upgradedJSON := upgradeTestStore(t, 0)
	saved := ledger.Upgrades
	ledger.Upgrades = []*ledger.UpgradeDefinition{{Slot: 1000, Build: ledger.BuildUpgrade1Library}}
	defer func() { ledger.Upgrades = saved }()

	require.NoError(t, WriteUpgradeLibrary(store, 1000, upgradedJSON))
	require.NoError(t, checkStoredUpgrade(store, 1000, upgradedJSON, nil))

	// stored: the genesis library unchanged, as written by another build
	require.ErrorContains(t, checkStoredUpgrade(store, 1000, genesisJSON, nil), "this release builds")
}

// A stored upgrade neither active in the state nor listed: refused when its slot is still to
// come (a library from another build), accepted when the slot is past (a snapshot taken before
// it, checked by the network's roots when the node syncs across the slot).
func TestStoredUpgrade_Unlisted(t *testing.T) {
	saved := ledger.Upgrades
	ledger.Upgrades = nil
	defer func() { ledger.Upgrades = saved }()

	// genesis just now: slot 1000 is about three hours ahead
	store, _, upgradedJSON := upgradeTestStore(t, 0)
	require.NoError(t, WriteUpgradeLibrary(store, 1000, upgradedJSON))
	require.ErrorContains(t, checkStoredUpgrades(store), "still to come, which this release does not list")

	// genesis five hours ago: slot 1000 is past
	store, _, upgradedJSON = upgradeTestStore(t, 5)
	require.NoError(t, WriteUpgradeLibrary(store, 1000, upgradedJSON))
	require.NoError(t, checkStoredUpgrades(store))
}

// A state that already holds branches at or after the upgrade slot committed them under
// the previous rules: the upgrade is refused and the node does not start.
func TestStoreUpgrade_RefusedAfterTheSlot(t *testing.T) {
	store := common.NewInMemoryKVStore()
	require.NoError(t, WriteUpgradeLibrary(store, 0, testLibraryJSON(t)))
	batch := store.BatchedWriter()
	batch.Set([]byte{latestSlotDBPartition}, base.Slot2Bytes(1000))
	require.NoError(t, batch.Commit())

	upgrade := &ledger.UpgradeDefinition{Slot: 1000, Build: func(prevJSON []byte) ([]byte, error) { return prevJSON, nil }}
	err := storeUpgrade(store, upgrade)
	require.ErrorContains(t, err, "committed without the library upgrade of slot 1000")
	_, found := GetUpgradeLibraryDirect(store, 1000)
	require.False(t, found)

	// one slot later is still ahead of the state: stored
	upgrade.Slot = 1001
	require.NoError(t, storeUpgrade(store, upgrade))
}

// The checked writer keeps the genesis identity: an upgrade that changes the
// description is refused.
func TestStoreUpgrade_GenesisIdentityKept(t *testing.T) {
	store := common.NewInMemoryKVStore()
	// one set of parameters for both libraries: they take the genesis time from
	// the clock, and only the description may differ
	params, _ := ledger.GetTestingLedgerParams(1)
	require.NoError(t, WriteUpgradeLibrary(store, 0, easyfl.ToJSON(ledger.LibraryFromParameters(params).Library, true, false)))
	params.Description = "another ledger"
	other := easyfl.ToJSON(ledger.LibraryFromParameters(params).Library, true, false)

	upgrade := &ledger.UpgradeDefinition{Slot: 1000, Build: func([]byte) ([]byte, error) { return other, nil }}
	require.ErrorContains(t, storeUpgrade(store, upgrade), "description mismatch")
}
