package setup

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"syscall"

	"github.com/lunfardo314/proxima/proxi/log"
	"github.com/lunfardo314/unitrie/common"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/ssh/terminal"
)

// initCmd represents the init command
var initCmd = &cobra.Command{
	Use:   "init",
	Short: "initialize proxi profile",
	Long:  `creates a new proxi profile with the private key`,
	Run:   runInitCommand,
}

var (
	privateKeyStr string
	privateKey    ed25519.PrivateKey
)

func Init(rootCmd *cobra.Command) {
	rootCmd.AddCommand(initCmd)

	initCmd.Flags().StringVar(&privateKeyStr, "key", "", "a ED25519 private key")
}

const minimumSeedLength = 8

func runInitCommand(_ *cobra.Command, _ []string) {
	var err error
	if privateKeyStr != "" {
		privateKey, err = hex.DecodeString(privateKeyStr)
		cobra.CheckErr(err)

		if len(privateKey) != ed25519.PrivateKeySize {
			cobra.CheckErr("wrong private key size")
		}
		return
	}
	log.Infof("Private key will be generated")
	log.Printf("Enter random seed (minimum %d characters): ", minimumSeedLength)
	userSeed, err := terminal.ReadPassword(syscall.Stdin)
	cobra.CheckErr(err)

	if len(userSeed) < minimumSeedLength {
		cobra.CheckErr(fmt.Errorf("must be at least %d characters of the seed", minimumSeedLength))
	}
	log.Printf("\n")

	var osSeed [8]byte
	n, err := rand.Read(osSeed[:])
	cobra.CheckErr(err)
	if n != 8 {
		cobra.CheckErr("error while reading random seed")
	}
	seed := blake2b.Sum256(common.Concat(userSeed, osSeed[:]))
	privateKey = ed25519.NewKeyFromSeed(seed[:])
	publicKey := privateKey.Public().(ed25519.PublicKey)
	address := blake2b.Sum256(publicKey)
	log.Infof("Private key has been generated. ED25519 address is: %s", hex.EncodeToString(address[:]))
}
