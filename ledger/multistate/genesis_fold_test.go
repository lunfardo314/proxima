package multistate

import (
	"fmt"
	"testing"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

// Tests for genesis libraries that fold the listed upgrades: a new ledger starts with the
// current definitions, its nodes skip the folded upgrades, and a node checks at start that the
// release still builds its genesis library from the base EasyFL library.

// foldTestUpgrades are the release's upgrades 1 to 5 at the slots of the take 1 network.
func foldTestUpgrades() []*ledger.UpgradeDefinition {
	return []*ledger.UpgradeDefinition{
		{Slot: ledger.Upgrade1Slot, Build: ledger.BuildUpgrade1Library},
		{Slot: ledger.Upgrade2Slot, Build: ledger.BuildUpgrade2Library},
		{Slot: ledger.Upgrade3Slot, Build: ledger.BuildUpgrade3Library},
		{Slot: ledger.Upgrade4Slot, Build: ledger.BuildUpgrade4Library},
		{Slot: ledger.Upgrade5Slot, Build: ledger.BuildUpgrade5Library},
	}
}

// foldedGenesisJSON is a test genesis library folding the given upgrades.
func foldedGenesisJSON(t *testing.T, upgrades []*ledger.UpgradeDefinition) []byte {
	t.Helper()
	params, _ := ledger.GetTestingLedgerParams(1)
	params.FoldUpgrades = upgrades
	return ledger.LibraryJSONFromParameters(params, true)
}

func parseLib(t *testing.T, libraryJSON []byte) *easyfl.Library[*ledger.EvalContext] {
	t.Helper()
	lib, err := ledger.ParseLibraryFromJSON(libraryJSON, ledger.GetEmbeddedFunctionResolver)
	require.NoError(t, err)
	return lib
}

// A folded genesis carries the definitions of every folded upgrade and records their number;
// a genesis folding none is the library of upgrade 0 unchanged, as the take 1 genesis is.
func TestGenesisFoldsUpgrades(t *testing.T) {
	folded := parseLib(t, foldedGenesisJSON(t, foldTestUpgrades()))
	require.Equal(t, 5, ledger.FoldedUpgrades(folded))
	_, err := folded.FunctionByName("_endorsementIsSequencerTx") // upgrade 4
	require.NoError(t, err)
	_, err = folded.FunctionByName("_sequencerKeyFixed") // upgrade 5
	require.NoError(t, err)

	params, _ := ledger.GetTestingLedgerParams(1)
	params.FoldUpgrades = nil
	plain := parseLib(t, ledger.LibraryJSONFromParameters(params, true))
	require.Zero(t, ledger.FoldedUpgrades(plain))
	require.Equal(t, ledger.LibraryFromParameters(params).Library.LibraryHash(), plain.LibraryHash(),
		"a genesis folding nothing is the library of upgrade 0")
}

// The start-up check rebuilds a stored genesis library from the base EasyFL library and the
// genesis constants it holds, folded or not, and arrives at the same hash.
func TestRebuildGenesisLibrary(t *testing.T) {
	for _, n := range []int{0, 3, 5} {
		t.Run(fmt.Sprintf("folds %d", n), func(t *testing.T) {
			upgrades := foldTestUpgrades()
			stored := foldedGenesisJSON(t, upgrades[:n])
			rebuilt, err := ledger.RebuildGenesisLibraryJSON(stored, upgrades)
			require.NoError(t, err)
			require.Equal(t, parseLib(t, stored).LibraryHash(), parseLib(t, rebuilt).LibraryHash())
		})
	}
	// a release that lists fewer upgrades than the genesis folds cannot rebuild it
	_, err := ledger.RebuildGenesisLibraryJSON(foldedGenesisJSON(t, foldTestUpgrades()), foldTestUpgrades()[:2])
	require.ErrorContains(t, err, "folds 5 upgrades, this release lists 2")
}

// initFromGenesis runs the node's start-up path on a store holding only the given genesis
// library, with the release listing upgrades, and returns the warnings.
func initFromGenesis(t *testing.T, genesisJSON []byte, upgrades []*ledger.UpgradeDefinition) ([]string, error, *common.InMemoryKVStore) {
	t.Helper()
	saved := ledger.Upgrades
	ledger.Upgrades = upgrades
	t.Cleanup(func() {
		ledger.Upgrades = saved
		ledger.ResetForTesting()
	})
	store := common.NewInMemoryKVStore()
	require.NoError(t, WriteUpgradeLibrary(store, 0, genesisJSON))
	var warnings []string
	ledger.ResetForTesting()
	err := InitLedgerFromStore(store, func(format string, args ...any) {
		warnings = append(warnings, fmt.Sprintf(format, args...))
	})
	return warnings, err, store
}

// A node on a genesis that folds every listed upgrade stores none of them: they are part of
// its genesis library, and its slots belong to another network.
func TestInitLedgerFromStore_FoldedGenesis(t *testing.T) {
	warnings, err, store := initFromGenesis(t, foldedGenesisJSON(t, foldTestUpgrades()), foldTestUpgrades())
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.Equal(t, 1, CountUpgradeLibraries(store), "only the genesis library is stored")
	require.Equal(t, 5, ledger.L(ledger.Upgrade5Slot).FoldedUpgrades())
}

// A genesis folding the first upgrades leaves the later ones to their slots, as for a ledger
// created by a release that listed fewer.
func TestInitLedgerFromStore_PartlyFoldedGenesis(t *testing.T) {
	upgrades := foldTestUpgrades()
	_, err, store := initFromGenesis(t, foldedGenesisJSON(t, upgrades[:3]), upgrades)
	require.NoError(t, err)
	_, found := GetUpgradeLibraryDirect(store, ledger.Upgrade3Slot)
	require.False(t, found, "upgrade 3 is folded")
	_, found = GetUpgradeLibraryDirect(store, ledger.Upgrade4Slot)
	require.True(t, found, "upgrade 4 is stored at its slot")
}

// A genesis folding more upgrades than the release lists needs a newer release.
func TestInitLedgerFromStore_GenesisAheadOfRelease(t *testing.T) {
	_, err, _ := initFromGenesis(t, foldedGenesisJSON(t, foldTestUpgrades()), foldTestUpgrades()[:4])
	require.ErrorContains(t, err, "a newer release is needed")
}

// When the release no longer builds the stored genesis library, here because the builder of a
// folded upgrade changed, the node is warned and starts on the stored library.
func TestInitLedgerFromStore_GenesisRebuildMismatchWarns(t *testing.T) {
	genesis := foldedGenesisJSON(t, foldTestUpgrades()[:1])
	drifted := []*ledger.UpgradeDefinition{{Slot: ledger.Upgrade1Slot, Build: ledger.BuildUpgrade2Library}}
	warnings, err, _ := initFromGenesis(t, genesis, drifted)
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0], "this release builds")
}
