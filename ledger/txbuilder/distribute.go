package txbuilder

import (
	"crypto/ed25519"
	"fmt"

	"github.com/lunfardo314/proxima/genesis"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"gopkg.in/yaml.v2"
)

type LockBalanceYAMLable struct {
	LockString string `yaml:"lock"`
	Balance    uint64 `yaml:"balance"`
}

func MustDistributeInitialSupply(stateStore global.StateStore, originPrivateKey ed25519.PrivateKey, genesisDistribution []ledger.LockBalance) []byte {
	ret, _ := MustDistributeInitialSupplyExt(stateStore, originPrivateKey, genesisDistribution)
	return ret
}

func MakeDistributionTransaction(stateStore global.StateStore, originPrivateKey ed25519.PrivateKey, genesisDistribution []ledger.LockBalance) ([]byte, error) {
	stateID, genesisRoot, err := genesis.ScanGenesisState(stateStore)
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
		err = util.ErrorConditionf(distributeTotal+ledger.MinimumAmountOnSequencer <= stateID.InitialSupply,
			"condition failed: distributeTotal(%d) + MinimumBalanceOnBoostrapSequencer(%d) < InitialSupply(%d)",
			distributeTotal, ledger.MinimumAmountOnSequencer, stateID.InitialSupply)
		if err != nil {
			return nil, err
		}
	}
	genesisDistributionOutputs := make([]*ledger.Output, len(genesisDistribution))
	for i := range genesisDistribution {
		genesisDistributionOutputs[i] = ledger.NewOutput(func(o *ledger.Output) {
			o.WithAmount(genesisDistribution[i].Balance).
				WithLock(genesisDistribution[i].Lock)
		})
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
		StemInput:          genesisStem,
		Timestamp:          ledger.MustNewLedgerTime(genesisStem.Timestamp().Slot()+1, 0),
		MinimumFee:         0,
		AdditionalInputs:   nil,
		AdditionalOutputs:  genesisDistributionOutputs,
		Endorsements:       nil,
		PrivateKey:         originPrivateKey,
		TotalSupply:        stateID.InitialSupply,
		DoNotInflateBranch: true,
	})
	if err != nil {
		return nil, err
	}
	return txBytes, nil
}

func MustDistributeInitialSupplyExt(stateStore global.StateStore, originPrivateKey ed25519.PrivateKey, genesisDistribution []ledger.LockBalance) ([]byte, ledger.TransactionID) {
	txBytes, err := MakeDistributionTransaction(stateStore, originPrivateKey, genesisDistribution)
	util.AssertNoError(err)

	stateID, genesisRoot, err := genesis.ScanGenesisState(stateStore)
	util.AssertNoError(err)

	rdr := multistate.MustNewSugaredReadableState(stateStore, genesisRoot)
	bootstrapChainID := stateID.OriginChainID()

	tx, err := transaction.FromBytesMainChecksWithOpt(txBytes)
	util.AssertNoError(err)

	err = tx.Validate(transaction.ValidateOptionWithFullContext(tx.InputLoaderFromState(rdr)))
	if err != nil {
		fmt.Printf(">>>>>>>>>>>>>>>>> %s\n<<<<<<<<<<<<<\n", tx.String())
	}
	util.AssertNoError(err)

	nextStem := tx.FindStemProducedOutput()
	util.Assertf(nextStem != nil, "nextStem != nil")
	muts := tx.StateMutations()

	updatableOrigin := multistate.MustNewUpdatable(stateStore, genesisRoot)
	updatableOrigin.MustUpdate(muts, &multistate.RootRecordParams{
		StemOutputID:  nextStem.ID,
		SeqID:         bootstrapChainID,
		Coverage:      multistate.LedgerCoverage{0, stateID.InitialSupply},
		SlotInflation: 0,
		Supply:        stateID.InitialSupply,
	})
	return txBytes, *tx.ID()
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

func InitialDistributionFromYAMLData(yamlData []byte) ([]ledger.LockBalance, error) {
	yamlAble := make([]LockBalanceYAMLable, 0)
	if err := yaml.Unmarshal(yamlData, &yamlAble); err != nil {
		return nil, err
	}
	ret := make([]ledger.LockBalance, 0, len(yamlAble))
	for i := range yamlAble {
		lck, err := ledger.LockFromSource(yamlAble[i].LockString)
		if err != nil {
			return nil, err
		}
		ret = append(ret, ledger.LockBalance{
			Lock:    lck,
			Balance: yamlAble[i].Balance,
		})
	}
	return ret, nil
}

func DistributionListToLines(lst []ledger.LockBalance, prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	for i := range lst {
		ret.Add("%s : %s", lst[i].Lock.String(), util.GoTh(lst[i].Balance))
	}
	return ret
}
