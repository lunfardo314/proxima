package glb

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"syscall"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/proxi/console"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/ssh/terminal"
)

var forceSetPrivateKey bool

func initSetKeyCmd() *cobra.Command {
	setKeyCmd := &cobra.Command{
		Use:     "set_private_key [<key>] [-c <config name>]",
		Aliases: []string{"setpk"},
		Short:   "Set a private key",
		Args:    cobra.MaximumNArgs(1),
		Run:     runSetKeyCommand,
	}

	setKeyCmd.Flags().BoolVarP(&forceSetPrivateKey, "force", "f", false, "force set private key to the config profile")

	return setKeyCmd
}

func runSetKeyCommand(_ *cobra.Command, args []string) {
	if viper.ConfigFileUsed() == "" {
		console.Fatalf("error: config profile not set")
	}

	if !forceSetPrivateKey {
		if GetPrivateKey() != nil {
			if !console.YesNoPrompt("Are you sure you want to replace current private key? (y/N):", false) {
				os.Exit(1)
			}
		}
	}

	console.Assertf(!forceSetPrivateKey || len(args) == 1, "private key must be provided explicitly")

	var privateKey ed25519.PrivateKey
	var err error

	if len(args) == 1 {
		privateKey, err = util.ED25519PrivateKeyFromHexString(args[0])
		console.AssertNoError(err)

		addr := core.AddressED25519FromPrivateKey(privateKey)
		console.Infof("Private key has been set. ED25519 address is: %s", addr.String())
	} else {
		console.Infof("Private key will be generated")
		console.Printf("Enter random seed (minimum %d characters): ", minimumSeedLength)
		userSeed, err := terminal.ReadPassword(syscall.Stdin)
		console.AssertNoError(err)

		if len(userSeed) < minimumSeedLength {
			cobra.CheckErr(fmt.Errorf("must be at least %d characters of the seed", minimumSeedLength))
		}
		console.Printf("\n")

		var osSeed [8]byte
		n, err := rand.Read(osSeed[:])
		cobra.CheckErr(err)
		if n != 8 {
			cobra.CheckErr("error while reading random seed")
		}
		seed := blake2b.Sum256(common.Concat(userSeed, osSeed[:]))
		privateKey = ed25519.NewKeyFromSeed(seed[:])
		addr := core.AddressED25519FromPrivateKey(privateKey)
		console.Infof("Private key has been generated. ED25519 address is: %s", addr)
	}
	SetKeyValue("private_key", hex.EncodeToString(privateKey))
}
