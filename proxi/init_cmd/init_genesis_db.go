package init_cmd

import (
	"os"

	"github.com/dgraph-io/badger/v4"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/unitrie/adaptors/badger_adaptor"
	"github.com/spf13/cobra"
)

func initGenesisDBCmd() *cobra.Command {
	genesisCmd := &cobra.Command{
		Use:   "genesis_db",
		Short: "creates genesis ledger state and transaction store databases from ledger ID data in 'proxi.genesis.id.yaml'",
		Args:  cobra.NoArgs,
		Run:   runGenesis,
	}
	return genesisCmd
}

func runGenesis(_ *cobra.Command, _ []string) {
	glb.FileMustNotExist(global.MultiStateDBName)
	glb.FileMustNotExist(global.TxStoreDBName)

	idDataYAML, err := os.ReadFile(ledgerIDFileName)
	glb.AssertNoError(err)
	idData, err := ledger.StateIdentityDataFromYAML(idDataYAML)
	glb.AssertNoError(err)

	ledger.Init(idData)

	glb.Infof("Will be creating genesis from the ledger identity data:")
	glb.Infof(idData.Lines("      ").String())
	glb.Infof("Multi-state database name: '%s'", global.MultiStateDBName)
	glb.Infof("Transaction store database name: '%s'", global.TxStoreDBName)

	if !glb.YesNoPrompt("Proceed?", true) {
		glb.Fatalf("exit: genesis database wasn't created")
	}

	// initialize genesis state

	stateDb := badger_adaptor.MustCreateOrOpenBadgerDB(global.MultiStateDBName, badger.DefaultOptions(global.MultiStateDBName))
	stateStore := badger_adaptor.New(stateDb)
	defer func() { _ = stateStore.Close() }()

	bootstrapChainID, _ := multistate.InitStateStore(*idData, stateStore)
	glb.Infof("Genesis state DB '%s' has been created successfully.\nBootstrap sequencer chainID: %s", global.MultiStateDBName, bootstrapChainID.String())

	txStore := badger_adaptor.New(badger_adaptor.MustCreateOrOpenBadgerDB(global.TxStoreDBName, badger.DefaultOptions(global.TxStoreDBName)))
	glb.Infof("Transaction store DB '%s' has been created successfully", global.TxStoreDBName)
	defer func() { _ = txStore.Close() }()
}
