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

func keyInfoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "info",
		Args:  cobra.NoArgs,
		Short: "display key file information",
		Run:   runKeyInfoCmd,
	}
	cmd.Flags().String("file", keystore.DefaultKeyFile, "key file to inspect")
	cmd.Flags().Bool("verify", false, "verify that the private key matches the public key (requires passphrase for encrypted keys)")
	return cmd
}

func runKeyInfoCmd(cmd *cobra.Command, _ []string) {
	file, _ := cmd.Flags().GetString("file")
	verify, _ := cmd.Flags().GetBool("verify")

	glb.Assertf(glb.FileExists(file), "key file '%s' not found", file)

	ks, err := keystore.LoadFromFile(file)
	glb.AssertNoError(err)

	glb.Infof("File: %s", file)
	glb.Infof("Version: %d", ks.Version)
	glb.Infof("Key type: %s", keystore.KeyTypeName(ks.KeyType))
	glb.Infof("Encrypted: %v", ks.IsEncrypted())
	if ks.Hint != "" {
		glb.Infof("Hint: %s", ks.Hint)
	}
	glb.Infof("Public key: %s", ks.PublicKey)
	if ks.HolderID != "" {
		glb.Infof("Holder ID (hash of <type>+<public key>): %s", ks.HolderID)
	}

	if verify {
		passphrase := ""
		if ks.IsEncrypted() {
			hint := ""
			if ks.Hint != "" {
				hint = fmt.Sprintf(" (hint: %s)", ks.Hint)
			}
			fmt.Printf("Enter passphrase%s: ", hint)
			passBytes, err := term.ReadPassword(syscall.Stdin)
			glb.AssertNoError(err)
			fmt.Println()
			passphrase = string(passBytes)
		}
		err := ks.Verify(passphrase)
		if err != nil {
			glb.Infof("Verification FAILED: %v", err)
			os.Exit(1)
		}
		glb.Infof("Verification OK: private key matches public key")
	}
}
