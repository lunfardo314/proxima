package config

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

func initSetKeyCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "set_private_key [<key>] [-c <config name>]",
		Aliases: []string{"setpk"},
		Short:   "Set a private key",
		Args:    cobra.MaximumNArgs(1),
		Run:     runSetKeyCommand,
	}
}

func runSetKeyCommand(_ *cobra.Command, args []string) {
	if viper.ConfigFileUsed() == "" {
		console.Fatalf("error: config profile not set")
	}

	if GetPrivateKey() != nil {
		if !console.YesNoPrompt("Are you sure you want to replace current private key? (y/N):", false) {
			os.Exit(1)
		}
	}

	var privateKey ed25519.PrivateKey
	if len(args) == 1 {
		privateKey = decodePrivateKey(args[0])
		if len(privateKey) == 0 {
			console.Fatalf("wrong private key")
		}
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
	}
	SetKeyValue("private_key", hex.EncodeToString(privateKey))
	console.Infof("Private key has been generated. ED25519 address is: %s", hex.EncodeToString(AddressBytes()))
}

func decodePrivateKey(pkstr string) ed25519.PrivateKey {
	privateKey, err := hex.DecodeString(pkstr)
	console.AssertNoError(err)

	if len(privateKey) != ed25519.PrivateKeySize {
		console.Fatalf("wrong private key size")
	}
	return privateKey
}

func GetPrivateKey() ed25519.PrivateKey {
	privateKeyStr := viper.GetString("private_key")
	if privateKeyStr == "" {
		return nil
	}
	return decodePrivateKey(privateKeyStr)
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
