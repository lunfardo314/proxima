package multistate

import (
	"encoding/hex"
	"fmt"
	"sort"
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
// checking the stored libraries against the ones this release builds (checkStoredUpgrades,
// checkGenesisRebuild) and storing every upgrade of ledger.Upgrades the store does not hold
// yet. The upgrades the genesis library folds are part of it and are skipped. A library other
// than the one the network runs would give the node other roots and transaction versions from
// its slot on. warn, when given, receives what the node may run with but should be told about.
// On error the cache is left uninitialized and the node must not start.
func InitLedgerFromStore(stateStore global.Store, warn ...func(format string, args ...any)) error {
	warnf := func(string, ...any) {}
	if len(warn) > 0 {
		warnf = warn[0]
	}
	genesisJSON, found := GetUpgradeLibraryDirect(stateStore, 0)
	if !found {
		return fmt.Errorf("genesis library (slot 0) not found")
	}
	genesis, err := ledger.ParseLibraryFromJSON(genesisJSON, ledger.GetEmbeddedFunctionResolver)
	if err != nil {
		return fmt.Errorf("genesis library: %w", err)
	}
	folded := ledger.FoldedUpgrades(genesis)
	if folded > len(ledger.Upgrades) {
		return fmt.Errorf("the genesis library carries %d library upgrades, this release lists %d: a newer release is needed",
			folded, len(ledger.Upgrades))
	}
	upgrades := ledger.Upgrades[folded:]
	if err = replacePendingUpgrades(stateStore, upgrades, warnf); err != nil {
		return err
	}
	if err = checkStoredUpgrades(stateStore, upgrades, warnf); err != nil {
		return err
	}
	checkGenesisRebuild(genesisJSON, genesis.LibraryHash(), warnf)
	for _, upgrade := range upgrades {
		if err = storeUpgrade(stateStore, upgrade); err != nil {
			return err
		}
	}
	ledger.MustInitLibraryCache(stateStore)
	return nil
}

// checkGenesisRebuild warns when this release no longer builds the stored genesis library from
// the base EasyFL library and the genesis constants. The node runs on the stored library either
// way; what differs is the genesis this release would give a new ledger.
func checkGenesisRebuild(genesisJSON []byte, genesisHash [32]byte, warnf func(string, ...any)) {
	rebuilt, err := ledger.RebuildGenesisLibraryJSON(genesisJSON, ledger.Upgrades)
	if err != nil {
		warnf("this release cannot rebuild the genesis library: %v", err)
		return
	}
	rebuiltHash, err := libraryHashFromJSON(rebuilt)
	if err != nil {
		warnf("this release cannot rebuild the genesis library: %v", err)
		return
	}
	if rebuiltHash != genesisHash {
		warnf("the genesis library has hash %s, this release builds %s from its definitions: the node runs on the stored "+
			"library, but a new ledger created by this release would not have the definitions of this one",
			hex.EncodeToString(genesisHash[:]), hex.EncodeToString(rebuiltHash[:]))
	}
}

// replacePendingUpgrades drops every stored library upgrade that no state commits to yet, the
// store holding no branch at or after its slot, when this release does not build it: unlisted
// (the upgrade moved or cancelled) or listed with other definitions. storeUpgrade then stores
// the release's own. A library enters the state only with the branches of its slot, through
// the upgrade UTXO, so until then a release may still change a pending upgrade's rules or slot:
// a last resort after a release, since every node must run the same build by the slot, and a
// node crossing the slot on another library falls out of the network. Once a branch at or
// after the slot exists, checkStoredUpgrades refuses any difference, as before. Later pending
// upgrades are built on an earlier one, so from the first dropped one on, all are dropped.
func replacePendingUpgrades(stateStore global.Store, upgrades []*ledger.UpgradeDefinition, warnf func(string, ...any)) error {
	latest := FetchLatestCommittedSlot(stateStore)
	var pending []uint32
	IterateUpgradeLibraries(stateStore, func(slot uint32, _ []byte) bool {
		if slot != 0 && slot > latest {
			pending = append(pending, slot)
		}
		return true
	})
	sort.Slice(pending, func(i, j int) bool { return pending[i] < pending[j] })
	for i, slot := range pending {
		storedJSON, _ := GetUpgradeLibraryDirect(stateStore, slot)
		storedHash, err := libraryHashFromJSON(storedJSON)
		if err != nil {
			return fmt.Errorf("the stored library upgrade of slot %d: %w", slot, err)
		}
		reason := fmt.Sprintf("this release does not list an upgrade at slot %d", slot)
		for _, upgrade := range upgrades {
			if upgrade.Slot == slot {
				if checkStoredAgainstBuild(stateStore, upgrade, storedHash) == nil {
					reason = ""
				} else {
					reason = fmt.Sprintf("this release builds other definitions for slot %d", slot)
				}
			}
		}
		if reason == "" {
			continue
		}
		for _, drop := range pending[i:] {
			dropJSON, _ := GetUpgradeLibraryDirect(stateStore, drop)
			dropHash, _ := libraryHashFromJSON(dropJSON)
			if err = deletePendingUpgradeLibrary(stateStore, drop); err != nil {
				return err
			}
			warnf("PENDING LIBRARY UPGRADE REPLACED: the database held a library upgrade at slot %d (hash %s) that no state "+
				"commits to yet, and %s: it is dropped, and the release's own upgrades are stored. Every node must run "+
				"the same release by the upgrade slot", drop, hex.EncodeToString(dropHash[:]), reason)
		}
		return nil
	}
	return nil
}

// checkStoredUpgrades checks each stored library upgrade against what vouches for it. The last
// upgrade of the list, pending or the latest active one, must be the library the release
// builds, committed in the state or not: a node that ran another build across its slot has
// committed that build's library into its own state, a fork the state check alone would accept.
// An earlier upgrade the state commits to is vouched for by the state: if the release builds
// another library for it, the node is warned and runs. A stored upgrade the state does not
// commit to and the release does not list is accepted when its slot is past: the state
// predates it (a snapshot taken before the slot), and the network's roots check it when the
// node syncs across the slot. One whose slot is still to come is a library from another build,
// and is refused.
func checkStoredUpgrades(stateStore global.Store, upgrades []*ledger.UpgradeDefinition, warnf func(string, ...any)) error {
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
		if err := checkStoredUpgrade(stateStore, lib.slot, lib.json, readers, upgrades, warnf); err != nil {
			return err
		}
	}
	return nil
}

func checkStoredUpgrade(stateStore global.Store, slot uint32, storedJSON []byte, latestStates []*Readable,
	upgrades []*ledger.UpgradeDefinition, warnf func(string, ...any)) error {
	storedHash, err := libraryHashFromJSON(storedJSON)
	if err != nil {
		return fmt.Errorf("the stored library upgrade of slot %d: %w", slot, err)
	}
	listed := false
	var buildErr error
	for i, upgrade := range upgrades {
		if upgrade.Slot != slot {
			continue
		}
		listed = true
		if buildErr = checkStoredAgainstBuild(stateStore, upgrade, storedHash); buildErr != nil && i == len(upgrades)-1 {
			return buildErr
		}
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
	if buildErr != nil {
		if !committed {
			return buildErr
		}
		warnf("%v. The state commits to the stored library, so the node runs on it, but a new ledger created "+
			"by this release would not have the definitions of this one", buildErr)
	}
	if committed || listed {
		return nil
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
