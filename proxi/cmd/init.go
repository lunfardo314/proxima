/*
Copyright © 2023 NAME HERE <EMAIL ADDRESS>
*/
package cmd

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"syscall"

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

func init() {
	rootCmd.AddCommand(initCmd)

	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// initCmd.PersistentFlags().String("foo", "", "A help for foo")

	initCmd.Flags().StringVar(&privateKeyStr, "key", "", "a ED25519 private key")
}

var (
	privateKeyStr string
)

const minimumSeedLength = 8

func runInitCommand(cmd *cobra.Command, args []string) {
	var privateKey ed25519.PrivateKey
	var err error
	if privateKeyStr != "" {
		privateKey, err = hex.DecodeString(privateKeyStr)
		cobra.CheckErr(err)

		if len(privateKey) != ed25519.PrivateKeySize {
			cobra.CheckErr("wrong private key size")
		}
		return
	}
	fmt.Printf("Private key will be generated\n")
	fmt.Printf("Enter random seed (minimum %d characters): ", minimumSeedLength)
	userSeed, err := terminal.ReadPassword(syscall.Stdin)
	cobra.CheckErr(err)

	if len(userSeed) < minimumSeedLength {
		cobra.CheckErr(fmt.Errorf("must be at least %d characters of the seed", minimumSeedLength))
	}
	fmt.Println()

	var osSeed [8]byte
	n, err := rand.Read(osSeed[:])
	cobra.CheckErr(err)
	if n != 8 {
		cobra.CheckErr("error while reading random seed")
	}
	seed := blake2b.Sum256(common.Concat(userSeed, osSeed[:]))
	privateKey = ed25519.NewKeyFromSeed(seed[:])
}
