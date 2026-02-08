package util_cmd

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"os"
	"syscall"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util/keystore"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func checkKeystoreCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check_keystore",
		Args:  cobra.NoArgs,
		Short: "verifies integrity of a passphrase-protected keystore file",
		Run:   runCheckKeystoreCmd,
	}
	cmd.Flags().String("file", defaultKeystoreFile, "path to keystore file")
	return cmd
}

func runCheckKeystoreCmd(cmd *cobra.Command, _ []string) {
	file, _ := cmd.Flags().GetString("file")

	glb.Assertf(glb.FileExists(file), "keystore file '%s' not found", file)

	ks, err := keystore.LoadFromFile(file)
	glb.AssertNoError(err)

	// Display info from the keystore without needing passphrase
	glb.Infof("Key type: %s", keystore.KeyTypeName(ks.KeyType))
	if ks.KeyType == keystore.KeyTypeED25519 {
		pubBytes, err := hex.DecodeString(ks.PubKey)
		if err == nil && len(pubBytes) == ed25519.PublicKeySize {
			lock := ledger.SigLockFromED25519PublicKey(pubBytes)
			glb.Infof("Account (from stored pubkey): %s", lock.String())
		}
	}

	// Prompt for passphrase
	fmt.Print("Enter passphrase: ")
	passBytes, err := term.ReadPassword(syscall.Stdin)
	glb.AssertNoError(err)
	fmt.Println()

	// Verify
	if err := ks.Verify(string(passBytes)); err != nil {
		glb.Fatalf("%v", err)
	}
	glb.Infof("Keystore OK. Decrypted key matches stored public key.")
	os.Exit(0)
}
