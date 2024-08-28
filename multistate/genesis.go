package multistate

import (
	"fmt"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
	"github.com/lunfardo314/unitrie/immutable"
)

// CommitEmptyRootWithLedgerIdentity writes ledger identity data as value of the empty key nil.
// Return root of the empty trie
func CommitEmptyRootWithLedgerIdentity(par ledger.IdentityData, store global.StateStore) (common.VCommitment, error) {
	batch := store.BatchedWriter()
	emptyRoot := immutable.MustInitRoot(batch, ledger.CommitmentModel, par.Bytes())
	if err := batch.Commit(); err != nil {
		return nil, err
	}
	return emptyRoot, nil
}

// InitStateStore initializes origin ledger state in the empty store
// Writes initial supply and origin stem outputs. Plus writes root record into the DB
// Returns root commitment to the genesis ledger state and genesis chainID
func InitStateStore(par ledger.IdentityData, store global.StateStore) (ledger.ChainID, common.VCommitment) {
	emptyRoot, err := CommitEmptyRootWithLedgerIdentity(par, store)
	util.AssertNoError(err)

	genesisAddr := ledger.AddressED25519FromPublicKey(par.GenesisControllerPublicKey)
	gout := ledger.GenesisOutput(par.InitialSupply, genesisAddr)
	gStemOut := ledger.GenesisStemOutput()

	updatable := MustNewUpdatable(store, emptyRoot)
	updatable.MustUpdate(genesisUpdateMutations(&gout.OutputWithID, gStemOut), &RootRecordParams{
		StemOutputID:      gStemOut.ID,
		SeqID:             gout.ChainID,
		Coverage:          par.InitialSupply,
		SlotInflation:     par.InitialSupply,
		Supply:            par.InitialSupply,
		WriteEarliestSlot: true,
	})
	return gout.ChainID, updatable.Root()
}

func genesisUpdateMutations(genesisOut, genesisStemOut *ledger.OutputWithID) *Mutations {
	ret := NewMutations()
	ret.InsertAddOutputMutation(genesisOut.ID, genesisOut.Output)
	ret.InsertAddOutputMutation(genesisStemOut.ID, genesisStemOut.Output)
	ret.InsertAddTxMutation(*ledger.GenesisTransactionID(), genesisOut.ID.Slot(), 1)
	return ret
}

// ScanGenesisState TODO more checks
func ScanGenesisState(stateStore global.StateStore) (*ledger.IdentityData, common.VCommitment, error) {
	var genesisRootRecord RootRecord

	// expecting a single branch in the genesis state
	fetched, moreThan1 := false, false
	IterateRootRecords(stateStore, func(_ ledger.TransactionID, rootData RootRecord) bool {
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
	stateID := ledger.MustIdentityDataFromBytes(rdr.MustLedgerIdentityBytes())

	genesisOid := ledger.GenesisOutputID()
	out, err := rdr.GetOutputErr(&genesisOid)
	if err != nil {
		return nil, nil, fmt.Errorf("GetOutputErr(%s): %w", genesisOid.StringShort(), err)
	}
	if out.Amount() != stateID.InitialSupply {
		return nil, nil, fmt.Errorf("different amounts in genesis output and state identity")
	}
	return stateID, branchData.Root, nil
}

func InitLedgerFromStore(stateStore global.StateStore, verbose ...bool) {
	ledger.Init(ledger.MustIdentityDataFromBytes(LedgerIdentityBytesFromStore(stateStore)), verbose...)
}
