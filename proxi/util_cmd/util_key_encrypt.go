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
	cmd.Flags().StringP("output", "o", "", "output file path (default: overwrite the input file in place)")
	cmd.Flags().String("hint", "", "optional passphrase hint stored in the key file")
	return cmd
}

func runKeyEncryptCmd(cmd *cobra.Command, _ []string) {
	file, _ := cmd.Flags().GetString("file")
	output, _ := cmd.Flags().GetString("output")
	hint, _ := cmd.Flags().GetString("hint")

	glb.Assertf(glb.FileExists(file), "key file '%s' not found", file)
	if output == "" {
		output = file
	}
	glb.Assertf(output == file || !glb.FileExists(output), "output file '%s' already exists", output)

	ks, err := keystore.LoadFromFile(file)
	glb.AssertNoError(err)

	glb.Assertf(!ks.IsEncrypted(), "key file '%s' is already encrypted", file)

	glb.Infof("Key type: %s", keystore.KeyTypeName(ks.KeyType))
	glb.Infof("Holder ID (hash of <type>+<public key>): %s", ks.HolderID)

	passphrase := glb.ReadPassphraseConfirm()

	encrypted, err := keystore.EncryptKeystore(ks, passphrase, hint)
	glb.AssertNoError(err)

	err = encrypted.SaveToFile(output)
	glb.AssertNoError(err)

	glb.Infof("Encrypted key file saved as '%s'.", output)
}
