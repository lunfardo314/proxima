package util_cmd

import (
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util/keystore"
	"github.com/spf13/cobra"
)

func keyEncryptCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "encrypt",
		Args:  cobra.NoArgs,
		Short: "encrypt an unencrypted .key file with a passphrase",
		Run:   runKeyEncryptCmd,
	}
	cmd.Flags().String("file", keystore.DefaultKeyFile, "key file to encrypt")
	cmd.Flags().String("hint", "", "optional passphrase hint stored in the key file")
	return cmd
}

func runKeyEncryptCmd(cmd *cobra.Command, _ []string) {
	file, _ := cmd.Flags().GetString("file")
	hint, _ := cmd.Flags().GetString("hint")

	glb.Assertf(glb.FileExists(file), "key file '%s' not found", file)

	ks, err := keystore.LoadFromFile(file)
	glb.AssertNoError(err)

	glb.Assertf(!ks.IsEncrypted(), "key file '%s' is already encrypted", file)

	glb.Infof("Key type: %s", keystore.KeyTypeName(ks.KeyType))
	glb.Infof("Spender ID (hash of the public key): %s", ks.SpenderID)

	passphrase := glb.ReadPassphraseConfirm()

	encrypted, err := keystore.EncryptKeystore(ks, passphrase, hint)
	glb.AssertNoError(err)

	err = encrypted.SaveToFile(file)
	glb.AssertNoError(err)

	glb.Infof("Key file '%s' encrypted successfully.", file)
}
