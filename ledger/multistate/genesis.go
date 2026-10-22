package multistate

import (
	"encoding/hex"
	"fmt"
	"time"

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
// checking every stored library upgrade (checkStoredUpgrades) and storing every upgrade of
// ledger.Upgrades the store does not hold yet. A library other than the one the network
// runs would give the node other roots and transaction versions from its slot on. On error
// the cache is left uninitialized and the node must not start.
func InitLedgerFromStore(stateStore global.Store) error {
	if err := checkStoredUpgrades(stateStore); err != nil {
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

// checkStoredUpgrades checks each stored library upgrade against what vouches for it. Once
// active, the state does: its upgrade UTXO commits to the library hash, so the release
// need not list the upgrade any more. Before that, a listed upgrade must be the library the
// release builds. A stored upgrade neither committed in this state nor listed is accepted
// when its slot is past: the state predates it (a snapshot taken before the slot), and
// the network's roots check it when the node syncs across the slot. One whose slot is still
// to come is a library from another build, and is refused.
func checkStoredUpgrades(stateStore global.Store) error {
	type stored struct {
		slot uint32
		json []byte
	}
	var libs []stored
	IterateUpgradeLibraries(stateStore, func(slot uint32, libraryJSON []byte) bool {
		if slot != 0 {
			libs = append(libs, stored{slot, libraryJSON})
		}
		return true
	})
	if len(libs) == 0 {
		return nil
	}
	var readers []*Readable
	for _, rr := range FetchLatestRootRecords(stateStore) {
		rdr, err := NewReadable(stateStore, rr.Root)
		if err != nil {
			return fmt.Errorf("checking stored library upgrades: %w", err)
		}
		readers = append(readers, rdr)
	}
	for _, lib := range libs {
		if err := checkStoredUpgrade(stateStore, lib.slot, lib.json, readers); err != nil {
			return err
		}
	}
	return nil
}

func checkStoredUpgrade(stateStore global.Store, slot uint32, storedJSON []byte, latestStates []*Readable) error {
	storedHash, err := libraryHashFromJSON(storedJSON)
	if err != nil {
		return fmt.Errorf("the stored library upgrade of slot %d: %w", slot, err)
	}
	committed := false
	for _, rdr := range latestStates {
		utxoBytes, found := rdr.GetUTXO(base.UpgradeOutputID(slot))
		if !found {
			continue
		}
		o, err := txbuildercore.OutputFromBytes(utxoBytes)
		if err != nil {
			return fmt.Errorf("the upgrade UTXO of slot %d: %w", slot, err)
		}
		u, err := txbuildercore.ParseUpgradeUTXO(o)
		if err != nil {
			return fmt.Errorf("the upgrade UTXO of slot %d: %w", slot, err)
		}
		if u.LibraryHash != storedHash {
			return fmt.Errorf("the database holds a library upgrade at slot %d with hash %s, the state commits to %s. "+
				"Restore a snapshot from an up-to-date node, and restart",
				slot, hex.EncodeToString(storedHash[:]), hex.EncodeToString(u.LibraryHash[:]))
		}
		committed = true
	}
	if committed {
		return nil
	}
	for _, upgrade := range ledger.Upgrades {
		if upgrade.Slot == slot {
			return checkStoredAgainstBuild(stateStore, upgrade, storedHash)
		}
	}
	nowSlot, err := currentSlot(stateStore)
	if err != nil {
		return err
	}
	if slot > nowSlot {
		return fmt.Errorf("the database holds a library upgrade at slot %d, still to come, which this release does not list. "+
			"Restore a snapshot from a node running this release, and restart", slot)
	}
	return nil
}

// checkStoredAgainstBuild compares a stored upgrade library with the one the release builds
// on the stored library before it, by library hash, the value the upgrade UTXO commits to.
func checkStoredAgainstBuild(stateStore global.Store, upgrade *ledger.UpgradeDefinition, storedHash [32]byte) error {
	prevSlot, prevJSON := findPreviousLibrary(stateStore, upgrade.Slot)
	if prevJSON == nil {
		return fmt.Errorf("no library found before the upgrade of slot %d", upgrade.Slot)
	}
	builtJSON, err := upgrade.Build(prevJSON)
	if err != nil {
		return fmt.Errorf("failed to build the library upgrade of slot %d (on slot %d): %w", upgrade.Slot, prevSlot, err)
	}
	built, err := libraryHashFromJSON(builtJSON)
	if err != nil {
		return fmt.Errorf("the library upgrade of slot %d built by this release: %w", upgrade.Slot, err)
	}
	if storedHash != built {
		return fmt.Errorf("the database holds a library upgrade at slot %d with hash %s, this release builds %s. "+
			"Restore a snapshot from a node running this release, and restart",
			upgrade.Slot, hex.EncodeToString(storedHash[:]), hex.EncodeToString(built[:]))
	}
	return nil
}

// currentSlot is the slot of the wall clock by the stored genesis library: the library
// cache, which ledger.TimeNow reads, is not initialized yet.
func currentSlot(stateStore global.Store) (uint32, error) {
	genesisJSON, found := GetUpgradeLibraryDirect(stateStore, 0)
	if !found {
		return 0, fmt.Errorf("genesis library (slot 0) not found")
	}
	lib, err := ledger.ParseLibraryFromJSON(genesisJSON, ledger.GetEmbeddedFunctionResolver)
	if err != nil {
		return 0, fmt.Errorf("genesis library: %w", err)
	}
	return ledger.ConstantsFromLibrary(lib).LedgerTimeFromClockTime(time.Now()).Slot, nil
}

// storeUpgrade builds the library of a listed upgrade on the stored library before it and
// stores it; a no-op when the store holds it (checkStoredUpgrades has checked it). A state
// that already holds branches at or after the upgrade slot committed them under the previous
// rules, so the upgrade can no longer be applied to it: the operator restores a snapshot.
func storeUpgrade(stateStore global.Store, upgrade *ledger.UpgradeDefinition) error {
	if _, found := GetUpgradeLibraryDirect(stateStore, upgrade.Slot); found {
		return nil
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
