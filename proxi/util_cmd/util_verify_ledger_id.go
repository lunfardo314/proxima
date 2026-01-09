package util_cmd

import (
	"encoding/hex"
	"fmt"
	"os"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func verifyIDCmd() *cobra.Command {
	verifyLedgerIDCmd := &cobra.Command{
		Use:   "verify_ledger_id",
		Args:  cobra.NoArgs,
		Short: fmt.Sprintf("checks consistency of the ledger definition in file '%s'", glb.LedgerIDFileName),
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			glb.ReadInConfig()
		},
		Run: runGenVerifyLedgerIDCommand,
	}
	verifyLedgerIDCmd.PersistentFlags().StringP("config", "c", "", "profile name")
	err := viper.BindPFlag("config", verifyLedgerIDCmd.PersistentFlags().Lookup("config"))
	glb.AssertNoError(err)

	return verifyLedgerIDCmd
}

func runGenVerifyLedgerIDCommand(_ *cobra.Command, _ []string) {
	glb.Assertf(glb.FileExists(glb.LedgerIDFileName), "file %s does not exist", glb.LedgerIDFileName)
	yamlData, err := os.ReadFile(glb.LedgerIDFileName)
	glb.AssertNoError(err)

	lib, err := ledger.ParseLibraryFromYAML(yamlData, ledger.GetEmbeddedFunctionResolverUpgrade0)
	glb.AssertNoError(err)
	constants := ledger.ConstantsFromLibrary(lib)
	glb.Infof("hash of the library: %s", hex.EncodeToString(constants.Hash[:]))

	if pk, ok := glb.GetPrivateKey(); ok {
		if constants.GenesisControllerPublicKey.Equal(pk.Public()) {
			glb.Infof("Genesis public key MATCHES public key of the wallet")
		} else {
			glb.Infof("Genesis public key DOES NOT MATCH public key of the wallet")
		}
	}
	glb.Infof("ledger ID data in '%s' is OK. Size: %d bytes\nMain ledger parameters:\n-------------------\n%s",
		glb.LedgerIDFileName, len(yamlData), constants.String())
}
