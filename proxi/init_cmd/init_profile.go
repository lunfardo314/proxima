package init_cmd

import (
	"encoding/hex"
	"fmt"
	"os"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func initProfileCmd() *cobra.Command {
	initProfCmd := &cobra.Command{
		Use:   "profile [<profile name. Default: 'proxi'>]",
		Args:  cobra.MaximumNArgs(1),
		Short: "initializes proxi profile with the private key",
		Run:   runInitProfileCommand,
	}
	initProfCmd.PersistentFlags().String("wallet.private_key", "", "mandatory hex-encoded private key")
	err := viper.BindPFlag("wallet.private_key", initProfCmd.PersistentFlags().Lookup("wallet.private_key"))
	glb.AssertNoError(err)

	return initProfCmd
}

const minimumSeedLength = 8

const profileTemplate = `# proxi profile 
wallet:
    private_key: %s
    account: %s
    # own sequencer (controlled by the private key)
	sequencer: 
api:
    endpoint:
`

func runInitProfileCommand(_ *cobra.Command, args []string) {
	profileName := "proxi"
	if len(args) > 0 {
		profileName = args[0]
	}
	profileFname := profileName + ".yaml"
	glb.Assertf(!fileExists(profileFname), "file %s already exists", profileFname)

	privKey := glb.MustGetPrivateKey()
	addr := core.AddressED25519FromPrivateKey(privKey)
	profile := fmt.Sprintf(profileTemplate, hex.EncodeToString(privKey), addr.String())
	err := os.WriteFile(profileFname, []byte(profile), 0666)
	glb.AssertNoError(err)
	glb.Infof("proxi profile '%s' has been created successfully", profileFname)

	viper.SetConfigFile(profileFname)
	wd := glb.GetWalletData()
	glb.Assertf(wd.PrivateKey.Equal(privKey), "inconsistency")
}
