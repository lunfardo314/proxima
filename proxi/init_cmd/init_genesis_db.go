package init_cmd

import (
	"os"

	"github.com/dgraph-io/badger/v4"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/genesis"
	"github.com/lunfardo314/proxima/proxi_old/glb"
	"github.com/lunfardo314/unitrie/adaptors/badger_adaptor"
	"github.com/spf13/cobra"
)

func initGenesisDBCmd() *cobra.Command {
	genesisCmd := &cobra.Command{
		Use:   "genesis_db",
		Short: "creates genesis ledger state and transaction store databases from the provided ledger ID data",
		Long: `creates genesis ledger state database 'proximadb' and transaction store database 'proximadb.txstore' 
from the ledger ID data provided in the file 'proxima.id.yaml'.
It does not require genesis controller's private key'
`,
		Args: cobra.NoArgs,
		Run:  runGenesis,
	}
	return genesisCmd
}

func runGenesis(_ *cobra.Command, _ []string) {
	mustNotExist(general.MultiStateDBName)
	mustNotExist(general.TxStoreDBName)

	idDataYAML, err := os.ReadFile(ledgerIDFileName)
	glb.AssertNoError(err)

	idData, err := genesis.StateIdentityDataFromYAML(idDataYAML)
	glb.AssertNoError(err)

	glb.Infof("Will be creating genesis from the ledger identity data:")
	glb.Infof(idData.Lines("      ").String())
	glb.Infof("Multi-state database name: '%s'", general.MultiStateDBName)
	glb.Infof("Transaction store database name: '%s'", general.TxStoreDBName)
	if !glb.YesNoPrompt("Proceed?", true) {
		glb.Fatalf("exit: genesis database wasn't created")
	}
	stateDb := badger_adaptor.MustCreateOrOpenBadgerDB(general.MultiStateDBName, badger.DefaultOptions(general.MultiStateDBName))
	stateStore := badger_adaptor.New(stateDb)

	bootstrapChainID, _ := genesis.InitLedgerState(*idData, stateStore)
	glb.AssertNoError(stateDb.Close())

	glb.Infof("Genesis state DB '%s' has been created successfully.\nBootstrap sequencer chainID: %s", general.MultiStateDBName, bootstrapChainID.String())

	txStoreDB := badger_adaptor.MustCreateOrOpenBadgerDB(general.TxStoreDBName, badger.DefaultOptions(general.TxStoreDBName))
	glb.AssertNoError(txStoreDB.Close())

	glb.Infof("Transaction store DB '%s' has been created successfully", general.TxStoreDBName)
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
