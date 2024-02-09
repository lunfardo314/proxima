package genesis

import (
	"fmt"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
	"github.com/lunfardo314/unitrie/immutable"
)

// InitLedgerState initializes origin ledger state in the empty store
// Writes initial supply and origin stem outputs. Plus writes root record into the DB
// Returns root commitment to the genesis ledger state and genesis chainID
func InitLedgerState(par ledger.IdentityData, store global.StateStore) (ledger.ChainID, common.VCommitment) {
	batch := store.BatchedWriter()
	emptyRoot := immutable.MustInitRoot(batch, ledger.CommitmentModel, par.Bytes())
	err := batch.Commit()
	util.AssertNoError(err)

	genesisAddr := ledger.AddressED25519FromPublicKey(par.GenesisControllerPublicKey)
	gout := InitialSupplyOutput(par.InitialSupply, genesisAddr, par.GenesisSlot)
	gStemOut := StemOutput(par.GenesisSlot)

	updatable := multistate.MustNewUpdatable(store, emptyRoot)
	coverage := multistate.LedgerCoverage{0, par.InitialSupply}
	updatable.MustUpdate(genesisUpdateMutations(&gout.OutputWithID, gStemOut), &multistate.RootRecordParams{
		StemOutputID:  gStemOut.ID,
		SeqID:         gout.ChainID,
		Coverage:      coverage,
		SlotInflation: 0,
		Supply:        par.InitialSupply,
	})
	return gout.ChainID, updatable.Root()
}

func InitialSupplyOutput(initialSupply uint64, controllerAddress ledger.AddressED25519, genesisSlot ledger.Slot) *ledger.OutputWithChainID {
	oid := ledger.InitialSupplyOutputID(genesisSlot)
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

func StemOutput(genesisTimeSlot ledger.Slot) *ledger.OutputWithID {
	return &ledger.OutputWithID{
		ID: ledger.StemOutputID(genesisTimeSlot),
		Output: ledger.NewOutput(func(o *ledger.Output) {
			o.WithAmount(0).
				WithLock(&ledger.StemLock{
					PredecessorOutputID: ledger.OutputID{},
				})
		}),
	}
}

func genesisUpdateMutations(genesisOut, genesisStemOut *ledger.OutputWithID) *multistate.Mutations {
	ret := multistate.NewMutations()
	ret.InsertAddOutputMutation(genesisOut.ID, genesisOut.Output)
	ret.InsertAddOutputMutation(genesisStemOut.ID, genesisStemOut.Output)
	ret.InsertAddTxMutation(*ledger.InitialSupplyTransactionID(genesisOut.ID.TimeSlot()), genesisOut.ID.TimeSlot(), 1)
	return ret
}

// ScanGenesisState TODO more checks
func ScanGenesisState(stateStore global.StateStore) (*ledger.IdentityData, common.VCommitment, error) {
	var genesisRootRecord multistate.RootRecord

	// expecting a single branch in the genesis state
	fetched, moreThan1 := false, false
	multistate.IterateRootRecords(stateStore, func(_ ledger.TransactionID, rootData multistate.RootRecord) bool {
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

	branchData := multistate.FetchBranchDataByRoot(stateStore, genesisRootRecord)
	rdr := multistate.MustNewSugaredReadableState(stateStore, branchData.Root)
	stateID := ledger.MustLedgerIdentityDataFromBytes(rdr.MustLedgerIdentityBytes())

	genesisOid := ledger.InitialSupplyOutputID(stateID.GenesisSlot)
	out, err := rdr.GetOutputErr(&genesisOid)
	if err != nil {
		return nil, nil, fmt.Errorf("GetOutputErr(%s): %w", genesisOid.StringShort(), err)
	}
	if out.Amount() != stateID.InitialSupply {
		return nil, nil, fmt.Errorf("different amounts in genesis output and state identity")
	}
	return stateID, branchData.Root, nil
}
