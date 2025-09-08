package init_cmd

import (
	"bytes"
	"encoding/hex"
	"os"
	"text/template"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initWalletCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "wallet [<profile name. Default: 'proxi'>]",
		Args:  cobra.MaximumNArgs(1),
		Short: "initializes new proxi wallet profile proxi.yaml with generated private key",
		Run:   runInitWalletCommand,
	}
}

func runInitWalletCommand(_ *cobra.Command, args []string) {
	templ := template.New("wallet")
	_, err := templ.Parse(walletProfileTemplate)
	glb.AssertNoError(err)

	profileName := "proxi"
	if len(args) > 0 {
		profileName = args[0]
	}
	profileFname := profileName + ".yaml"
	glb.Assertf(!glb.FileExists(profileFname), "file %s already exists", profileFname)

	privateKey := glb.AskEntropyGenEd25519PrivateKey(
		"we need some entropy from you for the private key of the account\nPlease enter at least 10 seed symbols as randomly as possible and press ENTER:", 10)

	data := struct {
		PrivateKey     string
		Account        string
		BootstrapSeqID string
	}{
		PrivateKey:     hex.EncodeToString(privateKey),
		Account:        ledger.AddressED25519FromPrivateKey(privateKey).String(),
		BootstrapSeqID: ledger.BoostrapSequencerIDHex,
	}
	var buf bytes.Buffer
	err = templ.Execute(&buf, data)
	glb.AssertNoError(err)

	err = os.WriteFile(profileFname, buf.Bytes(), 0666)
	glb.AssertNoError(err)
	glb.Infof("proxi profile '%s' has been created successfully.\nAccount address: %s", profileFname, data.Account)
}

const walletProfileTemplate = `# Proxi wallet profile

// the default sequencer id when not specified own sequencer, tag-along or tag-along for spamming
default_sequencer_id: {{.BootstrapSeqID}}

wallet:
    private_key: {{.PrivateKey}}
    account: {{.Account}}
    # <own sequencer id> must be own sequencer id, i.e. controlled by the private key of the wallet.
    # The controller wallet can withdraw tokens from the sequencer chain with command 'proxi node seq withdraw'
    sequencer_id: <own sequencer id>
api:
    # API endpoint of the node 
    endpoint: http://127.0.0.1:8000

tag_along:
    # id of the tag-along sequencer. Currently only one tag-along sequencer is supported
    # If not specified, the default sequencer ID will be used
    # uncomment the line and specify your preferred sequencer
#    sequencer_id: <your sequencer ID>
    fee: 200

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
        fee: 50
        # <sequencer ID hex encoded> is tag-along sequencer id for the tip transaction in the bundle
        # If not specified, the default sequencer id will be used
        # sequencer_id: <sequencer id hex encoded>
    # target address
    target: <target lock in EasyFL format>
`
