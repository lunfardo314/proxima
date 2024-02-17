package multistate

import (
	"fmt"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
	"github.com/lunfardo314/unitrie/immutable"
)

// InitStateStore initializes origin ledger state in the empty store
// Writes initial supply and origin stem outputs. Plus writes root record into the DB
// Returns root commitment to the genesis ledger state and genesis chainID
func InitStateStore(par ledger.IdentityData, store global.StateStore) (ledger.ChainID, common.VCommitment) {
	batch := store.BatchedWriter()
	emptyRoot := immutable.MustInitRoot(batch, ledger.CommitmentModel, par.Bytes())
	err := batch.Commit()
	util.AssertNoError(err)

	genesisAddr := ledger.AddressED25519FromPublicKey(par.GenesisControllerPublicKey)
	gout := InitialSupplyOutput(par.InitialSupply, genesisAddr)
	gStemOut := GenesisStemOutput()

	updatable := MustNewUpdatable(store, emptyRoot)
	coverage := LedgerCoverage{0, par.InitialSupply}
	updatable.MustUpdate(genesisUpdateMutations(&gout.OutputWithID, gStemOut), &RootRecordParams{
		StemOutputID:  gStemOut.ID,
		SeqID:         gout.ChainID,
		Coverage:      coverage,
		SlotInflation: 0,
		Supply:        par.InitialSupply,
	})
	return gout.ChainID, updatable.Root()
}

func InitialSupplyOutput(initialSupply uint64, controllerAddress ledger.AddressED25519) *ledger.OutputWithChainID {
	oid := ledger.GenesisOutputID()
	return &ledger.OutputWithChainID{
		OutputWithID: ledger.OutputWithID{
			ID: oid,
			Output: ledger.NewOutput(func(o *ledger.Output) {
				o.WithAmount(initialSupply).WithLock(controllerAddress)
				chainIdx, err := o.PushConstraint(ledger.NewChainOrigin().Bytes())
				util.AssertNoError(err)
				_, err = o.PushConstraint(ledger.NewSequencerConstraint(chainIdx, initialSupply).Bytes())
				util.AssertNoError(err)
			}),
		},
		ChainID: ledger.OriginChainID(&oid),
	}
}

func GenesisStemOutput() *ledger.OutputWithID {
	return &ledger.OutputWithID{
		ID: ledger.GenesisStemOutputID(),
		Output: ledger.NewOutput(func(o *ledger.Output) {
			o.WithAmount(0).
				WithLock(&ledger.StemLock{
					PredecessorOutputID: ledger.OutputID{},
				})
		}),
	}
}

func genesisUpdateMutations(genesisOut, genesisStemOut *ledger.OutputWithID) *Mutations {
	ret := NewMutations()
	ret.InsertAddOutputMutation(genesisOut.ID, genesisOut.Output)
	ret.InsertAddOutputMutation(genesisStemOut.ID, genesisStemOut.Output)
	ret.InsertAddTxMutation(*ledger.GenesisTransactionID(), genesisOut.ID.TimeSlot(), 1)
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
	stateID := ledger.MustLedgerIdentityDataFromBytes(rdr.MustLedgerIdentityBytes())

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
	ledger.Init(ledger.MustLedgerIdentityDataFromBytes(LedgerIdentityBytesFromStore(stateStore)), verbose...)
}
