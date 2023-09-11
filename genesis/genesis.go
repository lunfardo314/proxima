package genesis

import (
	"crypto/ed25519"
	"fmt"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/txbuilder"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
	"github.com/lunfardo314/unitrie/immutable"
)

// InitLedgerState initializes origin ledger state in the empty store
// Writes initial supply and origin stem outputs. Plus writes root record into the DB
// Returns root commitment to the genesis ledger state and genesis chainID
func InitLedgerState(par StateIdentityData, store general.StateStore) (core.ChainID, common.VCommitment) {
	batch := store.BatchedWriter()
	emptyRoot := immutable.MustInitRoot(batch, core.CommitmentModel, par.Bytes())
	err := batch.Commit()
	util.AssertNoError(err)

	genesisAddr := core.AddressED25519FromPublicKey(par.GenesisControllerPublicKey)
	gout := InitialSupplyOutput(par.InitialSupply, genesisAddr, par.GenesisTimeSlot)
	gStemOut := StemOutput(par.InitialSupply, par.GenesisTimeSlot)

	updatable := multistate.MustNewUpdatable(store, emptyRoot)
	updatable.MustUpdateWithCommands(genesisUpdateCommands(&gout.OutputWithID, gStemOut), &gStemOut.ID, &gout.ChainID)

	return gout.ChainID, updatable.Root()
}

func InitialSupplyOutput(initialSupply uint64, controllerAddress core.AddressED25519, genesisSlot core.TimeSlot) *core.OutputWithChainID {
	oid := InitialSupplyOutputID(genesisSlot)
	return &core.OutputWithChainID{
		OutputWithID: core.OutputWithID{
			ID: oid,
			Output: core.NewOutput(func(o *core.Output) {
				o.WithAmount(initialSupply).WithLock(controllerAddress)
				chainIdx, err := o.PushConstraint(core.NewChainOrigin().Bytes())
				util.AssertNoError(err)
				_, err = o.PushConstraint(core.NewSequencerConstraint(chainIdx, 0).Bytes())
				util.AssertNoError(err)
			}),
		},
		ChainID: core.OriginChainID(&oid),
	}
}

func StemOutput(initialSupply uint64, genesisTimeSlot core.TimeSlot) *core.OutputWithID {
	return &core.OutputWithID{
		ID: StemOutputID(genesisTimeSlot),
		Output: core.NewOutput(func(o *core.Output) {
			o.WithAmount(0).
				WithLock(core.NewStemLock(initialSupply, 0, core.OutputID{}))
		}),
	}
}

func genesisUpdateCommands(genesisOut, genesisStemOut *core.OutputWithID) []multistate.UpdateCmd {
	return []multistate.UpdateCmd{
		{
			ID:     &genesisOut.ID,
			Output: genesisOut.Output,
		},
		{
			ID:     &genesisStemOut.ID,
			Output: genesisStemOut.Output,
		},
	}
}

func InitialSupplyTransactionID(genesisTimeSlot core.TimeSlot) *core.TransactionID {
	ret := core.NewTransactionID(core.MustNewLogicalTime(genesisTimeSlot, 0), core.All0TransactionHash, true, true)
	return &ret
}

func InitialSupplyOutputID(e core.TimeSlot) (ret core.OutputID) {
	// we are placing sequencer flag = true into the genesis tx ID to please sequencer constraint
	// of the origin branch transaction. It is the only exception
	ret = core.NewOutputID(InitialSupplyTransactionID(e), InitialSupplyOutputIndex)
	return
}

func StemOutputID(e core.TimeSlot) (ret core.OutputID) {
	ret = core.NewOutputID(InitialSupplyTransactionID(e), StemOutputIndex)
	return
}

const (
	MinimumBalanceOnBoostrapSequencer = 1_000_000
)

// DistributeInitialSupply updates genesis state and branch records according to initial supply distribution parameters by
// adding initial distribution transaction.
// Distribution transaction is a branch transaction in the slot next after the genesis.
// Distribution parameter is added to the transaction store
func DistributeInitialSupply(stateStore general.StateStore, originPrivateKey ed25519.PrivateKey, genesisDistribution []txbuilder.LockBalance, txBytesStore common.KVStore) error {
	err := util.CatchPanicOrError(func() error {
		MustDistributeInitialSupply(stateStore, originPrivateKey, genesisDistribution, txBytesStore)
		return nil
	})
	if err != nil {
		err = fmt.Errorf("DistributeInitialSupply: %v", err)
	}
	return err
}

func MustDistributeInitialSupply(stateStore general.StateStore, originPrivateKey ed25519.PrivateKey, genesisDistribution []txbuilder.LockBalance, txBytesStore common.KVStore) {
	branchData := multistate.FetchBranchData(stateStore)
	util.Assertf(len(branchData) == 1, "not a genesis state: expected to find exactly 1 branch")
	rdr := multistate.MustNewSugaredReadableState(stateStore, branchData[0].Root)
	stateID := MustStateIdentityDataFromBytes(rdr.StateIdentityBytes())

	originPublicKey := originPrivateKey.Public().(ed25519.PublicKey)
	util.Assertf(originPublicKey.Equal(stateID.GenesisControllerPublicKey), "private and public keys does not match")
	util.Assertf(len(genesisDistribution) < 253, "too many addresses in the genesis distribution. Maximum is 252")

	distributeTotal := uint64(0)
	for i := range genesisDistribution {
		distributeTotal += genesisDistribution[i].Balance
		util.Assertf(distributeTotal+MinimumBalanceOnBoostrapSequencer <= stateID.InitialSupply,
			"condition failed: distributeTotal(%d) + MinimumBalanceOnBoostrapSequencer(%d) < InitialSupply(%d)",
			distributeTotal, MinimumBalanceOnBoostrapSequencer, stateID.InitialSupply)
	}
	genesisDistributionOutputs := make([]*core.Output, len(genesisDistribution))
	for i := range genesisDistribution {
		genesisDistributionOutputs[i] = core.NewOutput(func(o *core.Output) {
			o.WithAmount(genesisDistribution[i].Balance).
				WithLock(genesisDistribution[i].Lock)
		})
	}

	genesisStem := rdr.GetStemOutput()
	bootstrapChainID := stateID.OriginChainID()
	initSupplyOutput, err := rdr.GetChainOutput(&bootstrapChainID)
	util.AssertNoError(err)

	// create origin branch transaction at the next slot after genesis time slot
	txBytes, err := txbuilder.MakeSequencerTransaction(txbuilder.MakeSequencerTransactionParams{
		ChainInput: &core.OutputWithChainID{
			OutputWithID: *initSupplyOutput,
			ChainID:      bootstrapChainID,
		},
		StemInput:         genesisStem,
		Timestamp:         core.MustNewLogicalTime(genesisStem.Timestamp().TimeSlot()+1, 0),
		MinimumFee:        0,
		AdditionalInputs:  nil,
		AdditionalOutputs: genesisDistributionOutputs,
		Endorsements:      nil,
		PrivateKey:        originPrivateKey,
		TotalSupply:       stateID.InitialSupply,
	})
	util.AssertNoError(err)

	tx, err := transaction.FromBytesMainChecksWithOpt(txBytes)
	util.AssertNoError(err)

	err = tx.Validate(transaction.ValidateOptionWithFullContext(tx.InputLoaderFromState(rdr)))
	util.AssertNoError(err)

	nextStem := tx.FindStemProducedOutput()
	util.Assertf(nextStem != nil, "nextStem != nil")
	cmds := tx.UpdateCommands()

	updatableOrigin := multistate.MustNewUpdatable(stateStore, branchData[0].Root)
	updatableOrigin.MustUpdateWithCommands(cmds, &nextStem.ID, &bootstrapChainID)

	txBytesStore.Set(tx.ID()[:], txBytes)
}
