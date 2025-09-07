package multistate

import (
	"fmt"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
	"github.com/lunfardo314/unitrie/immutable"
)

// CommitEmptyRootWithLedgerIdentity writes ledger identity data as value of the empty key nil.
// Return root of the empty trie
func CommitEmptyRootWithLedgerIdentity(id []byte, store StateStore) (common.VCommitment, error) {
	batch := store.BatchedWriter()
	emptyRoot := immutable.MustInitRoot(batch, ledger.CommitmentModel, id)
	if err := batch.Commit(); err != nil {
		return nil, err
	}
	return emptyRoot, nil
}

// InitStateStoreFromGlobals initializes origin ledger state in the empty store
// Writes initial supply and origin stem outputs. Plus writes root record into the DB
// Returns root commitment to the genesis ledger state and genesis chainID
// The function is dependent on the global singleton of ledger definitions, because
// origin outputs can be created only with the library
func InitStateStoreFromGlobals(store StateStore) (base.ChainID, common.VCommitment) {
	emptyRoot, err := CommitEmptyRootWithLedgerIdentity(ledger.L().IdentityData(), store)
	util.AssertNoError(err)

	genesisAddr := ledger.AddressED25519FromPublicKey(ledger.Const.GenesisControllerPublicKey)

	initialSupply := ledger.Const.InitialSupply
	gout := ledger.GenesisOutput(initialSupply, genesisAddr)
	gStemOut := ledger.GenesisStemOutput()

	updatable := MustNewUpdatable(store, emptyRoot)
	updatable.MustUpdate(genesisUpdateMutations(&gout.OutputWithID, gStemOut), &RootRecordParams{
		StemOutputID:      gStemOut.ID,
		SeqID:             gout.ChainID,
		CoverageDelta:     initialSupply,
		SlotInflation:     initialSupply,
		Supply:            initialSupply,
		WriteEarliestSlot: true,
	})
	return gout.ChainID, updatable.Root()
}

func genesisUpdateMutations(genesisOut, genesisStemOut *ledger.OutputWithID) *Mutations {
	ret := NewMutations()
	ret.InsertAddOutputMutation(genesisOut.ID, genesisOut.Output)
	ret.InsertAddOutputMutation(genesisStemOut.ID, genesisStemOut.Output)
	ret.InsertAddTxMutation(base.GenesisTransactionID(), genesisOut.ID.Slot(), 1)
	return ret
}

// ScanGenesisState TODO more checks
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
	yamlData := rdr.MustLedgerIdentityBytes()
	lib, err := ledger.ParseLibraryFromYAML(yamlData)
	util.AssertNoError(err)

	genesisOid := base.GenesisOutputID()
	out, err := rdr.GetOutputErr(genesisOid)
	if err != nil {
		return nil, nil, fmt.Errorf("GetOutputErr(%s): %w", genesisOid.StringShort(), err)
	}
	constants := ledger.ConstantsFromLibrary(lib)
	if out.TokenBalance() != constants.InitialSupply {
		return nil, nil, fmt.Errorf("different amounts in genesis output and state identity")
	}
	return constants, branchData.Root, nil
}

func InitLedgerFromStore(stateStore StateStore) {
	ledger.MustInitSingleton(LedgerIdentityBytesFromStore(stateStore))
}
