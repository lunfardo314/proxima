package init_cmd

import (
	"bytes"
	"crypto/ed25519"
	"os"
	"text/template"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util/keystore"
	"github.com/spf13/cobra"
)

func initWalletCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "wallet [<profile name. Default: 'proxi'>]",
		Args:  cobra.MaximumNArgs(1),
		Short: "initializes new proxi wallet profile proxi.yaml with a .key file",
		Run:   runInitWalletCommand,
	}
}

func runInitWalletCommand(_ *cobra.Command, args []string) {
	profileName := "proxi"
	if len(args) > 0 {
		profileName = args[0]
	}
	profileFname := profileName + ".yaml"
	glb.Assertf(!glb.FileExists(profileFname), "file %s already exists", profileFname)

	keyFile := keystore.DefaultKeyFile
	var account string

	// Check if a .key file already exists
	if glb.FileExists(keyFile) {
		if glb.YesNoPrompt("Found existing key file '"+keyFile+"'. Use it?", true) {
			ks, err := keystore.LoadFromFile(keyFile)
			glb.AssertNoError(err)
			account = ks.SpenderID
			if account == "" {
				glb.Infof("Key file has no spender_id. Deriving from public key.")
				// For v1 keystores, derive from public key if possible
				account = deriveSpenderIDFromKeystore(ks)
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
		account = ledger.SigLockFromED25519PrivateKey(privateKey).String()

		ks, err := keystore.NewUnencrypted(keystore.KeyTypeED25519, privateKey, publicKey, account)
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
		Account        string
		BootstrapSeqID string
	}{
		KeyFile:        keyFile,
		Account:        account,
		BootstrapSeqID: ledger.BoostrapSequencerIDHex,
	}
	var buf bytes.Buffer
	err = templ.Execute(&buf, data)
	glb.AssertNoError(err)

	err = os.WriteFile(profileFname, buf.Bytes(), 0600)
	glb.AssertNoError(err)
	glb.Infof("proxi profile '%s' has been created successfully.\nAccount address: %s", profileFname, account)
}

// deriveSpenderIDFromKeystore derives the spender ID from the public key stored in the keystore.
// Works for ED25519 key types when the keystore has a valid public key field.
func deriveSpenderIDFromKeystore(ks *keystore.Keystore) string {
	if ks.KeyType != keystore.KeyTypeED25519 {
		return ""
	}
	pubBytes, err := keystore.PublicKeyBytes(ks)
	if err != nil {
		return ""
	}
	return ledger.SigLockFromED25519PublicKey(pubBytes).String()
}

const walletProfileTemplate = `# Proxi wallet profile

# default sequencer ID is used when own or tag-along sequencer is not specified
default_sequencer_id: {{.BootstrapSeqID}}

wallet:
    key_file: {{.KeyFile}}
    account: {{.Account}}
    # <own sequencer ID> must be the sequencer ID controlled by the private key of the wallet.
    # The controller wallet can withdraw tokens from the sequencer chain with command 'proxi node seq withdraw'
    # Default is used when not specified
    sequencer_id: <own sequencer ID>
api:
    endpoint: http://63.250.56.190:8001

# alternative testnet access points:
#    endpoint: http://113.30.191.219:8001
#    endpoint: http://83.229.84.197:8001
#    endpoint: http://5.180.181.103:8001

tag_along:
    # tag-along fee amount and ID of the tag-along sequencer. Currently only one tag-along sequencer is supported
    # If not specified, the default sequencer ID will be used
    fee: 1
# uncomment the line and specify your preferred sequencer
#    sequencer_id: <tag-along sequencer ID>

# provides parameters for 'proxi node getfunds' command
faucet:
    port:  9500
    host:  113.30.191.219

# provides parameters for 'proxi node spam' command
# The spammer in a loop sends bundles of transactions to the target address by using specified tag-along sequencer
# Before sending next bundle, the spammer waits for the finality of the previous according to the provided criterion
spammer:
    bundle_size: 5
    output_amount: 1000
    pace: 25
    tag_along:
        fee: 1
        # <sequencer ID hex encoded> is tag-along sequencer id for the tip transaction in the bundle
        # If not specified, the default sequencer ID will be used
        # sequencer_id: <sequencer id hex encoded>
    # target address
    target: <target lock in EasyFL format>
`
