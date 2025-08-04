package util_cmd

import (
	"encoding/hex"
	"fmt"
	"os"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func compileIDCmd() *cobra.Command {
	validateLedgerIDCmd := &cobra.Command{
		Use:   "compile_ledger_id",
		Args:  cobra.NoArgs,
		Short: fmt.Sprintf("(re)compiles ledger ChainID and recalculates hash'%s'", glb.LedgerIDFileName),
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			glb.ReadInConfig()
		},
		Run: runGenCompileLedgerIDCommand,
	}
	validateLedgerIDCmd.PersistentFlags().StringP("config", "c", "", "profile name")
	err := viper.BindPFlag("config", validateLedgerIDCmd.PersistentFlags().Lookup("config"))
	glb.AssertNoError(err)

	return validateLedgerIDCmd
}

func runGenCompileLedgerIDCommand(_ *cobra.Command, _ []string) {
	glb.Assertf(glb.FileExists(glb.LedgerIDFileName), "file %s does not exist", glb.LedgerIDFileName)
	yamlData, err := os.ReadFile(glb.LedgerIDFileName)
	glb.AssertNoError(err)

	fromYAML, err := easyfl.ReadLibraryFromYAML(yamlData)
	glb.AssertNoError(err)

	if len(fromYAML.Hash) > 0 {
		glb.Infof("ledger ChainID data is already compiled. Will recompile it..")
	}

	lib := easyfl.NewLibrary[*ledger.EvalContext]()
	err = lib.UpgradeFromYAML(yamlData, ledger.GetEmbeddedFunctionResolver(lib))
	glb.AssertNoError(err)

	h := lib.LibraryHash()
	glb.Infof("new library hash is: %s", hex.EncodeToString(h[:]))

	yamlData1 := lib.ToYAML(true, "# compiled library of Proxima ledger definitions")

	err = os.WriteFile(glb.LedgerIDFileName, yamlData1, 0755)
	glb.AssertNoError(err)
}
