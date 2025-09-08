package util_cmd

import (
	"fmt"
	"os"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func compileIDCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "compile_ledger_id",
		Args:  cobra.NoArgs,
		Short: fmt.Sprintf("(re)compiles ledger definition from %s and recalculates library hash", glb.LedgerIDFileName),
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			glb.ReadInConfig()
		},
		Run: runGenCompileLedgerIDCommand,
	}
	cmd.PersistentFlags().StringP("config", "c", "", "profile name")
	err := viper.BindPFlag("config", cmd.PersistentFlags().Lookup("config"))
	glb.AssertNoError(err)

	return cmd
}

func runGenCompileLedgerIDCommand(_ *cobra.Command, _ []string) {
	glb.Assertf(glb.FileExists(glb.LedgerIDFileName), "file %s does not exist", glb.LedgerIDFileName)
	yamlData, err := os.ReadFile(glb.LedgerIDFileName)
	glb.AssertNoError(err)

	fromYAML, err := easyfl.ReadLibraryFromYAML(yamlData)
	glb.AssertNoError(err)

	if len(fromYAML.Hash) > 0 {
		glb.Infof("ledger definition in %s are already compiled, library hash %s", glb.LedgerIDFileName, fromYAML.Hash)
		prompt := fmt.Sprintf("recompile the library to the same file %s?", glb.LedgerIDFileName)
		if !glb.YesNoPrompt(prompt, true) {
			return
		}
	}

	lib := easyfl.NewLibrary[*ledger.EvalContext]()
	err = lib.UpgradeFromYAML(yamlData, ledger.GetEmbeddedFunctionResolver(lib))
	glb.AssertNoError(err)

	yamlData1 := lib.ToYAML(true, "# compiled library of Proxima ledger definitions")

	err = os.WriteFile(glb.LedgerIDFileName, yamlData1, 0755)
	glb.AssertNoError(err)

	constants := ledger.ConstantsFromLibrary(lib)
	glb.Infof("---- main library constants:\n%s", constants.String())
}
