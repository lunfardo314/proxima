package init_cmd

import (
	"os"
	"strconv"

	"github.com/dgraph-io/badger/v4"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/adaptors/badger_adaptor"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v2"
)

func initBootstrapAccountCmd() *cobra.Command {
	bootstrapAccountCmd := &cobra.Command{
		Use:   "bootstrap_account [amount, default 1_000_000]",
		Short: "creates transaction store DB. Creates bootstrap output on bootstrap address with specified amount",
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			glb.ReadInConfig()
		},
		Args: cobra.MaximumNArgs(1),
		Run:  runBootstrapAccount,
	}
	return bootstrapAccountCmd
}

const (
	defaultBootstrapBalance = uint64(1_000_000)
	minimumBootstrapBalance = uint64(10_000)
)

func runBootstrapAccount(_ *cobra.Command, args []string) {
	// initialize ledger
	glb.FileMustExist(global.MultiStateDBName)
	stateDb := badger_adaptor.MustCreateOrOpenBadgerDB(global.MultiStateDBName, badger.DefaultOptions(global.MultiStateDBName))
	stateStore := badger_adaptor.New(stateDb)
	defer func() { _ = stateDb.Close() }()

	multistate.InitLedgerFromStore(stateStore)
	privKey := glb.MustGetPrivateKey()

	// create transaction store database
	glb.FileMustNotExist(global.TxStoreDBName)
	glb.Infof("Transaction store database name: '%s'", global.TxStoreDBName)

	bootstrapBalance := defaultBootstrapBalance
	if len(args) > 0 {
		b, err := strconv.Atoi(args[0])
		glb.AssertNoError(err)
		bootstrapBalance = uint64(b)
	}
	if bootstrapBalance < minimumBootstrapBalance {
		glb.Fatalf("bootstrap account balance must be at least %s", util.Th(minimumBootstrapBalance))
	}
	addr := glb.GetWalletData().Account
	glb.Infof("Transaction store DB will be created. %s tokens from the bootstrap chain will be sent to bootstrap address %s", util.Th(bootstrapBalance), addr.String())
	if !glb.YesNoPrompt("Proceed?", true) {
		glb.Fatalf("exit: bootstrap account wasn't created")
	}

	txStoreDB := badger_adaptor.New(badger_adaptor.MustCreateOrOpenBadgerDB(global.TxStoreDBName, badger.DefaultOptions(global.TxStoreDBName)))
	txStore := txstore.NewSimpleTxBytesStore(txStoreDB)
	defer func() { _ = txStoreDB.Close() }()

	// distributed genesis balance by sending bootstrapBalance to the same address, which controls bootstrap chain
	// Remainder stays in genesis
	// It commits right to the database.
	txBytesBootstrapBalance, txid, err := txbuilder.DistributeInitialSupplyExt(stateStore, privKey,
		getAdditionalDistribution([]ledger.LockBalance{
			{Lock: addr, Balance: bootstrapBalance, ChainBalance: 0},
		}))
	glb.AssertNoError(err)

	// store distribution transaction in the ts store so that other nodes
	// could sync the state right from the genesis state at slot 0
	rr, found := multistate.FetchRootRecord(stateStore, txid)
	glb.Assertf(found, "inconsistency: can't find root record")

	_, err = txStore.PersistTxBytesWithMetadata(txBytesBootstrapBalance, &txmetadata.TransactionMetadata{
		StateRoot:      rr.Root,
		LedgerCoverage: util.Ref(rr.LedgerCoverage),
		SlotInflation:  util.Ref(rr.SlotInflation),
		Supply:         util.Ref(rr.Supply),
	})
	glb.AssertNoError(err)
	util.Assertf(len(txStore.GetTxBytesWithMetadata(&txid)) > 0, "inconsistency: just stored transaction has not been found")

	glb.Infof("Success. Bootstrap account has been created in the transaction %s", txid.String())
}

// this function tries to open distribution.yaml and if found parses the content and adds additional found account balances to the genesisDistribution
// Format of distribution.yaml:
// distribution:
//   - account: "addressED25519(0xaa401c8c6a9deacf479ab2209c07c01a27bd1eeecf0d7eaa4180b8049c6190d0)"
//     amount: 200000000000000
//     chain:  100000000000000
//   - account: "addressED25519(0x62c733803a83a26d4db1ce9f22206281f64af69401da6eb26390d34e6a88c5fa)"
//     amount: 200000000000000
//     chain:  0
func getAdditionalDistribution(genesisDistribution []ledger.LockBalance) []ledger.LockBalance {

	type (
		// Item represents a single item with an account and an amount
		DistItem struct {
			Account string `yaml:"account"`
			Amount  uint64 `yaml:"amount"`
			Chain   uint64 `yaml:"chain"`
		}

		// Data represents the structure of the YAML file
		Data struct {
			Distribution []DistItem `yaml:"distribution"`
		} // Read the YAML file
	)

	data, err := os.ReadFile("distribution.yaml")
	if err != nil {
		// no error, distribution.yaml is optional
		return genesisDistribution
	}

	// Parse the YAML file
	var parsedData Data
	err = yaml.Unmarshal(data, &parsedData)
	glb.AssertNoError(err)

	for _, item := range parsedData.Distribution {
		account, err := ledger.AddressED25519FromSource(item.Account)
		glb.AssertNoError(err)
		genesisDistribution = append(genesisDistribution, ledger.LockBalance{Lock: account, Balance: item.Amount, ChainBalance: item.Chain})
	}
	return genesisDistribution
}
