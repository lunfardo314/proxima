package txbuilder

import (
	"crypto/ed25519"
	"fmt"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/genesis"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/util"
)

func MustDistributeInitialSupply(stateStore general.StateStore, originPrivateKey ed25519.PrivateKey, genesisDistribution []core.LockBalance) []byte {
	stateID, genesisRoot, err := genesis.ScanGenesisState(stateStore)
	util.AssertNoError(err)

	originPublicKey := originPrivateKey.Public().(ed25519.PublicKey)
	util.Assertf(originPublicKey.Equal(stateID.GenesisControllerPublicKey), "private and public keys do not match")
	util.Assertf(len(genesisDistribution) < 253, "too many addresses in the genesis distribution. Maximum is 252")

	distributeTotal := uint64(0)
	for i := range genesisDistribution {
		distributeTotal += genesisDistribution[i].Balance
		util.Assertf(distributeTotal+core.MinimumAmountOnSequencer <= stateID.InitialSupply,
			"condition failed: distributeTotal(%d) + MinimumBalanceOnBoostrapSequencer(%d) < InitialSupply(%d)",
			distributeTotal, core.MinimumAmountOnSequencer, stateID.InitialSupply)
	}
	genesisDistributionOutputs := make([]*core.Output, len(genesisDistribution))
	for i := range genesisDistribution {
		genesisDistributionOutputs[i] = core.NewOutput(func(o *core.Output) {
			o.WithAmount(genesisDistribution[i].Balance).
				WithLock(genesisDistribution[i].Lock)
		})
	}

	rdr := multistate.MustNewSugaredReadableState(stateStore, genesisRoot)

	genesisStem := rdr.GetStemOutput()
	bootstrapChainID := stateID.OriginChainID()
	initSupplyOutput, err := rdr.GetChainOutput(&bootstrapChainID)
	util.AssertNoError(err)

	// create origin branch transaction at the next slot after genesis time slot
	txBytes, err := MakeSequencerTransaction(MakeSequencerTransactionParams{
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
	muts := tx.StateMutations()

	updatableOrigin := multistate.MustNewUpdatable(stateStore, genesisRoot)
	updatableOrigin.MustUpdate(muts, &nextStem.ID, &bootstrapChainID, stateID.InitialSupply)

	return txBytes
}

// DistributeInitialSupply updates genesis state and branch records according to initial supply distribution parameters by
// adding initial distribution transaction.
// Distribution transaction is a branch transaction in the slot next after the genesis.
// Distribution parameter is added to the transaction store
func DistributeInitialSupply(stateStore general.StateStore, originPrivateKey ed25519.PrivateKey, genesisDistribution []core.LockBalance) ([]byte, error) {
	var ret []byte
	err := util.CatchPanicOrError(func() error {
		ret = MustDistributeInitialSupply(stateStore, originPrivateKey, genesisDistribution)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("DistributeInitialSupply: %v", err)
	}
	return ret, nil
}
