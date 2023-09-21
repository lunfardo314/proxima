package glb

import (
	"crypto/ed25519"
	"encoding/hex"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/proxi/console"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/crypto/blake2b"
)

var ConfigName string

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
	var configFname string
	if ConfigName == "" {
		configFname = "proxi.yaml"
	} else {
		configFname = ConfigName + ".yaml"
	}
	console.AssertNoError(viper.SafeWriteConfigAs(configFname))
}

func GetPrivateKey() ed25519.PrivateKey {
	privateKeyStr := viper.GetString("private_key")
	ret, err := util.ED25519PrivateKeyFromHexString(privateKeyStr)
	console.AssertNoError(err)
	return ret
}

func AddressBytes() []byte {
	privateKey := GetPrivateKey()
	if len(privateKey) == 0 {
		return nil
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	address := blake2b.Sum256(publicKey)
	return address[:]
}

func AddressHex() string {
	addr := AddressBytes()
	if len(addr) == 0 {
		return "(unknown)"
	}
	return hex.EncodeToString(addr)
}

func GetWalletAccount() core.AddressED25519 {
	pk := GetPrivateKey()
	if len(pk) == 0 {
		return nil
	}
	return core.AddressED25519FromPrivateKey(pk)
}

func MustGetTarget() core.Accountable {
	var ret core.Accountable
	var err error

	if str := viper.GetString("target"); str != "" {
		ret, err = core.AccountableFromSource(str)
		console.AssertNoError(err)
		console.Infof("target account is: %s", ret.String())
	} else {
		ret = GetWalletAccount()
		util.Assertf(ret != nil, "wallet private key undefined")
		console.Infof("wallet account will be used as target: %s", ret.String())
	}
	return ret
}
