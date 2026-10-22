package multistate

import (
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set256"
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
// Also stores the library JSON in the upgrade DB partition at slot 0.
// Returns root commitment to the genesis ledger state and genesis chainID.
func InitStateStoreFromGlobals(store global.Store) (base.ChainID, common.VCommitment) {
	lib := ledger.L(0)
	// Create minimal identity from constants
	identity := ledger.NewLedgerIdentity(lib.GenesisTimeUnix, lib.Description)
	emptyRoot, err := WriteEmptyRootWithLedgerIdentity(identity, store)
	util.AssertNoError(err)

	// Store library JSON in upgrade DB partition at slot 0
	libraryJSON := lib.DefinitionsJSON()
	err = WriteUpgradeLibrary(store, 0, libraryJSON)
	util.AssertNoError(err)

	genesisAddr := ledger.SigLockFromED25519PublicKey(lib.GenesisControllerPublicKey())

	initialSupply := lib.InitialSupply
	// The mine chain dust C is carved out of the genesis output so total
	// genesis supply stays constInitialSupply. R_init is a separate mintable
	// ceiling carried in the mine output's lock, not part of genesis supply.
	gout := ledger.GenesisOutput(initialSupply-1-ledger.GenesisMineChainDust, genesisAddr)
	gStemOut := ledger.GenesisStemOutput()
	// Controller mote output ensures the controller can always create transactions
	dustOut := ledger.GenesisControllerDustOutput(genesisAddr)
	// Fair-launch mine chain output (index 3)
	mineOut := ledger.GenesisMineChainOutput()

	// Create upgrade commitment UTXO for slot 0
	// For slot 0, prevHash is the base library hash, prevSlot is MaxSlot
	libraryHash := lib.LibraryHash()
	prevLibraryHash := ledger.BaseLibraryHash()
	upgradeOut := ledger.UpgradeUTXO(0, libraryHash, prevLibraryHash, base.MaxSlot)

	updatable := MustNewUpdatable(store, emptyRoot)
	updatable.MustUpdate(genesisUpdateMutations(&gout.OutputWithID, gStemOut, dustOut, &mineOut.OutputWithID, upgradeOut), &RootRecordParams{
		StemOutputID:      gStemOut.ID,
		SeqID:             gout.ChainID,
		SlotInflation:     initialSupply, // updateTrie invariant: produced == consumed + slotInflation
		WriteEarliestSlot: true,
	})
	return gout.ChainID, updatable.Root()
}

func genesisUpdateMutations(genesisOut, genesisStemOut, dustOut, mineOut, upgradeOut *ledger.OutputWithID) *Mutations {
	ret := NewMutations()
	ret.InsertAddOutputMutation(genesisOut.ID, genesisOut.Output)
	ret.InsertAddOutputMutation(genesisStemOut.ID, genesisStemOut.Output)
	ret.InsertAddOutputMutation(dustOut.ID, dustOut.Output)
	ret.InsertAddOutputMutation(mineOut.ID, mineOut.Output)
	// Use raw clone for upgrade UTXO since it doesn't have a standard lock
	ret.InsertAddOutputMutationRaw(upgradeOut.ID, upgradeOut.Output)
	var unspent set256.Set256
	unspent.InsertRange(0, 4) // 4 real genesis outputs: indices 0-3 (mine chain at 3)
	ret.InsertAddTxMutation(base.GenesisTransactionID(), unspent)
	return ret
}

// ScanGenesisState scans the genesis state and returns constants and root commitment.
// It loads the library from the upgrade DB partition (slot 0).
func ScanGenesisState(stateStore global.Store) (*txbuildercore.Constants, common.VCommitment, error) {
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
	jsonData, found := GetUpgradeLibraryDirect(stateStore, 0)
	if !found {
		return nil, nil, fmt.Errorf("ScanGenesisState: library not found in upgrade partition at slot 0")
	}
	lib, err := ledger.ParseLibraryFromJSON(jsonData, ledger.GetEmbeddedFunctionResolver)
	if err != nil {
		return nil, nil, fmt.Errorf("ScanGenesisState: failed to parse library: %w", err)
	}

	genesisOid := base.GenesisOutputID()
	out, err := rdr.GetOutputErr(genesisOid)
	if err != nil {
		return nil, nil, fmt.Errorf("GetOutputErr(%s): %w", genesisOid.StringShort(), err)
	}
	constants := ledger.ConstantsFromLibrary(lib)
	// Genesis output has initialSupply - 1 - mineChainDust tokens; the remaining
	// 1 token is in the controller mote output and mineChainDust is in the mine
	// chain output (index 3).
	expectedGenesis := constants.InitialSupply - 1 - ledger.GenesisMineChainDust
	if out.TokenBalance() != expectedGenesis {
		return nil, nil, fmt.Errorf("different amounts in genesis output and state definitions: got %d, expected %d",
			out.TokenBalance(), expectedGenesis)
	}
	return constants, branchData.Root, nil
}

// InitLedgerFromStore initializes the ledger library cache from the state store, after
// storing every upgrade of ledger.Upgrades the store does not hold yet. The stored
// upgrades must be exactly those the binary lists, each with the library the binary
// builds: a library written by another build, restored from its snapshot, or kept for a
// slot since moved would give the node other roots and transaction versions from its
// slot on. On error the cache is left uninitialized and the node must not start.
func InitLedgerFromStore(stateStore global.Store) error {
	if err := checkNoUnlistedUpgrades(stateStore); err != nil {
		return err
	}
	for _, upgrade := range ledger.Upgrades {
		if err := storeUpgrade(stateStore, upgrade); err != nil {
			return err
		}
	}
	ledger.MustInitLibraryCache(stateStore)
	return nil
}

// checkNoUnlistedUpgrades refuses a store holding a library upgrade the binary does not list.
func checkNoUnlistedUpgrades(stateStore global.Store) error {
	listed := make(map[uint32]bool)
	for _, upgrade := range ledger.Upgrades {
		listed[upgrade.Slot] = true
	}
	var err error
	IterateUpgradeLibraries(stateStore, func(slot uint32, _ []byte) bool {
		if slot != 0 && !listed[slot] {
			err = fmt.Errorf("the database holds a library upgrade at slot %d which this release does not list. "+
				"Restore a snapshot from a node running this release, and restart", slot)
			return false
		}
		return true
	})
	return err
}

// storeUpgrade builds the library of an upgrade on the stored library before it, and
// stores it, or checks that the stored one is the same. A state that already holds
// branches at or after the upgrade slot committed them under the previous rules, so the
// upgrade can no longer be applied to it: the operator restores a snapshot instead.
func storeUpgrade(stateStore global.Store, upgrade *ledger.UpgradeDefinition) error {
	if storedJSON, found := GetUpgradeLibraryDirect(stateStore, upgrade.Slot); found {
		return checkStoredUpgrade(stateStore, upgrade, storedJSON)
	}
	if latest := FetchLatestCommittedSlot(stateStore); latest >= upgrade.Slot {
		return fmt.Errorf("the state holds branches up to slot %d, committed without the library upgrade of slot %d. "+
			"Restore a snapshot taken after slot %d (it carries the upgrade) or one taken before it, and restart",
			latest, upgrade.Slot, upgrade.Slot)
	}
	prevSlot, prevJSON := findPreviousLibrary(stateStore, upgrade.Slot)
	if prevJSON == nil {
		return fmt.Errorf("no library found before the upgrade of slot %d", upgrade.Slot)
	}
	newJSON, err := upgrade.Build(prevJSON)
	if err != nil {
		return fmt.Errorf("failed to build the library upgrade of slot %d (on slot %d): %w", upgrade.Slot, prevSlot, err)
	}
	if err = WriteUpgradeLibrary(stateStore, upgrade.Slot, newJSON); err != nil {
		return fmt.Errorf("failed to store the library upgrade of slot %d: %w", upgrade.Slot, err)
	}
	return nil
}

// checkStoredUpgrade compares a stored upgrade library with the one the binary builds on
// the stored library before it, by library hash, the value the upgrade UTXO commits to.
func checkStoredUpgrade(stateStore global.Store, upgrade *ledger.UpgradeDefinition, storedJSON []byte) error {
	prevSlot, prevJSON := findPreviousLibrary(stateStore, upgrade.Slot)
	if prevJSON == nil {
		return fmt.Errorf("no library found before the upgrade of slot %d", upgrade.Slot)
	}
	builtJSON, err := upgrade.Build(prevJSON)
	if err != nil {
		return fmt.Errorf("failed to build the library upgrade of slot %d (on slot %d): %w", upgrade.Slot, prevSlot, err)
	}
	stored, err := libraryHashFromJSON(storedJSON)
	if err != nil {
		return fmt.Errorf("the stored library upgrade of slot %d: %w", upgrade.Slot, err)
	}
	built, err := libraryHashFromJSON(builtJSON)
	if err != nil {
		return fmt.Errorf("the library upgrade of slot %d built by this release: %w", upgrade.Slot, err)
	}
	if stored != built {
		return fmt.Errorf("the database holds a library upgrade at slot %d with hash %s, this release builds %s. "+
			"Restore a snapshot from a node running this release, and restart",
			upgrade.Slot, hex.EncodeToString(stored[:]), hex.EncodeToString(built[:]))
	}
	return nil
}

func libraryHashFromJSON(libraryJSON []byte) ([32]byte, error) {
	lib, err := ledger.ParseLibraryFromJSON(libraryJSON, ledger.GetEmbeddedFunctionResolver)
	if err != nil {
		return [32]byte{}, err
	}
	return lib.LibraryHash(), nil
}

// findPreviousLibrary finds the most recent library before the given slot.
// Returns the slot and JSON data of the previous library.
func findPreviousLibrary(stateStore global.Store, beforeSlot uint32) (uint32, []byte) {
	var foundSlot uint32
	var foundJSON []byte
	found := false

	IterateUpgradeLibraries(stateStore, func(slot uint32, jsonData []byte) bool {
		if slot < beforeSlot {
			if !found || slot > foundSlot {
				foundSlot = slot
				foundJSON = jsonData
				found = true
			}
		}
		return true
	})

	if !found {
		return 0, nil
	}
	return foundSlot, foundJSON
}
