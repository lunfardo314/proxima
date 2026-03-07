package util_cmd

import (
	"fmt"
	"os"
	"syscall"

	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util/keystore"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func keyDecryptCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "decrypt",
		Args:  cobra.NoArgs,
		Short: "decrypt an encrypted .key file to unencrypted",
		Run:   runKeyDecryptCmd,
	}
	cmd.Flags().String("file", keystore.DefaultKeyFile, "encrypted key file to decrypt")
	return cmd
}

func runKeyDecryptCmd(cmd *cobra.Command, _ []string) {
	file, _ := cmd.Flags().GetString("file")

	glb.Assertf(glb.FileExists(file), "key file '%s' not found", file)

	ks, err := keystore.LoadFromFile(file)
	glb.AssertNoError(err)

	glb.Assertf(ks.IsEncrypted(), "key file '%s' is not encrypted", file)

	glb.Infof("Key type: %s", keystore.KeyTypeName(ks.KeyType))
	glb.Infof("Holder ID (hash of <type>+<public key>): %s", ks.HolderID)

	hint := ""
	if ks.Hint != "" {
		hint = fmt.Sprintf(" (hint: %s)", ks.Hint)
	}
	fmt.Printf("Enter passphrase%s: ", hint)
	passBytes, err := term.ReadPassword(syscall.Stdin)
	glb.AssertNoError(err)
	fmt.Println()

	decrypted, err := keystore.DecryptKeystore(ks, string(passBytes))
	glb.AssertNoError(err)

	glb.Infof("WARNING: The decrypted key file will contain the private key in plaintext.")
	if !glb.YesNoPrompt("Proceed?", false) {
		os.Exit(0)
	}

	err = decrypted.SaveToFile(file)
	glb.AssertNoError(err)

	glb.Infof("Key file '%s' decrypted successfully.", file)
}
