package multistate

import (
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
	require.NoError(t, WriteUpgradeLibraryUnchecked(store, 1000, lib1000))   // 1000 > MinSlotsBetweenUpgrades from 0
	require.NoError(t, WriteUpgradeLibraryUnchecked(store, 5000, lib5000))   // 4000 > MinSlotsBetweenUpgrades from 1000

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
		querySlot        uint32
		expectedJSON     []byte
		expectedUpgSlot  uint32
		expectedFound    bool
		description      string
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
		beforeSlot       uint32
		expectedSlot     uint32
		expectedJSON     []byte
		expectNil        bool
		description      string
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

func TestStorePendingUpgrade_NewUpgrade(t *testing.T) {
	store := common.NewInMemoryKVStore()

	// Set up genesis library
	require.NoError(t, WriteUpgradeLibraryUnchecked(store, 0, []byte("genesis library")))

	// Create a pending upgrade definition
	pending := &ledger.UpgradeDefinition{
		Slot: 1000,
		Build: func(prevJSON []byte) ([]byte, error) {
			// Simple build that appends to previous
			return append(prevJSON, []byte(" + upgrade 1")...), nil
		},
	}

	// Store the pending upgrade
	storePendingUpgrade(store, pending)

	// Verify upgrade was stored
	jsonData, found := GetUpgradeLibraryDirect(store, 1000)
	require.True(t, found, "upgrade should be stored")
	require.Equal(t, []byte("genesis library + upgrade 1"), jsonData)
}

func TestStorePendingUpgrade_AlreadyExists(t *testing.T) {
	store := common.NewInMemoryKVStore()

	// Set up genesis and existing upgrade
	require.NoError(t, WriteUpgradeLibraryUnchecked(store, 0, []byte("genesis")))
	require.NoError(t, WriteUpgradeLibraryUnchecked(store, 1000, []byte("existing upgrade")))

	// Track calls
	buildCalled := false

	// Create pending upgrade for same slot
	pending := &ledger.UpgradeDefinition{
		Slot: 1000,
		Build: func(prevJSON []byte) ([]byte, error) {
			buildCalled = true
			return []byte("new upgrade"), nil
		},
	}

	// Store - should be a no-op since upgrade exists
	storePendingUpgrade(store, pending)

	// Build should NOT be called since upgrade already exists
	require.False(t, buildCalled, "Build should not be called when upgrade exists")

	// Original upgrade should still be there
	jsonData, found := GetUpgradeLibraryDirect(store, 1000)
	require.True(t, found)
	require.Equal(t, []byte("existing upgrade"), jsonData)
}

func TestStorePendingUpgrade_Simple(t *testing.T) {
	store := common.NewInMemoryKVStore()

	// Set up genesis library
	require.NoError(t, WriteUpgradeLibraryUnchecked(store, 0, []byte("genesis")))

	// Create pending upgrade
	pending := &ledger.UpgradeDefinition{
		Slot: 1000,
		Build: func(prevJSON []byte) ([]byte, error) {
			return []byte("new library"), nil
		},
	}

	// Store the upgrade
	storePendingUpgrade(store, pending)

	// Upgrade should be stored
	jsonData, found := GetUpgradeLibraryDirect(store, 1000)
	require.True(t, found)
	require.Equal(t, []byte("new library"), jsonData)
}
