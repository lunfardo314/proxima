package init_cmd

import (
	"bytes"
	"encoding/hex"
	"os"
	"text/template"

	p2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var includeSeq bool

func initNodeConfigCmd() *cobra.Command {
	initNodeConfig := &cobra.Command{
		Use:   "node",
		Args:  cobra.NoArgs,
		Short: "creates config file for the Proxima node",
		Run:   runNodeConfigCommand,
	}

	initNodeConfig.PersistentFlags().BoolVarP(&includeSeq, "sequencer", "s", false, "include sequencer config template")
	err := viper.BindPFlag("sequencer", initNodeConfig.PersistentFlags().Lookup("sequencer"))
	glb.AssertNoError(err)

	return initNodeConfig
}

const (
	proximaNodeProfile     = "proxima.yaml"
	peeringPort            = 4000
	apiPort                = 8000
	defaultMaxDynamicPeers = 5
)

type configFileData struct {
	HostPrivateKey string
	HostID         string
	HostPort       int
	Bootstrap      bool
	APIPort        int
	StaticPeers    []struct {
		Name      string
		MultiAddr string
	}
	MaxDynamicPeers int
	SequencerConfig string
}

func runNodeConfigCommand(_ *cobra.Command, _ []string) {
	templ := template.New("config")
	_, err := templ.Parse(configFileTemplate)
	glb.AssertNoError(err)

	glb.Assertf(!glb.FileExists(proximaNodeProfile), "file %s already exists", proximaNodeProfile)
	var buf bytes.Buffer

	privateKey := glb.AskEntropyGenEd25519PrivateKey("enter at least 10 seed symbols:", 10)
	pklpp, err := p2pcrypto.UnmarshalEd25519PrivateKey(privateKey)
	util.AssertNoError(err)
	hid, err := peer.IDFromPrivateKey(pklpp)

	data := configFileData{
		HostPrivateKey:  hex.EncodeToString(privateKey),
		HostID:          hid.String(),
		HostPort:        peeringPort,
		Bootstrap:       false,
		APIPort:         apiPort,
		StaticPeers:     nil,
		MaxDynamicPeers: defaultMaxDynamicPeers,
	}
	if includeSeq {
		data.SequencerConfig = sequencerConfigTemplate
	}
	err = templ.Execute(&buf, data)
	glb.AssertNoError(err)

	err = os.WriteFile(proximaNodeProfile, buf.Bytes(), 0666)
	glb.AssertNoError(err)

	glb.Infof("initial Proxima node configuration file has been saved as '%s'", proximaNodeProfile)
}

const configFileTemplate = `# Configuration file for the Proxima node

# Peering configuration
peering:
  # libp2p host data:
  host:
    # host ID private key
    id_private_key: {{.HostPrivateKey}}
    # host ID is derived from the host ID public key.
    id: {{.HostID}}
    # port to connect from other peers
    port: {{.HostPort}}

  # YAML dictionary (map) of statically pre-configured peers. Also used in the peering boostrap phase by Kademlia DHT
  # It will be empty for the first bootstrap node in the network. In that case must be peering.host.bootstrap = true
  # Must be at least 1 static for non-bootstrap node
  # Each static peer is specified as a pair <name>: <multiaddr>, where:
  # -- <name> is unique mnemonic name used for convenience locally
  # -- <multiaddr> is the libp2p multi-address in the form '/ip4/<IPaddr ir URL>/<port>/tcp/p2p/<hostID>'
  # for more info see https://docs.libp2p.io/concepts/fundamentals/addressing/
  peers:
    # Example -> boot: /ip4/127.0.0.1/tcp/4000/p2p/12D3KooWL32QkXc8ZuraMJkLaoZjRXBkJVjRz7sxGWYwmzBFig3M

  # Maximum number of peers which may be connected to via the automatic peer discovery
  # max_dynamic_peers > 0 means automatic peer discovery (autopeering) is enabled, otherwise disabled
  max_dynamic_peers: {{.MaxDynamicPeers}}

# Node's API config
api:
    # server port
  port: {{.APIPort}}

snapshot:
  enable: false
    # where to put snapshot files. Directory must exist at startup
  directory: snapshot
    # 30 slots means snapshot is every ~5 min
  period_in_slots: 30
    # keep latest up to 3 snapshots, older ones will be purged
  keep_latest: 3

# logger config
# logger.previous can be 'erase' or 'save'
logger:
  # verbosity:
  #   0 - logging branches
  #   1 - logging branches, sequencer transaction
  #   2 - not implemented
  # for tracing configure trace_tags
  verbosity: 0
  output: proxima.log
  # options: 'erase' (previous will be erased), 'save' (previous will be saved and then deleted)
  # Otherwise or when absent: log will be appended in the same existing file
  previous: erase

# Other parameters used for tracing and debugging
# Prometheus metrics exposure
metrics:
  # expose Prometheus metrics yes/no
  enable: false
  port: 14000

# list of enabled trace tags. When enabled, it forces tracing of the specified module.
# It may be very verbose, so it is only used For debugging. 
# For more available trace tags search the code for "TraceTag"
trace_tags:
#  - autopeering
#  - pull_server
#  - txinput
#  - txStore

{{.SequencerConfig}}
`

const sequencerConfigTemplate = `
# Sequencer configuration (optional) 
sequencer:
    # sequencer name usually is 4 or so symbols. It is put into every sequencer transaction for tracking purposes  
  name: <mandatory name>
    # start sequencer yes/no
  enable: false
    # chain ID of the sequencer
    # chain ID af7bedde1fea222230b82d63d5b665ac75afbe4ad3f75999bb3386cf994a6963 is
    # predefined chain ID of the genesis chain (the bootstrap sequencer)
    # Sequencer chain is created by 'proxi node mkchain' command
    # All chains controlled by the wallet can be displayed by 'proxi node chains'
  chain_id: <sequencer ID hex encoded>
  # sequencer chain controller's private key (hex-encoded)
  controller_key: <ED25519 private key of the controller>
  # sequencer pace. Distance in ticks between two subsequent sequencer transactions
  # cannot be less than the sequencer pace value set by the ledger
  pace: 12
  # maximum tag-along inputs allowed in the sequencer transaction (maximum value is 254)
  max_tag_along_inputs: 100
`
