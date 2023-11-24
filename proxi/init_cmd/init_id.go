package init_cmd

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/genesis"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const ledgerIDFileName = "proxima.id.yaml"

func initIDCmd() *cobra.Command {
	initLedgerIDCmd := &cobra.Command{
		Use:   "ledger_id",
		Args:  cobra.NoArgs,
		Short: "creates identity data of the ledger",
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			readInConfig()
		},
		Run: runInitLedgerIDCommand,
	}
	initLedgerIDCmd.PersistentFlags().StringP("config", "c", "", "profile name")
	err := viper.BindPFlag("config", initLedgerIDCmd.PersistentFlags().Lookup("config"))
	glb.AssertNoError(err)

	return initLedgerIDCmd
}

func runInitLedgerIDCommand(_ *cobra.Command, _ []string) {
	if fileExists(ledgerIDFileName) {
		if !glb.YesNoPrompt(fmt.Sprintf("file '%s' already exists. Overwrite?", ledgerIDFileName), false) {
			os.Exit(0)
		}
	}
	privKey := glb.MustGetPrivateKey()
	id := genesis.DefaultIdentityData(privKey)
	yamlData := id.YAML()
	err := os.WriteFile(ledgerIDFileName, yamlData, 0666)
	glb.AssertNoError(err)
	glb.Infof("new ledger identity data has been stored in file '%s':", ledgerIDFileName)
	glb.Infof("--------------\n%s--------------", string(yamlData))
	glb.Infof("Genesis controller address: %s", core.AddressED25519FromPrivateKey(privKey).String())
	pubKeyStr := hex.EncodeToString(privKey.Public().(ed25519.PublicKey))
	glb.Infof("Genesis controller public key: %s (for control)", pubKeyStr)
}
