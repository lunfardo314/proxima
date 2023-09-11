package setup

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"syscall"

	"github.com/lunfardo314/proxima/proxi/console"
	"github.com/lunfardo314/unitrie/common"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/ssh/terminal"
)

var (
	privateKeyStr string
	privateKey    ed25519.PrivateKey
)

func initSetKeyCmd() *cobra.Command {
	setPrivateKeyCmd := &cobra.Command{
		Use:     "set_private_key [<key>]",
		Aliases: []string{"setpk"},
		Short:   "Set a private key",
		Args:    cobra.MaximumNArgs(1),
		Run:     runSetKeyCommand,
	}
	setPrivateKeyCmd.Flags().StringVar(&privateKeyStr, "private_key", "", "an ED25519 private key in hexadecimal")
	if privateKeyStr != "" {
		mustDecodePrivateKey(privateKeyStr)
		console.Infof("Address: %s", hex.EncodeToString(addressBytes()))
	}
	return setPrivateKeyCmd
}

func mustDecodePrivateKey(pkStr string) {
	var err error
	privateKey, err = hex.DecodeString(pkStr)
	console.NoError(err)

	if len(privateKey) != ed25519.PrivateKeySize {
		console.Fatalf("wrong private key size")
	}
}

func addressBytes() []byte {
	publicKey := privateKey.Public().(ed25519.PublicKey)
	address := blake2b.Sum256(publicKey)
	return address[:]
}

func runSetKeyCommand(_ *cobra.Command, args []string) {
	if viper.ConfigFileUsed() == "" {
		console.Fatalf("error: config profile not set")
	}

	if len(privateKey) != 0 {
		if !console.YesNoPrompt("Are you sure you want to replace current private key? (y/N):", false) {
			os.Exit(1)
		}
	}

	if len(args) == 1 {
		mustDecodePrivateKey(args[1])
	} else {
		console.Infof("Private key will be generated")
		console.Printf("Enter random seed (minimum %d characters): ", minimumSeedLength)
		userSeed, err := terminal.ReadPassword(syscall.Stdin)
		cobra.CheckErr(err)

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
	}

	_set("private_key", hex.EncodeToString(privateKey))
	console.Infof("Private key has been generated. ED25519 address is: %s", hex.EncodeToString(addressBytes()))
}
