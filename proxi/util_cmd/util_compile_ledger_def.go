package util_cmd

import (
	"fmt"
	"os"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func compileIDCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "compile_ledger_definitions",
		Args:  cobra.NoArgs,
		Short: fmt.Sprintf("(re)compiles ledger definition from %s and recalculates library hash", glb.LedgerDefinitionsFileName),
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			glb.ReadInConfig()
		},
		Run: runGenCompileLedgerIDCommand,
	}
	// 'config' / '-c' is inherited from the root command (see proxi/main.go).
	return cmd
}

func runGenCompileLedgerIDCommand(_ *cobra.Command, _ []string) {
	glb.Assertf(glb.FileExists(glb.LedgerDefinitionsFileName), "file %s does not exist", glb.LedgerDefinitionsFileName)
	jsonData, err := os.ReadFile(glb.LedgerDefinitionsFileName)
	glb.AssertNoError(err)

	fromJSON, err := easyfl.ReadLibraryFromJSON(jsonData)
	glb.AssertNoError(err)

	if len(fromJSON.Hash) > 0 {
		glb.Infof("ledger definition in %s are already compiled, library hash %s", glb.LedgerDefinitionsFileName, fromJSON.Hash)
		prompt := fmt.Sprintf("recompile the library to the same file %s?", glb.LedgerDefinitionsFileName)
		if !glb.YesNoPrompt(prompt, true) {
			return
		}
	}

	lib := easyfl.NewLibrary[*ledger.EvalContext]()
	err = easyfl.UpgradeFromJSON(lib, jsonData, ledger.GetEmbeddedFunctionResolver(lib))
	glb.AssertNoError(err)

	// indented JSON for human readability in the on-disk definitions file
	jsonOut := easyfl.ToJSON(lib, true, true)

	err = os.WriteFile(glb.LedgerDefinitionsFileName, jsonOut, 0755)
	glb.AssertNoError(err)

	glb.Infof("---- main library constants:\n%s", ledger.ConstantsStringFromLibrary(lib))
}
