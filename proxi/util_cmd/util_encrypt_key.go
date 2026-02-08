package util_cmd

import (
	"crypto/ed25519"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/keystore"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	"gopkg.in/yaml.v2"
)

const defaultKeystoreFile = "proxima_sequencer.keystore"
const defaultKeyFile = "proxima_sequencer.key"

func encryptKeyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "encrypt_key",
		Args:  cobra.NoArgs,
		Short: "encrypts a sequencer key file into a passphrase-protected keystore",
		Run:   runEncryptKeyCmd,
	}
	cmd.Flags().String("key-file", defaultKeyFile, "path to plaintext key file")
	cmd.Flags().Int("key-type", keystore.KeyTypeED25519, "key type (0=ED25519)")
	cmd.Flags().String("output", defaultKeystoreFile, "output keystore file path")
	return cmd
}

func runEncryptKeyCmd(cmd *cobra.Command, _ []string) {
	keyFile, _ := cmd.Flags().GetString("key-file")
	keyType, _ := cmd.Flags().GetInt("key-type")
	outputFile, _ := cmd.Flags().GetString("output")

	// Read the plaintext key
	glb.Assertf(glb.FileExists(keyFile), "key file '%s' not found", keyFile)
	data, err := os.ReadFile(keyFile)
	glb.AssertNoError(err)
	keyHex := strings.TrimSpace(string(data))

	privateKey, err := util.ED25519PrivateKeyFromHexString(keyHex)
	glb.AssertNoError(err)
	publicKey := privateKey.Public().(ed25519.PublicKey)

	glb.Infof("Key type: %s", keystore.KeyTypeName(keyType))
	glb.Infof("Account: %s", ledger.SigLockFromED25519PrivateKey(privateKey).String())

	// Prompt for passphrase (twice for confirmation)
	passphrase := readPassphraseConfirm()

	// Encrypt
	ks, err := keystore.Encrypt(keyType, privateKey, publicKey, passphrase)
	glb.AssertNoError(err)

	// Save keystore
	glb.Assertf(!glb.FileExists(outputFile), "output file '%s' already exists. Remove it first or use --output flag", outputFile)
	err = ks.SaveToFile(outputFile)
	glb.AssertNoError(err)
	glb.Infof("Keystore saved to '%s'", outputFile)

	// Update proxima.yaml if it exists
	if glb.FileExists("proxima.yaml") {
		updateProximaYAMLKeyFile(outputFile)
		glb.Infof("Updated proxima.yaml: controller_key_file = %s", outputFile)
	}

	// Offer to delete plaintext key file
	if glb.YesNoPrompt(fmt.Sprintf("Delete plaintext key file '%s'?", keyFile), false) {
		err = os.Remove(keyFile)
		glb.AssertNoError(err)
		glb.Infof("Plaintext key file deleted.")
	}
}

// readPassphraseConfirm prompts for passphrase twice and returns it.
// Uses terminal no-echo input.
func readPassphraseConfirm() string {
	fmt.Print("Enter passphrase: ")
	pass1, err := term.ReadPassword(syscall.Stdin)
	glb.AssertNoError(err)
	fmt.Println()

	fmt.Print("Confirm passphrase: ")
	pass2, err := term.ReadPassword(syscall.Stdin)
	glb.AssertNoError(err)
	fmt.Println()

	glb.Assertf(string(pass1) == string(pass2), "passphrases do not match")
	glb.Assertf(len(pass1) > 0, "passphrase must not be empty")
	return string(pass1)
}

// updateProximaYAMLKeyFile updates the controller_key_file field in proxima.yaml.
func updateProximaYAMLKeyFile(keystorePath string) {
	data, err := os.ReadFile("proxima.yaml")
	glb.AssertNoError(err)

	var config map[string]interface{}
	err = yaml.Unmarshal(data, &config)
	glb.AssertNoError(err)

	if sequencer, ok := config["sequencer"].(map[interface{}]interface{}); ok {
		sequencer["controller_key_file"] = keystorePath
		delete(sequencer, "controller_key")
	}

	modifiedData, err := yaml.Marshal(&config)
	glb.AssertNoError(err)

	err = os.WriteFile("proxima.yaml", modifiedData, 0600)
	glb.AssertNoError(err)
}
