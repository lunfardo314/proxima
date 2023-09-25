package glb

import (
	"crypto/ed25519"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// Init adds all commands in the package to the root command
func Init(rootCmd *cobra.Command) {
	rootCmd.AddCommand(initInitCmd())
	rootCmd.AddCommand(initConfigSetCmd())
	rootCmd.AddCommand(initSetKeyCmd())
}

func initInitCmd() *cobra.Command {
	initCmd := &cobra.Command{
		Use:   "init [-c profile]",
		Args:  cobra.NoArgs,
		Short: "initializes an empty config profile",
		Run:   runInitCommand,
	}
	return initCmd
}

const minimumSeedLength = 8

func runInitCommand(_ *cobra.Command, _ []string) {
	configFname := viper.GetString("config")
	if configFname == "" {
		configFname = "proxi.yaml"
	} else {
		configFname = configFname + ".yaml"
	}
	AssertNoError(viper.SafeWriteConfigAs(configFname))
}

type WalletData struct {
	Name       string
	PrivateKey ed25519.PrivateKey
	Account    core.AddressED25519
	Sequencer  *core.ChainID
}

func GetWalletData() (ret WalletData) {
	ret.Name = viper.GetString("wallet.name")
	ret.PrivateKey = mustGetPrivateKey()
	ret.Account = core.AddressED25519FromPrivateKey(ret.PrivateKey)
	ret.Sequencer = GetOwnSequencerID()
	return
}

func mustGetPrivateKey() ed25519.PrivateKey {
	privateKeyStr := viper.GetString("wallet.pk")
	ret, err := util.ED25519PrivateKeyFromHexString(privateKeyStr)
	AssertNoError(err)
	return ret
}

func MustGetTarget() core.Accountable {
	var ret core.Accountable
	var err error

	if str := viper.GetString("target"); str != "" {
		ret, err = core.AccountableFromSource(str)
		AssertNoError(err)
		Infof("target account is: %s", ret.String())
	} else {
		ret = GetWalletData().Account
		Infof("wallet account will be used as target: %s", ret.String())
	}
	return ret
}

func GetOwnSequencerID() *core.ChainID {
	seqIDStr := viper.GetString("wallet.sequencer")
	if seqIDStr == "" {
		return nil
	}
	ret, err := core.ChainIDFromHexString(seqIDStr)
	AssertNoError(err)
	return &ret
}

func GetTagAlongSequencerID() *core.ChainID {
	seqIDStr := viper.GetString("tag-along.sequencer")
	if seqIDStr == "" {
		return nil
	}
	ret, err := core.ChainIDFromHexString(seqIDStr)
	AssertNoError(err)
	return &ret
}

func BypassYesNoPrompt() bool {
	return viper.GetBool("force")
}
