package util_cmd

import (
	"encoding/hex"
	"fmt"
	"os"
	"time"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func genIDCmd() *cobra.Command {
	initLedgerIDCmd := &cobra.Command{
		Use:   "ledger_definitions",
		Args:  cobra.NoArgs,
		Short: fmt.Sprintf("creates default ledger definitions with genesis controller taken from proxi wallet. Saves definitions to the file '%s'", glb.LedgerDefinitionsFileName),
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			glb.ReadInConfig()
		},
		Run: runGenLedgerIDCommand,
	}
	initLedgerIDCmd.PersistentFlags().StringP("config", "c", "", "profile name")
	err := viper.BindPFlag("config", initLedgerIDCmd.PersistentFlags().Lookup("config"))
	glb.AssertNoError(err)

	return initLedgerIDCmd
}

func runGenLedgerIDCommand(_ *cobra.Command, _ []string) {
	if glb.FileExists(glb.LedgerDefinitionsFileName) {
		if !glb.YesNoPrompt(fmt.Sprintf("file '%s' already exists. Overwrite?", glb.LedgerDefinitionsFileName), false) {
			os.Exit(0)
		}
	}
	privKey := glb.MustGetPrivateKey()

	// create ledger identity
	params := ledger.DefaultParameters(privKey, uint32(time.Now().Unix()))
	yamlData := ledger.LibraryYAMLFromParameters(params, true)
	lib, err := easyfl.NewLibraryFromYAML[*ledger.EvalContext](yamlData, ledger.GetEmbeddedFunctionResolver)
	glb.AssertNoError(err)

	err = os.WriteFile(glb.LedgerDefinitionsFileName, yamlData, 0666)
	glb.AssertNoError(err)
	glb.Infof("new ledger identity data has been stored in the file '%s'", glb.LedgerDefinitionsFileName)
	h := lib.LibraryHash()
	glb.Infof("calculated library hash: %s", hex.EncodeToString(h[:]))
	constants := ledger.ConstantsFromLibrary(lib)
	glb.Infof("main ledger constants:\n--------------\n%s\n", constants.String())
}
