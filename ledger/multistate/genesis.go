package multistate

import (
	"fmt"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
	"github.com/lunfardo314/unitrie/immutable"
)

// CommitEmptyRootWithLedgerIdentity writes minimal ledger identity data as value of the empty key nil.
// The identity contains only genesis time and description (truly immutable data).
// Returns root of the empty trie.
func CommitEmptyRootWithLedgerIdentity(identity *ledger.LedgerIdentity, store StateStore) (common.VCommitment, error) {
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
func InitStateStoreFromGlobals(store StateStore) (base.ChainID, common.VCommitment) {
	// Create minimal identity from constants
	identity := ledger.NewLedgerIdentity(ledger.Const.GenesisTimeUnix, ledger.Const.Description)
	emptyRoot, err := CommitEmptyRootWithLedgerIdentity(identity, store)
	util.AssertNoError(err)

	// Store library YAML in upgrade DB partition at slot 0
	libraryYAML := ledger.L(base.MaxSlot).DefinitionsYAML()
	err = WriteUpgradeLibrary(store, 0, libraryYAML)
	util.AssertNoError(err)

	genesisAddr := ledger.AddressED25519FromPublicKey(ledger.Const.GenesisControllerPublicKey)

	initialSupply := ledger.Const.InitialSupply
	gout := ledger.GenesisOutput(initialSupply, genesisAddr)
	gStemOut := ledger.GenesisStemOutput()

	// Create upgrade commitment UTXO for slot 0
	libraryHash := ledger.L(base.MaxSlot).LibraryHash()
	upgradeOut := ledger.UpgradeUTXO(0, libraryHash)

	updatable := MustNewUpdatable(store, emptyRoot)
	updatable.MustUpdate(genesisUpdateMutations(&gout.OutputWithID, gStemOut, upgradeOut), &RootRecordParams{
		StemOutputID:      gStemOut.ID,
		SeqID:             gout.ChainID,
		CoverageDelta:     initialSupply,
		SlotInflation:     initialSupply,
		Supply:            initialSupply,
		WriteEarliestSlot: true,
	})
	return gout.ChainID, updatable.Root()
}

func genesisUpdateMutations(genesisOut, genesisStemOut, upgradeOut *ledger.OutputWithID) *Mutations {
	ret := NewMutations()
	ret.InsertAddOutputMutation(genesisOut.ID, genesisOut.Output)
	ret.InsertAddOutputMutation(genesisStemOut.ID, genesisStemOut.Output)
	// Use raw clone for upgrade UTXO since it doesn't have a standard lock
	ret.InsertAddOutputMutationRaw(upgradeOut.ID, upgradeOut.Output)
	ret.InsertAddTxMutation(base.GenesisTransactionID(), genesisOut.ID.Slot(), 1)
	return ret
}

// ScanGenesisState scans the genesis state and returns constants and root commitment.
// It loads the library from the upgrade DB partition (slot 0).
func ScanGenesisState(stateStore StateStore) (*ledger.Constants, common.VCommitment, error) {
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
	if out.TokenBalance() != constants.InitialSupply {
		return nil, nil, fmt.Errorf("different amounts in genesis output and state definitions")
	}
	return constants, branchData.Root, nil
}

// InitLedgerFromStore initializes the ledger library cache from the state store.
// It loads libraries from the upgrade DB partition.
func InitLedgerFromStore(stateStore StateStore) {
	// Register the resolver for upgrade 0
	ledger.RegisterResolverForUpgrade(0, ledger.GetEmbeddedFunctionResolverUpgrade0)

	// Initialize the library cache with the state store
	ledger.MustInitLibraryCache(stateStore)
}
