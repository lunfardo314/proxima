package init_cmd

import (
	"bytes"
	"crypto/ed25519"
	_ "embed"
	"encoding/hex"
	"os"
	"text/template"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util/keystore"
	"github.com/spf13/cobra"
)

//go:embed wallet_profile.template
var walletProfileTemplate string

func initWalletCmd() *cobra.Command {
	initWallet := &cobra.Command{
		Use:   "wallet [<profile name. Default: 'proxi'>]",
		Args:  cobra.MaximumNArgs(1),
		Short: "initializes new proxi wallet profile proxi.yaml with a .key file",
		Run:   runInitWalletCommand,
	}

	return initWallet
}

func runInitWalletCommand(_ *cobra.Command, args []string) {
	profileName := "proxi"
	if len(args) > 0 {
		profileName = args[0]
	}
	profileFname := profileName + ".yaml"
	glb.Assertf(!glb.FileExists(profileFname), "file %s already exists", profileFname)

	keyFile := keystore.DefaultKeyFile
	var holderID string

	// Check if a .key file already exists
	if glb.FileExists(keyFile) {
		if glb.YesNoPrompt("Found existing key file '"+keyFile+"'. Use it?", true) {
			ks, err := keystore.LoadFromFile(keyFile)
			glb.AssertNoError(err)
			holderID = ks.HolderID
			if holderID == "" {
				glb.Infof("Key file has no holder_id. Deriving from public key.")
				// For v1 keystores, derive from public key if possible
				holderID = deriveHolderIDFromKeystore(ks)
			}
			glb.Infof("Using existing key file '%s'", keyFile)
		} else {
			// User wants a new key but default filename is taken
			glb.Fatalf("key file '%s' already exists. Remove it or use a different profile.", keyFile)
		}
	} else {
		// Generate a new key
		privateKey := glb.AskEntropyGenEd25519PrivateKey(
			"We need some entropy for the private key of the account.\nPlease enter at least 10 seed symbols as randomly as possible and press ENTER:", 10)
		publicKey := privateKey.Public().(ed25519.PublicKey)
		sid := base.HolderIDFromPublicKey(base.SignatureTypeED25519, publicKey)
		holderID = hex.EncodeToString(sid[:])

		ks, err := keystore.NewUnencrypted(keystore.KeyTypeED25519, privateKey, publicKey, holderID)
		glb.AssertNoError(err)

		// Offer encryption
		if glb.YesNoPrompt("Encrypt the key file with a passphrase?", false) {
			passphrase := glb.ReadPassphraseConfirm()
			ks, err = keystore.EncryptKeystore(ks, passphrase, "")
			glb.AssertNoError(err)
			glb.Infof("Key encrypted.")
		}

		err = ks.SaveToFile(keyFile)
		glb.AssertNoError(err)
		glb.Infof("Key file saved to '%s'", keyFile)
	}

	// Generate the wallet profile
	templ := template.New("wallet")
	_, err := templ.Parse(walletProfileTemplate)
	glb.AssertNoError(err)

	data := struct {
		KeyFile        string
		HolderID       string
		BootstrapSeqID string
	}{
		KeyFile:        keyFile,
		HolderID:       holderID,
		BootstrapSeqID: ledger.BoostrapSequencerIDHex,
	}
	var buf bytes.Buffer
	err = templ.Execute(&buf, data)
	glb.AssertNoError(err)

	err = os.WriteFile(profileFname, buf.Bytes(), 0600)
	glb.AssertNoError(err)
	glb.Infof("proxi profile '%s' has been created successfully.\nHolder ID (hash of <type>+<public key>): %s", profileFname, holderID)
}

// deriveHolderIDFromKeystore derives the holder ID from the public key stored in the keystore.
// Works for ED25519 key types when the keystore has a valid public key field.
func deriveHolderIDFromKeystore(ks *keystore.Keystore) string {
	if ks.KeyType != keystore.KeyTypeED25519 {
		return ""
	}
	pubBytes, err := keystore.PublicKeyBytes(ks)
	if err != nil {
		return ""
	}
	sid := base.HolderIDFromPublicKey(base.SignatureTypeED25519, pubBytes)
	return hex.EncodeToString(sid[:])
}
