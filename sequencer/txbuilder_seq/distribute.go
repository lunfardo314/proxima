package txbuilder_seq

import (
	"crypto/ed25519"
	"fmt"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util"
)

// MakeDistributionTransaction creates initial distribution transaction according to distribution list.
// It is a branch transaction. Remainder goes to the genesis chain
func MakeDistributionTransaction(stateStore global.Store, originPrivateKey ed25519.PrivateKey, genesisDistribution []ledger.LockBalance) ([]byte, error) {
	constants, genesisRoot, err := multistate.ScanGenesisState(stateStore)
	if err != nil {
		return nil, err
	}

	originPublicKey := originPrivateKey.Public().(ed25519.PublicKey)
	err = util.ErrorCondf(originPublicKey.Equal(constants.GenesisControllerPublicKey), "private and public keys do not match")
	if err != nil {
		return nil, err
	}
	err = util.ErrorCondf(len(genesisDistribution) < 253, "too many addresses in the genesis distribution. Maximum is 252")
	if err != nil {
		return nil, err
	}

	lib := ledger.L(base.MaxSlot)
	distributeTotal := uint64(0)
	for i := range genesisDistribution {
		distributeTotal += genesisDistribution[i].Balance
		err = util.ErrorCondf(distributeTotal+lib.MinimumAmountOnSequencer <= constants.InitialSupply,
			"condition failed: distributeTotal(%d) + MinimumBalanceOnBoostrapSequencer(%d) < InitialSupply(%d)",
			distributeTotal, lib.MinimumAmountOnSequencer, constants.InitialSupply)
		if err != nil {
			return nil, err
		}
	}

	rdr, err := multistate.NewSugaredReadableState(stateStore, genesisRoot)
	if err != nil {
		return nil, err
	}

	genesisStem := rdr.GetStemOutput()
	ts := base.T(genesisStem.Timestamp().Slot+1, 0)
	genesisDistributionOutputs := make([]*ledger.Output, len(genesisDistribution))
	for i := range genesisDistribution {
		genesisDistributionOutputs[i] = ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(int64(genesisDistribution[i].Balance)).
				WithLock(genesisDistribution[i].Lock)
			if genesisDistribution[i].ChainOrigin {
				o.MustPushConstraint(ledger.NewChainOrigin(ts.Slot, genesisDistribution[i].Balance).Bytes())
			}
		})
	}

	bootstrapChainID := ledger.OriginChainID()
	initSupplyOutput, err := rdr.GetChainOutputWithID(bootstrapChainID)
	if err != nil {
		return nil, err
	}

	// create origin branch transaction at the next slot after genesis time slot
	txBytes, err := MakeSimpleSequencerTransaction(MakeSimpleSequencerTransactionParams{
		ChainInput: &ledger.OutputWithChainID{
			OutputWithID: *initSupplyOutput,
			ChainConstraintData: ledger.ChainConstraintData{
				ChainConstraint: ledger.ChainConstraint{
					ChainID:      base.BoostrapSequencerID,
					OriginAmount: initSupplyOutput.Output.TokenBalance(),
				},
				ChainConstraintIndex: 2,
			},
		},
		StemInput:             genesisStem,
		Timestamp:             ts,
		WithdrawOutputs:       genesisDistributionOutputs,
		SignatureType:         base.SignatureTypeED25519,
		PrivateKey:            originPrivateKey,
		PublicKey:             originPrivateKey.Public().(ed25519.PublicKey),
		DoNotInflateMainChain: false,
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
func DistributeInitialSupply(stateStore global.Store, originPrivateKey ed25519.PrivateKey, genesisDistribution []ledger.LockBalance) ([]byte, error) {
	txBytes, _, err := DistributeInitialSupplyExt(stateStore, originPrivateKey, genesisDistribution)
	return txBytes, err
}

func DistributeInitialSupplyExt(stateStore global.Store, originPrivateKey ed25519.PrivateKey, genesisDistribution []ledger.LockBalance) ([]byte, base.TransactionID, error) {
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
func MustDistributeInitialSupply(stateStore global.Store, originPrivateKey ed25519.PrivateKey, genesisDistribution []ledger.LockBalance) []byte {
	ret, _ := MustDistributeInitialSupplyExt(stateStore, originPrivateKey, genesisDistribution)
	return ret
}

// MustDistributeInitialSupplyExt makes a distribution transaction and commits it into the multi-ledger state with branch record
func MustDistributeInitialSupplyExt(stateStore global.Store, originPrivateKey ed25519.PrivateKey, genesisDistribution []ledger.LockBalance) ([]byte, base.TransactionID) {
	txBytes, err := MakeDistributionTransaction(stateStore, originPrivateKey, genesisDistribution)
	util.AssertNoError(err)

	stateID, genesisRoot, err := multistate.ScanGenesisState(stateStore)
	util.AssertNoError(err)

	rdr := multistate.MustNewSugaredReadableState(stateStore, genesisRoot)
	bootstrapChainID := ledger.OriginChainID()

	tx, err := transaction.Parse(txBytes)
	util.AssertNoError(err)

	err = tx.SetFullContext(tx.InputLoaderFromState(rdr))
	util.Assertf(err == nil, "%v\n>>>>>>>>>>>>>>>>> %s\n<<<<<<<<<<<<<\n", err, tx.String)

	err = tx.ValidateFullContext()
	util.Assertf(err == nil, "%v\n>>>>>>>>>>>>>>>>> %s\n<<<<<<<<<<<<<\n", err, tx.String)

	// extract branch inflation from the sequencer output
	seqData := tx.SequencerTransactionData()
	util.Assertf(seqData != nil, "expected sequencer transaction")
	seqOut := tx.MustProducedOutputWithIDAt(seqData.SequencerOutputIndex)
	branchInflation := seqOut.Output.Inflation()

	nextStem := tx.FindStemProducedOutput()
	util.Assertf(nextStem != nil, "nextStem != nil")
	muts := tx.StateMutations()

	updatableOrigin := multistate.MustNewUpdatable(stateStore, genesisRoot)
	updatableOrigin.MustUpdate(muts, &multistate.RootRecordParams{
		StemOutputID:    nextStem.ID,
		SeqID:           bootstrapChainID,
		CoverageDelta:   stateID.InitialSupply,
		SlotInflation:   branchInflation,
		Supply:          stateID.InitialSupply + branchInflation,
		NumTransactions: 1,
	})
	return txBytes, tx.ID()
}
