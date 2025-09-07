package init_cmd

import (
	"encoding/hex"
	"fmt"
	"os"

	"github.com/dgraph-io/badger/v4"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/unitrie/adaptors/badger_adaptor"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var remoteEndpoint string

func initGenesisDBCmd() *cobra.Command {
	genesisCmd := &cobra.Command{
		Use: "genesis_db",
		Short: fmt.Sprintf("creates multi-state DB and initializes genesis ledger state init according "+
			"ledger id data taken either from file '%s' (default) or from another API endpoint specified with flag -r", glb.LedgerIDFileName),
		Args: cobra.NoArgs,
		Run:  runGenesis,
	}
	genesisCmd.PersistentFlags().StringVarP(&remoteEndpoint, "remote", "r", "", "remote API endpoint to fetch ledger identity data from")
	err := viper.BindPFlag("remote", genesisCmd.PersistentFlags().Lookup("remote"))
	glb.AssertNoError(err)

	return genesisCmd
}

func runGenesis(_ *cobra.Command, _ []string) {
	glb.DirMustNotExistOrBeEmpty(global.MultiStateDBName)

	var err error
	var idDataYAML []byte

	if remoteEndpoint != "" {
		glb.ReadInConfig()
		glb.Infof("retrieving ledger identity data from '%s'", remoteEndpoint)
		idDataYAML, err = glb.GetClient(remoteEndpoint).GetLedgerIdentityData()
		glb.AssertNoError(err)
	} else {
		glb.Infof("reading ledger identity data from file '%s'", glb.LedgerIDFileName)
		// take ledger id data from the 'proxi.genesis.id.yaml'
		idDataYAML, err = os.ReadFile(glb.LedgerIDFileName)
		glb.AssertNoError(err)
	}

	// parse and validate
	lib, err := ledger.ParseLibraryFromYAML(idDataYAML, ledger.GetEmbeddedFunctionResolver)
	glb.AssertNoError(err)
	constants := ledger.ConstantsFromLibrary(lib)

	glb.Infof("Will be creating genesis with the following ledger identity parameters:")
	glb.Infof(constants.String())
	h := lib.LibraryHash()
	glb.Infof("library hash: %s", hex.EncodeToString(h[:]))
	glb.Infof("Multi-state database name: '%s'", global.MultiStateDBName)

	if !glb.YesNoPrompt("Proceed?", true) {
		glb.Fatalf("exit: genesis database wasn't created")
	}

	// create state store and initialize genesis state
	stateDb := badger_adaptor.MustCreateOrOpenBadgerDB(global.MultiStateDBName, badger.DefaultOptions(global.MultiStateDBName))
	stateStore := badger_adaptor.New(stateDb)
	defer func() { _ = stateStore.Close() }()

	ledger.MustInitSingleton(idDataYAML)

	bootstrapChainID, _ := multistate.InitStateStoreFromGlobals(stateStore)
	glb.Infof("Genesis state DB '%s' has been created successfully.\nBootstrap sequencer chainID: %s", global.MultiStateDBName, bootstrapChainID.String())
}
