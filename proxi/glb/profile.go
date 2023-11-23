package glb

import (
	"crypto/ed25519"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func initProfileCmd(initCmd *cobra.Command) *cobra.Command {
	initProfCmd := &cobra.Command{
		Use:   "profile [<profile name>]",
		Args:  cobra.MaximumNArgs(1),
		Short: "initializes empty config profile",
		Run:   runInitProfileCommand,
	}
	initCmd.AddCommand(initProfCmd)
	return initProfCmd
}

const minimumSeedLength = 8

func runInitProfileCommand(_ *cobra.Command, args []string) {
	profileName := "proxi_old"
	if len(args) > 0 {
		profileName = args[0]
	}
	profileFname := profileName + ".yaml"
	privKey := mustGetPrivateKey()
	addr := core.AddressED25519FromPrivateKey(privKey)
	viper.SetConfigFile(profileFname)
	viper.Set("wallet.account", addr.String())
	AssertNoError(viper.SafeWriteConfigAs(profileFname))
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
	ret, ok := getPrivateKey()
	Assertf(ok, "private key not specified")
	return ret
}

func getPrivateKey() (ed25519.PrivateKey, bool) {
	privateKeyStr := viper.GetString("wallet.private_key")
	if privateKeyStr == "" {
		return nil, false
	}
	ret, err := util.ED25519PrivateKeyFromHexString(privateKeyStr)
	return ret, err == nil
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

func BypassYesNoPrompt() bool {
	return viper.GetBool("force")
}
