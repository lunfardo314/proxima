package txbuilder

import (
	"crypto/ed25519"
	"fmt"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/genesis"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"gopkg.in/yaml.v2"
)

type LockBalanceYAMLable struct {
	LockString string `yaml:"lock"`
	Balance    uint64 `yaml:"balance"`
}

func MustDistributeInitialSupply(stateStore global.StateStore, originPrivateKey ed25519.PrivateKey, genesisDistribution []core.LockBalance) []byte {
	ret, _ := MustDistributeInitialSupplyExt(stateStore, originPrivateKey, genesisDistribution)
	return ret
}

func MustDistributeInitialSupplyExt(stateStore global.StateStore, originPrivateKey ed25519.PrivateKey, genesisDistribution []core.LockBalance) ([]byte, core.TransactionID) {
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

	//fmt.Printf("=======================\n%s=======================\n", tx.Lines(tx.InputLoaderFromState(rdr)).String())

	err = tx.Validate(transaction.ValidateOptionWithFullContext(tx.InputLoaderFromState(rdr)))
	util.AssertNoError(err)

	nextStem := tx.FindStemProducedOutput()
	util.Assertf(nextStem != nil, "nextStem != nil")
	muts := tx.StateMutations()

	updatableOrigin := multistate.MustNewUpdatable(stateStore, genesisRoot)
	var coverage multistate.LedgerCoverage
	updatableOrigin.MustUpdate(muts, &nextStem.ID, &bootstrapChainID, coverage.MakeNext(1, stateID.InitialSupply))

	return txBytes, *tx.ID()
}

// DistributeInitialSupply updates genesis state and branch records according to initial supply distribution parameters by
// adding initial distribution transaction.
// Distribution transaction is a branch transaction in the slot next after the genesis.
// Distribution parameter is added to the transaction store
func DistributeInitialSupply(stateStore global.StateStore, originPrivateKey ed25519.PrivateKey, genesisDistribution []core.LockBalance) ([]byte, error) {
	txBytes, _, err := DistributeInitialSupplyExt(stateStore, originPrivateKey, genesisDistribution)
	return txBytes, err
}

func DistributeInitialSupplyExt(stateStore global.StateStore, originPrivateKey ed25519.PrivateKey, genesisDistribution []core.LockBalance) ([]byte, core.TransactionID, error) {
	var ret []byte
	var txid core.TransactionID
	err := util.CatchPanicOrError(func() error {
		ret, txid = MustDistributeInitialSupplyExt(stateStore, originPrivateKey, genesisDistribution)
		return nil
	})
	if err != nil {
		return nil, core.TransactionID{}, fmt.Errorf("DistributeInitialSupply: %v", err)
	}
	return ret, txid, nil
}

func InitialDistributionFromYAMLData(yamlData []byte) ([]core.LockBalance, error) {
	yamlAble := make([]LockBalanceYAMLable, 0)
	if err := yaml.Unmarshal(yamlData, &yamlAble); err != nil {
		return nil, err
	}
	ret := make([]core.LockBalance, 0, len(yamlAble))
	for i := range yamlAble {
		lck, err := core.LockFromSource(yamlAble[i].LockString)
		if err != nil {
			return nil, err
		}
		ret = append(ret, core.LockBalance{
			Lock:    lck,
			Balance: yamlAble[i].Balance,
		})
	}
	return ret, nil
}

func DistributionListToLines(lst []core.LockBalance, prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	for i := range lst {
		ret.Add("%s : %s", lst[i].Lock.String(), util.GoThousands(lst[i].Balance))
	}
	return ret
}
