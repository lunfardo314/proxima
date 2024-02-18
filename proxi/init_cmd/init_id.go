package init_cmd

import (
	"fmt"
	"os"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	ledgerIDFileName            = "proxi.genesis.id.yaml"
	genesisDistributionFileName = "proxi.genesis.distribute.yaml"
	bootstrapAmount             = 1_000_000
)

func initIDCmd() *cobra.Command {
	initLedgerIDCmd := &cobra.Command{
		Use:   "ledger_id",
		Args:  cobra.NoArgs,
		Short: "creates identity data of the ledger in the file 'proxi.genesis.id.yaml'",
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			glb.ReadInConfig()
		},
		Run: runInitLedgerIDCommand,
	}
	initLedgerIDCmd.PersistentFlags().StringP("config", "c", "", "profile name")
	err := viper.BindPFlag("config", initLedgerIDCmd.PersistentFlags().Lookup("config"))
	glb.AssertNoError(err)

	return initLedgerIDCmd
}

func runInitLedgerIDCommand(_ *cobra.Command, _ []string) {
	if glb.FileExists(ledgerIDFileName) {
		if !glb.YesNoPrompt(fmt.Sprintf("file '%s' already exists. Overwrite?", ledgerIDFileName), false) {
			os.Exit(0)
		}
	}
	privKey := glb.MustGetPrivateKey()

	// create ledger identity
	id := ledger.DefaultIdentityData(privKey)
	ledger.Init(id)

	yamlData := id.YAML()
	err := os.WriteFile(ledgerIDFileName, yamlData, 0666)
	glb.AssertNoError(err)
	glb.Infof("new ledger identity data has been stored in the file '%s':", ledgerIDFileName)
	glb.Infof("--------------\n%s--------------\n", string(yamlData))
}
