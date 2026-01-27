package multistate

import (
	"fmt"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
	"github.com/lunfardo314/unitrie/immutable"
)

// WriteEmptyRootWithLedgerIdentity writes minimal ledger identity data as value of the empty key nil.
// The identity contains only genesis time and description (truly immutable data).
// Returns root of the empty trie.
func WriteEmptyRootWithLedgerIdentity(identity *ledger.LedgerIdentity, store global.Store) (common.VCommitment, error) {
	batch := store.BatchedWriter()
	emptyRoot := immutable.MustInitRoot(batch, ledger.CommitmentModel, identity.Bytes())
	if err := batch.Commit(); err != nil {
		return nil, err
	}
	return emptyRoot, nil
}

// InitStateStoreFromGlobals initializes origin ledger state in the empty store.
// Creates:
// - Initial supply output (index 0)
// - Stem output (index 1)
// - Upgrade commitment UTXO (synthetic, index 255)
// Also stores the library YAML in the upgrade DB partition at slot 0.
// Returns root commitment to the genesis ledger state and genesis chainID.
func InitStateStoreFromGlobals(store global.Store) (base.ChainID, common.VCommitment) {
	lib := ledger.L(0)
	// Create minimal identity from constants
	identity := ledger.NewLedgerIdentity(lib.GenesisTimeUnix, lib.Description)
	emptyRoot, err := WriteEmptyRootWithLedgerIdentity(identity, store)
	util.AssertNoError(err)

	// Store library YAML in upgrade DB partition at slot 0
	libraryYAML := lib.DefinitionsYAML()
	err = WriteUpgradeLibrary(store, 0, libraryYAML)
	util.AssertNoError(err)

	genesisAddr := ledger.AddressED25519FromPublicKey(lib.GenesisControllerPublicKey)

	initialSupply := lib.InitialSupply
	gout := ledger.GenesisOutput(initialSupply-1, genesisAddr)
	gStemOut := ledger.GenesisStemOutput()
	// Controller dust output ensures the controller can always create transactions
	dustOut := ledger.GenesisControllerDustOutput(genesisAddr)

	// Create upgrade commitment UTXO for slot 0
	// For slot 0, prevHash is the base library hash, prevSlot is MaxSlot
	libraryHash := lib.LibraryHash()
	prevLibraryHash := ledger.BaseLibraryHash()
	upgradeOut := ledger.UpgradeUTXO(0, libraryHash, prevLibraryHash, base.MaxSlot)

	updatable := MustNewUpdatable(store, emptyRoot)
	updatable.MustUpdate(genesisUpdateMutations(&gout.OutputWithID, gStemOut, dustOut, upgradeOut), &RootRecordParams{
		StemOutputID:      gStemOut.ID,
		SeqID:             gout.ChainID,
		CoverageDelta:     initialSupply,
		SlotInflation:     initialSupply,
		Supply:            initialSupply,
		WriteEarliestSlot: true,
	})
	return gout.ChainID, updatable.Root()
}

func genesisUpdateMutations(genesisOut, genesisStemOut, dustOut, upgradeOut *ledger.OutputWithID) *Mutations {
	ret := NewMutations()
	ret.InsertAddOutputMutation(genesisOut.ID, genesisOut.Output)
	ret.InsertAddOutputMutation(genesisStemOut.ID, genesisStemOut.Output)
	ret.InsertAddOutputMutation(dustOut.ID, dustOut.Output)
	// Use raw clone for upgrade UTXO since it doesn't have a standard lock
	ret.InsertAddOutputMutationRaw(upgradeOut.ID, upgradeOut.Output)
	ret.InsertAddTxMutation(base.GenesisTransactionID(), genesisOut.ID.Slot(), 1)
	return ret
}

// ScanGenesisState scans the genesis state and returns constants and root commitment.
// It loads the library from the upgrade DB partition (slot 0).
func ScanGenesisState(stateStore global.Store) (*ledger.Constants, common.VCommitment, error) {
	var genesisRootRecord RootRecord

	// expecting a single branch in the genesis state
	fetched, moreThan1 := false, false
	IterateRootRecords(stateStore, func(_ base.TransactionID, rootData RootRecord) bool {
		if fetched {
			moreThan1 = true
			return false
		}
		genesisRootRecord = rootData
		fetched = true
		return true
	})
	if !fetched || moreThan1 {
		return nil, nil, fmt.Errorf("ScanGenesisState: exactly 1 branch expected. Not a genesis state")
	}

	branchData := FetchBranchDataByRoot(stateStore, genesisRootRecord)
	rdr := MustNewSugaredReadableState(stateStore, branchData.Root)

	// Load library from upgrade DB partition (slot 0)
	yamlData, found := GetUpgradeLibraryDirect(stateStore, 0)
	if !found {
		return nil, nil, fmt.Errorf("ScanGenesisState: library not found in upgrade partition at slot 0")
	}
	lib, err := ledger.ParseLibraryFromYAML(yamlData, ledger.GetEmbeddedFunctionResolverUpgrade0)
	if err != nil {
		return nil, nil, fmt.Errorf("ScanGenesisState: failed to parse library: %w", err)
	}

	genesisOid := base.GenesisOutputID()
	out, err := rdr.GetOutputErr(genesisOid)
	if err != nil {
		return nil, nil, fmt.Errorf("GetOutputErr(%s): %w", genesisOid.StringShort(), err)
	}
	constants := ledger.ConstantsFromLibrary(lib)
	// Genesis output has initialSupply - 1 tokens; the remaining 1 token is in the controller dust output
	if out.TokenBalance() != constants.InitialSupply-1 {
		return nil, nil, fmt.Errorf("different amounts in genesis output and state definitions: got %d, expected %d",
			out.TokenBalance(), constants.InitialSupply-1)
	}
	return constants, branchData.Root, nil
}

// InitLedgerFromStore initializes the ledger library cache from the state store.
// It loads libraries from the upgrade DB partition and handles pending upgrades.
func InitLedgerFromStore(stateStore global.Store) {
	// Handle pending upgrade if one exists
	if ledger.PendingUpgrade != nil {
		storePendingUpgrade(stateStore, ledger.PendingUpgrade)
	}

	// Initialize the library cache with the state store
	ledger.MustInitLibraryCache(stateStore)
}

// storePendingUpgrade stores the library for a pending upgrade in the DB partition.
// If the upgrade already exists in the DB partition, this is a no-op (idempotent).
func storePendingUpgrade(stateStore global.Store, pending *ledger.UpgradeDefinition) {
	// Check if upgrade already exists in DB partition
	if _, found := GetUpgradeLibraryDirect(stateStore, pending.Slot); found {
		// Upgrade already stored, nothing more to do
		return
	}

	// Find the previous library to build upon
	prevSlot, prevYAML := findPreviousLibrary(stateStore, pending.Slot)
	util.Assertf(prevYAML != nil, "no previous library found for pending upgrade at slot %d", pending.Slot)

	// Build the upgraded library
	newYAML, err := pending.Build(prevYAML)
	util.Assertf(err == nil, "failed to build pending upgrade library at slot %d (based on slot %d): %v",
		pending.Slot, prevSlot, err)

	// Store the upgraded library in the DB partition
	// Using WriteUpgradeLibraryUnchecked because:
	// 1. The Build function is trusted code from the upgrade definition
	// 2. It builds on top of the previous library, preserving identity
	// 3. Identity validation is still enforced for external inputs via WriteUpgradeLibrary
	err = WriteUpgradeLibraryUnchecked(stateStore, pending.Slot, newYAML)
	util.AssertNoError(err)
}

// findPreviousLibrary finds the most recent library before the given slot.
// Returns the slot and YAML data of the previous library.
func findPreviousLibrary(stateStore global.Store, beforeSlot uint32) (uint32, []byte) {
	var foundSlot uint32
	var foundYAML []byte
	found := false

	IterateUpgradeLibraries(stateStore, func(slot uint32, yamlData []byte) bool {
		if slot < beforeSlot {
			if !found || slot > foundSlot {
				foundSlot = slot
				foundYAML = yamlData
				found = true
			}
		}
		return true
	})

	if !found {
		return 0, nil
	}
	return foundSlot, foundYAML
}
