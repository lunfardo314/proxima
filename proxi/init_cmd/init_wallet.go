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

	privateKey := glb.AskEntropyGenEd25519PrivateKey("enter at least 10 seed symbols:", 10)

	data := struct {
		PrivateKey string
		Account    string
	}{
		PrivateKey: hex.EncodeToString(privateKey),
		Account:    ledger.AddressED25519FromPrivateKey(privateKey).String(),
	}
	var buf bytes.Buffer
	err = templ.Execute(&buf, data)
	glb.AssertNoError(err)

	err = os.WriteFile(profileFname, buf.Bytes(), 0666)
	glb.AssertNoError(err)
	glb.Infof("proxi profile '%s' has been created successfully.\nAccount address: %s", profileFname, data.Account)
}

const walletProfileTemplate = `# Proxi wallet profile
wallet:
    private_key: {{.PrivateKey}}
    account: {{.Account}}
    # <own sequencer ID> must be own sequencer ID, i.e. controlled by the private key of the wallet.
    # The controller wallet can withdraw tokens from the sequencer chain with command
    # 'proxi node seq withdraw'
    sequencer_id: <own sequencer ID>
api:
    # node API endpoint  
    endpoint: http://127.0.0.1:8000

tag_along:
    # ID of the tag-along sequencer. Currently only one
    # In the bootstrap phase it normally is bootstrap chain ID: af7bedde1fea222230b82d63d5b665ac75afbe4ad3f75999bb3386cf994a6963
    # Later it is up to the wallet owner
    sequencer_id: af7bedde1fea222230b82d63d5b665ac75afbe4ad3f75999bb3386cf994a6963
    fee: 500
finality:
    # finality rule used by the wallet. It has no effect on the way transaction is treated by the network
    inclusion_threshold:
        # numerator / denominator is the 'theta' in the WP.
        # with strong finality, the wallet waits until all branches in last 2 slots with coverage > numerator / denominator
        # contains the transaction
        # With weak finality inclusion_threshold has no effect
        numerator: 2
        denominator: 3
        # strong: based on ledger coverage and inclusion_threshold (see above)
        # weak: wait until all branches in last 2 slots contains the transaction.
        # The weak finality may be used when less than totalSupply/2 of sequencers are active,
        # for example during bootstrap
    weak: false

# provides parameters for 'proxi node spam' command
# The spammer in a loop sends bundles of transactions to the target address by using specified tag-along sequencer
# Before sending next bundle, the spammer waits for the finality of the previous according to the provided criterion
spammer:
    bundle_size: 5
    max_duration_minutes: 0
    max_transactions: 0
    output_amount: 1000
    pace: 10
    scenario: standard
    submit_nowait: false
    tag_along:
        fee: 500
        # <sequencer ID hex encoded> is tag-along sequencer ID for the tip transaction in the bundle
        # For example the bootstrap sequencer af7bedde1fea222230b82d63d5b665ac75afbe4ad3f75999bb3386cf994a6963
        sequencer: <sequencer ID hex encoded>
    # target address
    target: <target address in EasyFL format>
    trace_on_node: false
`
