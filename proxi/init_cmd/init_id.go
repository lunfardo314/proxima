package init_cmd

import (
	"fmt"
	"os"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/genesis"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/txbuilder"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// TODO create default initial distribution

const (
	ledgerIDFileName            = "proxi.genesis.id.yaml"
	genesisDistributionFileName = "proxi.genesis.distribute.yaml"
	bootstrapAmount             = 10_000
)

func initIDCmd() *cobra.Command {
	initLedgerIDCmd := &cobra.Command{
		Use:   "ledger_id",
		Args:  cobra.NoArgs,
		Short: "creates identity data of the ledger in 'proxi.genesis.id' and default initial distribution list in 'proxi.genesis.distribution.yaml'",
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
	if glb.FileExists(genesisDistributionFileName) {
		if !glb.YesNoPrompt(fmt.Sprintf("file '%s' already exists. Overwrite?", genesisDistributionFileName), false) {
			os.Exit(0)
		}
	}
	privKey := glb.MustGetPrivateKey()

	// create ledger identity
	id := genesis.DefaultIdentityData(privKey)
	yamlData := id.YAML()
	err := os.WriteFile(ledgerIDFileName, yamlData, 0666)
	glb.AssertNoError(err)
	glb.Infof("new ledger identity data has been stored in the file '%s':", ledgerIDFileName)
	glb.Infof("--------------\n%s--------------\n", string(yamlData))

	// create default distribution
	controllerAddr := core.AddressED25519FromPrivateKey(privKey)
	lstYAML := []byte(fmt.Sprintf(defaultDistributionListTemplate,
		controllerAddr.String(), bootstrapAmount))
	lstDistrib, err := txbuilder.InitialDistributionFromYAMLData(lstYAML)
	glb.AssertNoError(err)
	err = os.WriteFile(genesisDistributionFileName, lstYAML, 0666)
	glb.AssertNoError(err)
	glb.Infof("default genesis distribution list has been stored in the file '%s':", genesisDistributionFileName)
	glb.Infof(txbuilder.DistributionListToLines(lstDistrib, "     ").String())
}

const defaultDistributionListTemplate = `# Genesis distribution list consists the list of lock/balance pairs.
# The default genesis distribution list contains only one lock/balance pair.
# It assigns 10_000 tokens to the target ED25519 address ('bootstrap address') which is also
# the controlling address of the bootstrap chain. 
# I.e. both the bootstrap chain and bootstrap address is controlled by the same private key
# The tokens in the ED25519 address are needed to be able to 
# provide tokens for the command output to te bootstrap sequencer
-
  lock: %s
  balance: %d
`
