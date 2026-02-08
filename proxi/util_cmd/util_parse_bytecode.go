package util_cmd

import (
	"encoding/hex"
	"fmt"
	"os"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initParseBytecode() *cobra.Command {
	validateLedgerIDCmd := &cobra.Command{
		Use:   "parse_bytecode <bytecode hex>",
		Args:  cobra.ExactArgs(1),
		Short: fmt.Sprintf("parses EasyFL bytecode with ledger definitions provided in '%s'", glb.LedgerDefinitionsFileName),
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			glb.ReadInConfig()
		},
		Run: runParseBytecode,
	}
	//validateLedgerIDCmd.PersistentFlags().StringP("config", "c", "", "profile name")
	//err := viper.BindPFlag("config", validateLedgerIDCmd.PersistentFlags().Lookup("config"))
	//glb.AssertNoError(err)

	return validateLedgerIDCmd
}

func runParseBytecode(_ *cobra.Command, args []string) {
	ledgerIDData, err := os.ReadFile(glb.LedgerDefinitionsFileName)
	glb.AssertNoError(err)
	ledger.MustInitLibraryCacheFromYAML(ledgerIDData)

	bytecode, err := hex.DecodeString(args[0])
	glb.AssertNoError(err)

	// CLI uses latest library version for parsing bytecode
	c, err := ledger.ConstraintFromBytesAtSlot(bytecode, base.MaxSlot)
	glb.AssertNoError(err)

	glb.Infof("Parsed bytecode:\n    string: %s\n    source: %s", c.String(), c.Source())
}
