package txbuilder

import (
	"crypto/ed25519"
	"fmt"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
)

type LockBalanceYAMLable struct {
	LockString string `yaml:"lock"`
	Balance    uint64 `yaml:"balance"`
}

// MakeDistributionTransaction creates initial distribution transaction according to distribution list.
// It is a branch transaction. Remainder goes to the genesis chain
func MakeDistributionTransaction(stateStore global.StateStore, originPrivateKey ed25519.PrivateKey, genesisDistribution []ledger.LockBalance) ([]byte, error) {
	stateID, genesisRoot, err := multistate.ScanGenesisState(stateStore)
	if err != nil {
		return nil, err
	}

	originPublicKey := originPrivateKey.Public().(ed25519.PublicKey)
	err = util.ErrorConditionf(originPublicKey.Equal(stateID.GenesisControllerPublicKey), "private and public keys do not match")
	if err != nil {
		return nil, err
	}
	err = util.ErrorConditionf(len(genesisDistribution) < 253, "too many addresses in the genesis distribution. Maximum is 252")
	if err != nil {
		return nil, err
	}

	distributeTotal := uint64(0)
	for i := range genesisDistribution {
		distributeTotal += genesisDistribution[i].Balance
		minimumAmountOnSequencer := ledger.L().Const().MinimumAmountOnSequencer()
		err = util.ErrorConditionf(distributeTotal+minimumAmountOnSequencer <= stateID.InitialSupply,
			"condition failed: distributeTotal(%d) + MinimumBalanceOnBoostrapSequencer(%d) < InitialSupply(%d)",
			distributeTotal, minimumAmountOnSequencer, stateID.InitialSupply)
		if err != nil {
			return nil, err
		}
	}
	genesisDistributionOutputs := make([]*ledger.Output, len(genesisDistribution))
	for i := range genesisDistribution {
		if !genesisDistribution[i].ChainBalance {
			genesisDistributionOutputs[i] = ledger.NewOutput(func(o *ledger.Output) {
				o.WithAmount(genesisDistribution[i].Balance).
					WithLock(genesisDistribution[i].Lock)
			})
		} else {
			genesisDistributionOutputs[i] = ledger.NewOutput(func(o *ledger.Output) {
				_, _ = o.WithAmount(genesisDistribution[i].Balance).
					WithLock(genesisDistribution[i].Lock).
					PushConstraint(ledger.NewChainOrigin().Bytes())
			})
		}
	}

	rdr, err := multistate.NewSugaredReadableState(stateStore, genesisRoot)
	if err != nil {
		return nil, err
	}

	genesisStem := rdr.GetStemOutput()
	bootstrapChainID := stateID.OriginChainID()
	initSupplyOutput, err := rdr.GetChainOutput(&bootstrapChainID)
	if err != nil {
		return nil, err
	}

	// create origin branch transaction at the next slot after genesis time slot
	txBytes, err := MakeSequencerTransaction(MakeSequencerTransactionParams{
		ChainInput: &ledger.OutputWithChainID{
			OutputWithID: *initSupplyOutput,
			ChainID:      bootstrapChainID,
		},
		StemInput:         genesisStem,
		Timestamp:         ledger.MustNewLedgerTime(genesisStem.Timestamp().Slot()+1, 0),
		MinimumFee:        0,
		AdditionalInputs:  nil,
		AdditionalOutputs: genesisDistributionOutputs,
		Endorsements:      nil,
		PrivateKey:        originPrivateKey,
		PutInflation:      false,
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
func DistributeInitialSupply(stateStore global.StateStore, originPrivateKey ed25519.PrivateKey, genesisDistribution []ledger.LockBalance) ([]byte, error) {
	txBytes, _, err := DistributeInitialSupplyExt(stateStore, originPrivateKey, genesisDistribution)
	return txBytes, err
}

func DistributeInitialSupplyExt(stateStore global.StateStore, originPrivateKey ed25519.PrivateKey, genesisDistribution []ledger.LockBalance) ([]byte, ledger.TransactionID, error) {
	var ret []byte
	var txid ledger.TransactionID
	err := util.CatchPanicOrError(func() error {
		ret, txid = MustDistributeInitialSupplyExt(stateStore, originPrivateKey, genesisDistribution)
		return nil
	})
	if err != nil {
		return nil, ledger.TransactionID{}, fmt.Errorf("DistributeInitialSupply: %v", err)
	}
	return ret, txid, nil
}

// MustDistributeInitialSupply makes distribution transaction and commits it into the multi-ledger state with branch record
func MustDistributeInitialSupply(stateStore global.StateStore, originPrivateKey ed25519.PrivateKey, genesisDistribution []ledger.LockBalance) []byte {
	ret, _ := MustDistributeInitialSupplyExt(stateStore, originPrivateKey, genesisDistribution)
	return ret
}

// MustDistributeInitialSupplyExt makes distribution transaction and commits it into the multi-ledger state with branch record
func MustDistributeInitialSupplyExt(stateStore global.StateStore, originPrivateKey ed25519.PrivateKey, genesisDistribution []ledger.LockBalance) ([]byte, ledger.TransactionID) {
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
		Coverage:        (stateID.InitialSupply >> 1) + stateID.InitialSupply,
		SlotInflation:   0,
		Supply:          stateID.InitialSupply,
		NumTransactions: 1,
	})
	return txBytes, *tx.ID()
}
