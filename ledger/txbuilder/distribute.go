package txbuilder

import (
	"crypto/ed25519"
	"fmt"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util"
)

// MakeDistributionTransaction creates initial distribution transaction according to distribution list.
// It is a branch transaction. Remainder goes to the genesis chain
func MakeDistributionTransaction(stateStore multistate.StateStore, originPrivateKey ed25519.PrivateKey, genesisDistribution []ledger.LockBalance) ([]byte, error) {
	stateID, genesisRoot, err := multistate.ScanGenesisState(stateStore)
	if err != nil {
		return nil, err
	}

	originPublicKey := originPrivateKey.Public().(ed25519.PublicKey)
	err = util.ErrorCondf(originPublicKey.Equal(stateID.GenesisControllerPublicKey), "private and public keys do not match")
	if err != nil {
		return nil, err
	}
	err = util.ErrorCondf(len(genesisDistribution) < 253, "too many addresses in the genesis distribution. Maximum is 252")
	if err != nil {
		return nil, err
	}

	distributeTotal := uint64(0)
	for i := range genesisDistribution {
		distributeTotal += genesisDistribution[i].Balance
		err = util.ErrorCondf(distributeTotal+ledger.L().ID.MinimumAmountOnSequencer <= stateID.InitialSupply,
			"condition failed: distributeTotal(%d) + MinimumBalanceOnBoostrapSequencer(%d) < InitialSupply(%d)",
			distributeTotal, ledger.L().ID.MinimumAmountOnSequencer, stateID.InitialSupply)
		if err != nil {
			return nil, err
		}
	}

	rdr, err := multistate.NewSugaredReadableState(stateStore, genesisRoot)
	if err != nil {
		return nil, err
	}

	genesisStem := rdr.GetStemOutput()
	ts := base.NewLedgerTime(genesisStem.Timestamp().Slot+1, 0)
	genesisDistributionOutputs := make([]*ledger.Output, len(genesisDistribution))
	for i := range genesisDistribution {
		genesisDistributionOutputs[i] = ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmount(genesisDistribution[i].Balance).
				WithLock(genesisDistribution[i].Lock)
			if genesisDistribution[i].ChainOrigin {
				o.MustPushConstraint(ledger.NewChainOrigin(ts.Slot, genesisDistribution[i].Balance).Bytes())
			}
		})
	}

	bootstrapChainID := stateID.OriginChainID()
	initSupplyOutput, err := rdr.GetChainOutput(bootstrapChainID)
	if err != nil {
		return nil, err
	}

	// create origin branch transaction at the next slot after genesis time slot
	txBytes, err := MakeSequencerTransaction(MakeSequencerTransactionParams{
		ChainInput: &ledger.OutputWithChainID{
			OutputWithID: *initSupplyOutput,
			ChainConstraintData: ledger.ChainConstraintData{
				ChainID:              bootstrapChainID,
				OriginAmount:         initSupplyOutput.Output.Amount(),
				ChainConstraintIndex: 2,
			},
		},
		StemInput:        genesisStem,
		Timestamp:        ts,
		MinimumFee:       0,
		AdditionalInputs: nil,
		WithdrawOutputs:  genesisDistributionOutputs,
		Endorsements:     nil,
		PrivateKey:       originPrivateKey,
		InflateMainChain: false,
	})
	if err != nil {
		return nil, err
	}
	return txBytes, nil
}

// DistributeInitialSupply updates genesis state and branch records according to initial supply distribution parameters by
// adding initial distribution transaction.
// Distribution transaction is a branch transaction in the slot next after the genesis.
// Distribution parameter is added to the transaction store
func DistributeInitialSupply(stateStore multistate.StateStore, originPrivateKey ed25519.PrivateKey, genesisDistribution []ledger.LockBalance) ([]byte, error) {
	txBytes, _, err := DistributeInitialSupplyExt(stateStore, originPrivateKey, genesisDistribution)
	return txBytes, err
}

func DistributeInitialSupplyExt(stateStore multistate.StateStore, originPrivateKey ed25519.PrivateKey, genesisDistribution []ledger.LockBalance) ([]byte, base.TransactionID, error) {
	var ret []byte
	var txid base.TransactionID
	err := util.CatchPanicOrError(func() error {
		ret, txid = MustDistributeInitialSupplyExt(stateStore, originPrivateKey, genesisDistribution)
		return nil
	})
	if err != nil {
		return nil, base.TransactionID{}, fmt.Errorf("DistributeInitialSupply: %v", err)
	}
	return ret, txid, nil
}

// MustDistributeInitialSupply makes distribution transaction and commits it into the multi-ledger state with branch record
func MustDistributeInitialSupply(stateStore multistate.StateStore, originPrivateKey ed25519.PrivateKey, genesisDistribution []ledger.LockBalance) []byte {
	ret, _ := MustDistributeInitialSupplyExt(stateStore, originPrivateKey, genesisDistribution)
	return ret
}

// MustDistributeInitialSupplyExt makes distribution transaction and commits it into the multi-ledger state with branch record
func MustDistributeInitialSupplyExt(stateStore multistate.StateStore, originPrivateKey ed25519.PrivateKey, genesisDistribution []ledger.LockBalance) ([]byte, base.TransactionID) {
	txBytes, err := MakeDistributionTransaction(stateStore, originPrivateKey, genesisDistribution)
	util.AssertNoError(err)

	stateID, genesisRoot, err := multistate.ScanGenesisState(stateStore)
	util.AssertNoError(err)

	rdr := multistate.MustNewSugaredReadableState(stateStore, genesisRoot)
	bootstrapChainID := stateID.OriginChainID()

	tx, err := transaction.FromBytesMainChecksWithOpt(txBytes)
	util.AssertNoError(err)

	err = tx.Validate(transaction.ValidateOptionWithFullContext(tx.InputLoaderFromState(rdr)))
	util.Assertf(err == nil, "%v\n>>>>>>>>>>>>>>>>> %s\n<<<<<<<<<<<<<\n", err, tx.String)

	nextStem := tx.FindStemProducedOutput()
	util.Assertf(nextStem != nil, "nextStem != nil")
	muts := tx.StateMutations()

	updatableOrigin := multistate.MustNewUpdatable(stateStore, genesisRoot)
	updatableOrigin.MustUpdate(muts, &multistate.RootRecordParams{
		StemOutputID:    nextStem.ID,
		SeqID:           bootstrapChainID,
		CoverageDelta:   stateID.InitialSupply,
		SlotInflation:   0,
		Supply:          stateID.InitialSupply,
		NumTransactions: 1,
	})
	return txBytes, tx.ID()
}
