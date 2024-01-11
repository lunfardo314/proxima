package init_cmd

import (
	"os"

	"github.com/dgraph-io/badger/v4"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/adaptors/badger_adaptor"
	"github.com/spf13/cobra"
)

func initDistributeDBCmd() *cobra.Command {
	distributeCmd := &cobra.Command{
		Use:   "genesis_distribute",
		Short: "writes genesis distribution transaction into the DB according to 'proxi.genesis.distribute.yaml'",
		Long: `writes genesis distribution transaction into the DB. Distribution data is take from the file
'proxi.genesis.distribute.yaml'.
`,
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			glb.ReadInConfig()
		},
		Args: cobra.NoArgs,
		Run:  runDistribute,
	}
	return distributeCmd
}

func runDistribute(_ *cobra.Command, _ []string) {
	privKey := glb.MustGetPrivateKey()
	distributionDataYAML, err := os.ReadFile(genesisDistributionFileName)
	glb.AssertNoError(err)
	distributionList, err := txbuilder.InitialDistributionFromYAMLData(distributionDataYAML)
	glb.AssertNoError(err)

	glb.Infof("Genesis supply will be distributed the following way with remainder in the bootstrap chain:")
	glb.Infof(txbuilder.DistributionListToLines(distributionList, "     ").String())

	if !glb.YesNoPrompt("Proceed?", true) {
		glb.Fatalf("exit: distribution transaction wasn't created")
	}

	stateDb := badger_adaptor.MustCreateOrOpenBadgerDB(global.MultiStateDBName, badger.DefaultOptions(global.MultiStateDBName))
	stateStore := badger_adaptor.New(stateDb)
	defer func() { _ = stateDb.Close() }()

	// run initial distribution
	txStoreDB := badger_adaptor.New(badger_adaptor.MustCreateOrOpenBadgerDB(global.TxStoreDBName, badger.DefaultOptions(global.TxStoreDBName)))
	txStore := txstore.NewSimpleTxBytesStore(txStoreDB)
	defer func() { _ = txStoreDB.Close() }()

	txBytesDistribution, txid, err := txbuilder.DistributeInitialSupplyExt(stateStore, privKey, distributionList)
	glb.AssertNoError(err)

	err = txStore.SaveTxBytes(txBytesDistribution)
	glb.AssertNoError(err)
	util.Assertf(len(txStore.GetTxBytes(&txid)) > 0, "inconsistency: stored transaction has not been found")

	glb.Infof("Success. Genesis distribution transaction %s has been stored in the transaction store DB", txid.String())
}
