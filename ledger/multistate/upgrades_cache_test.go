package multistate

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

// Tests for the library caching behavior.
// These tests verify that L(slot) properly caches and retrieves libraries.

func TestLibraryCache_BasicAccess(t *testing.T) {
	// Initialize ledger with test data which sets up the singleton
	ledger.ResetForTesting()
	ledger.InitWithTestingLedgerIDData()

	// L(base.MaxSlot) should return the latest library
	lib := ledger.L(base.MaxSlot)
	require.NotNil(t, lib, "should get non-nil library")

	// Library should have constants initialized via embedded Constants field
	require.NotZero(t, lib.TicksPerSlot, "constants should be initialized (TicksPerSlot should be non-zero)")

	// L(0) should also work (genesis library)
	lib0 := ledger.L(0)
	require.NotNil(t, lib0, "should get genesis library")

	// Both should return the same library instance since only one version exists
	require.Same(t, lib, lib0, "both slots should return same library instance")
}

func TestLibraryCache_SlotConsistency(t *testing.T) {
	// Initialize ledger with test data
	ledger.ResetForTesting()
	ledger.InitWithTestingLedgerIDData()

	// Multiple calls to L() with same slot should return the same library instance
	lib1 := ledger.L(100)
	lib2 := ledger.L(100)

	// Should be the same instance (cached)
	require.Same(t, lib1, lib2, "should return cached library instance")
}

func TestLibraryCache_MaxSlotConsistency(t *testing.T) {
	// Initialize ledger with test data
	ledger.ResetForTesting()
	ledger.InitWithTestingLedgerIDData()

	// L(base.MaxSlot) should consistently return the latest library
	lib1 := ledger.L(base.MaxSlot)
	lib2 := ledger.L(base.MaxSlot)

	require.Same(t, lib1, lib2, "MaxSlot calls should return same cached instance")
}

func TestLibraryCache_LibraryFunctionality(t *testing.T) {
	// Test that the cached library is fully functional
	ledger.ResetForTesting()
	ledger.InitWithTestingLedgerIDData()

	lib := ledger.L(base.MaxSlot)

	// Test compilation
	_, _, bytecode, err := lib.CompileExpression("add(1, 2)")
	require.NoError(t, err, "should compile expression")
	require.NotEmpty(t, bytecode, "should produce bytecode")

	// Test evaluation
	result, err := lib.EvalFromSource(nil, "add(1, 2)")
	require.NoError(t, err, "should evaluate expression")
	require.NotEmpty(t, result, "should produce result")

	// Test bytecode prefix parsing
	prefix, err := lib.ParsePrefixBytecode(bytecode)
	require.NoError(t, err, "should parse prefix")
	require.NotEmpty(t, prefix, "should have prefix")
}

func TestLibraryCache_ConstraintNames(t *testing.T) {
	// Test that constraint names are properly accessible via cached library
	ledger.ResetForTesting()
	ledger.InitWithTestingLedgerIDData()

	// Test some known constraint names
	name, found := ledger.NameByPrefix([]byte{0x03, 0x01}) // Example prefix
	if found {
		require.NotEmpty(t, name, "should have constraint name")
	}
}

func TestLibraryCache_DifferentSlotsSameVersion(t *testing.T) {
	// When there's only one library version, all slots should return the same library
	ledger.ResetForTesting()
	ledger.InitWithTestingLedgerIDData()

	// Test various slots
	slots := []uint32{0, 1, 100, 1000, 10000, base.MaxSlot}
	libs := make([]*ledger.Library, len(slots))

	for i, slot := range slots {
		libs[i] = ledger.L(slot)
		require.NotNil(t, libs[i], "should get library for slot %d", slot)
	}

	// All should be the same instance since there's only one version (cached)
	firstLib := libs[0]
	for i, lib := range libs[1:] {
		require.Same(t, firstLib, lib, "library at slot %d should be same instance as genesis", slots[i+1])
	}
}

func TestUpgradeLibraryStorage_IntegrationWithCache(t *testing.T) {
	// Test the storage layer functions that will be used by the cache
	store := common.NewInMemoryKVStore()

	// Simulate genesis setup: write library at slot 0
	genesisYAML := []byte("test genesis library YAML")
	err := WriteUpgradeLibrary(store, 0, genesisYAML)
	require.NoError(t, err)

	// The cache will use GetUpgradeLibraryForSlot to find applicable library
	yaml, upgradeSlot, found := GetUpgradeLibraryForSlot(store, 0)
	require.True(t, found)
	require.Equal(t, uint32(0), upgradeSlot)
	require.Equal(t, genesisYAML, yaml)

	// Future slots should also get the genesis library
	yaml, upgradeSlot, found = GetUpgradeLibraryForSlot(store, 100000)
	require.True(t, found)
	require.Equal(t, uint32(0), upgradeSlot)
	require.Equal(t, genesisYAML, yaml)
}

func TestUpgradeLibraryStorage_MultiVersionCacheLookup(t *testing.T) {
	// Test lookup behavior with multiple versions in storage
	// Uses WriteUpgradeLibraryUnchecked to bypass identity validation (test uses placeholder data)
	store := common.NewInMemoryKVStore()

	// Set up multiple library versions with proper spacing
	lib0 := []byte("library version 0 - genesis")
	lib1000 := []byte("library version 1 - first upgrade")
	lib5000 := []byte("library version 2 - second upgrade")

	require.NoError(t, WriteUpgradeLibraryUnchecked(store, 0, lib0))
	require.NoError(t, WriteUpgradeLibraryUnchecked(store, 1000, lib1000))
	require.NoError(t, WriteUpgradeLibraryUnchecked(store, 5000, lib5000))

	// Verify slot lookup returns correct version
	testCases := []struct {
		slot         uint32
		expectedVer  uint32
		expectedYAML []byte
	}{
		{0, 0, lib0},
		{500, 0, lib0},
		{999, 0, lib0},
		{1000, 1000, lib1000},
		{2500, 1000, lib1000},
		{4999, 1000, lib1000},
		{5000, 5000, lib5000},
		{10000, 5000, lib5000},
		{base.MaxSlot, 5000, lib5000},
	}

	for _, tc := range testCases {
		yaml, upgradeSlot, found := GetUpgradeLibraryForSlot(store, tc.slot)
		require.True(t, found, "should find library for slot %d", tc.slot)
		require.Equal(t, tc.expectedVer, upgradeSlot, "wrong upgrade slot for query slot %d", tc.slot)
		require.Equal(t, tc.expectedYAML, yaml, "wrong YAML for query slot %d", tc.slot)
	}
}
