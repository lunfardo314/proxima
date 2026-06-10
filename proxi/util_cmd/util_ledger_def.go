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
	// 'config' / '-c' is inherited from the root command (see proxi/main.go).
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
	jsonData := ledger.LibraryJSONFromParameters(params, true)
	lib, err := easyfl.NewLibraryFromJSON[*ledger.EvalContext](jsonData, ledger.GetEmbeddedFunctionResolver)
	glb.AssertNoError(err)

	err = os.WriteFile(glb.LedgerDefinitionsFileName, jsonData, 0666)
	glb.AssertNoError(err)
	glb.Infof("new ledger identity data has been stored in the file '%s'", glb.LedgerDefinitionsFileName)
	h := lib.LibraryHash()
	glb.Infof("calculated library hash: %s", hex.EncodeToString(h[:]))
	glb.Infof("main ledger constants:\n--------------\n%s\n", ledger.ConstantsStringFromLibrary(lib))
}
