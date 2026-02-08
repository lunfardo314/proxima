package util_cmd

import (
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util/keystore"
	"github.com/spf13/cobra"
)

func keyInfoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "info",
		Args:  cobra.NoArgs,
		Short: "display key file information",
		Run:   runKeyInfoCmd,
	}
	cmd.Flags().StringP("file", "f", keystore.DefaultKeyFile, "key file to inspect")
	return cmd
}

func runKeyInfoCmd(cmd *cobra.Command, _ []string) {
	file, _ := cmd.Flags().GetString("file")

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
	if ks.SpenderID != "" {
		glb.Infof("Account: %s", ks.SpenderID)
	}
}
