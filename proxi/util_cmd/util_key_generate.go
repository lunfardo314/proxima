package util_cmd

import (
	"crypto/ed25519"
	"encoding/hex"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util/keystore"
	"github.com/spf13/cobra"
)

func keyGenerateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "generate",
		Args:  cobra.NoArgs,
		Short: "generate a new ED25519 key pair and save as .key file",
		Run:   runKeyGenerateCmd,
	}
	cmd.Flags().StringP("output", "o", keystore.DefaultKeyFile, "output key file path")
	cmd.Flags().Bool("encrypt", false, "encrypt the key with a passphrase")
	cmd.Flags().String("hint", "", "passphrase hint (only used with --encrypt)")
	return cmd
}

func runKeyGenerateCmd(cmd *cobra.Command, _ []string) {
	outputFile, _ := cmd.Flags().GetString("output")
	encrypt, _ := cmd.Flags().GetBool("encrypt")
	hint, _ := cmd.Flags().GetString("hint")

	glb.Assertf(!glb.FileExists(outputFile), "file '%s' already exists", outputFile)

	glb.Infof("DISCLAIMER: USE AT YOUR OWN RISK! This program generates a private key based on system randomness and user-provided entropy.")
	privateKey := glb.AskEntropyGenEd25519PrivateKey(
		"Please enter at least 10 random seed symbols and press ENTER:", 10)
	publicKey := privateKey.Public().(ed25519.PublicKey)
	sid := base.HolderIDFromPublicKey(base.SignatureTypeED25519, publicKey)
	holderID := hex.EncodeToString(sid[:])

	ks, err := keystore.NewUnencrypted(keystore.KeyTypeED25519, privateKey, publicKey, holderID)
	glb.AssertNoError(err)

	if encrypt {
		passphrase := glb.ReadPassphraseConfirm()
		ks, err = keystore.EncryptKeystore(ks, passphrase, hint)
		glb.AssertNoError(err)
		glb.Infof("Key encrypted.")
	}

	err = ks.SaveToFile(outputFile)
	glb.AssertNoError(err)

	glb.Infof("Key saved to '%s'", outputFile)
	glb.Infof("Holder ID (hash of <type>+<public key>): %s", holderID)
}
