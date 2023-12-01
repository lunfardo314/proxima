package init_cmd

import (
	"os"

	"github.com/dgraph-io/badger/v4"
	"github.com/lunfardo314/proxima/genesis"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/txbuilder"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/lunfardo314/unitrie/adaptors/badger_adaptor"
	"github.com/spf13/cobra"
)

func initGenesisDBCmd() *cobra.Command {
	genesisCmd := &cobra.Command{
		Use:   "genesis_db",
		Short: "creates genesis ledger state and transaction store databases from the provided ledger ID data",
		Long: `creates genesis ledger state database 'proximadb' and transaction store database 'proximadb.txstore' 
from the ledger ID data provided in the file 'proxi.genesis.id.yaml'.
It does not require genesis controller's private key'
`,
		Args: cobra.NoArgs,
		Run:  runGenesis,
	}
	return genesisCmd
}

func runGenesis(_ *cobra.Command, _ []string) {
	mustNotExist(global.MultiStateDBName)
	mustNotExist(global.TxStoreDBName)

	idDataYAML, err := os.ReadFile(ledgerIDFileName)
	glb.AssertNoError(err)
	idData, err := genesis.StateIdentityDataFromYAML(idDataYAML)
	glb.AssertNoError(err)

	distributionDataYAML, err := os.ReadFile(initialDistributionFileName)
	glb.AssertNoError(err)
	distributionList, err := txbuilder.InitialDistributionFromYAMLData(distributionDataYAML)
	glb.AssertNoError(err)

	wallet := glb.GetWalletData()

	glb.Infof("Will be creating genesis from the ledger identity data:")
	glb.Infof(idData.Lines("      ").String())
	glb.Infof("Multi-state database name: '%s'", global.MultiStateDBName)
	glb.Infof("Transaction store database name: '%s'", global.TxStoreDBName)
	glb.Infof("Supply will be distributed the following way (remainder will stay on the bootstrap chain):")
	glb.Infof(txbuilder.DistributionListToLines(distributionList, "     ").String())

	if !glb.YesNoPrompt("Proceed?", true) {
		glb.Fatalf("exit: genesis database wasn't created")
	}

	// initialize genesis state

	stateDb := badger_adaptor.MustCreateOrOpenBadgerDB(global.MultiStateDBName, badger.DefaultOptions(global.MultiStateDBName))
	stateStore := badger_adaptor.New(stateDb)

	bootstrapChainID, _ := genesis.InitLedgerState(*idData, stateStore)
	glb.AssertNoError(stateDb.Close())
	glb.Infof("Genesis state DB '%s' has been created successfully.\nBootstrap sequencer chainID: %s", global.MultiStateDBName, bootstrapChainID.String())

	// run initial distribution

	txStoreDB := badger_adaptor.New(badger_adaptor.MustCreateOrOpenBadgerDB(global.TxStoreDBName, badger.DefaultOptions(global.TxStoreDBName)))
	glb.Infof("Transaction store DB '%s' has been created successfully", global.TxStoreDBName)
	txStore := txstore.NewSimpleTxBytesStore(txStoreDB)

	distribTxBytes, err := txbuilder.DistributeInitialSupply(stateStore, wallet.PrivateKey, distributionList)
	glb.AssertNoError(txStoreDB.Close())

	err = txStore.SaveTxBytes(distribTxBytes)
	glb.AssertNoError(err)
	glb.AssertNoError(txStoreDB.Close())
}

func mustNotExist(dir string) {
	_, err := os.Stat(dir)
	if err == nil {
		glb.Fatalf("'%s' already exists", dir)
	} else {
		if !os.IsNotExist(err) {
			glb.AssertNoError(err)
		}
	}
}

func fileExists(name string) bool {
	_, err := os.Stat(name)
	return !os.IsNotExist(err)
}
