package init_cmd

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/peering"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/spf13/cobra"
)

func initNodeConfigCmd() *cobra.Command {
	initNodeConfig := &cobra.Command{
		Use:   "node_config",
		Args:  cobra.MaximumNArgs(1),
		Short: "creates config file for the Proxima node",
		Run:   runNodeConfigCommand,
	}
	return initNodeConfig
}

const (
	proximaNodeProfile               = "proxima.yaml"
	deterministicSeedForTestingNodes = 31415926535 + 2718281828
	peeringPort                      = 4000
	apiPortStart                     = 8000
)

func runNodeConfigCommand(_ *cobra.Command, args []string) {
	if glb.FileExists(proximaNodeProfile) {
		prompt := fmt.Sprintf("file %s already exists. Overwrite?", proximaNodeProfile)
		if !glb.YesNoPrompt(prompt, false) {
			glb.Fatalf("exit")
		}
	}

	generatePeering := len(args) > 0
	var err error
	var peerConfig string
	var hostPrivateKey ed25519.PrivateKey
	var hostID peer.ID

	hostPort := peeringPort
	apiPort := apiPortStart
	var intro string
	if generatePeering {
		intro = "# Testing configuration of the Proxima node with 4 other peers on the same machine\n" +
			"# All private keys are auto-generated deterministically"
		var hostPeerIndex int
		var idPrivateKeys []ed25519.PrivateKey
		hostPeerIndex, err = strconv.Atoi(args[0])
		glb.AssertNoError(err)
		glb.Assertf(0 <= hostPeerIndex && hostPeerIndex <= 4, "argument must be one of: 0,1,2,3 or 4")
		idPrivateKeys = testutil.GetTestingPrivateKeys(5, deterministicSeedForTestingNodes)

		peeringCfgLines := lines.New("    ")
		for i := 0; i < 5; i++ {
			lppPK, err := crypto.UnmarshalEd25519PrivateKey(idPrivateKeys[i])
			glb.AssertNoError(err)
			peerID, err := peer.IDFromPrivateKey(lppPK)
			glb.AssertNoError(err)
			if i != hostPeerIndex {
				peeringCfgLines.Add("peer%d: %s", i, peering.TestMultiAddrString(peerID, peeringPort+i))
			} else {
				hostID = peerID
				hostPrivateKey = idPrivateKeys[i]
				hostPort = peeringPort + i
				apiPort = apiPortStart + i
			}
		}
		peerConfig = peeringCfgLines.String()
	} else {
		intro = "# Configuration of the Proxima node"
		_, hostPrivateKey, err = ed25519.GenerateKey(rand.Reader)
		glb.AssertNoError(err)
		lppHostPrivateKey, err := crypto.UnmarshalEd25519PrivateKey(hostPrivateKey)
		glb.AssertNoError(err)
		hostID, err = peer.IDFromPrivateKey(lppHostPrivateKey)
		glb.AssertNoError(err)
		peerConfig =
			`    # peer0: "<multiaddr0>"
    # peer1: "<multiaddr1>"
`
	}

	yamlStr := fmt.Sprintf(configFileTemplate,
		intro,
		hex.EncodeToString(hostPrivateKey),
		hostID.String(),
		hostPort,
		peerConfig,
		apiPort,
	)
	err = os.WriteFile(proximaNodeProfile, []byte(yamlStr), 0666)
	glb.AssertNoError(err)

	glb.Infof("initial Proxima node configuration file has been saved as 'proxima.yaml'")
}

const configFileTemplate = `# Configuration file for the Proxima node
#
# Peering configuration
peering:
  # libp2p host data:
  host:
    # host ID private key
    id_private_key: {{.HostPrivateKey}}
    # host ID is derived from the host ID public key.
    id: {{HostID}}
    # port to connect from other peers
    port: {{.HostPort}}
    # For the first bootstrap node this must be true
    # If false or absent, it will require at least one statically configured peer
    bootstrap: {{.Bootstrap}}

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
  max_dynamic_peers: 3

# Node's API config
api:
    # server port
  port: {{.APIPort}}

# map of maps of sequencers <seq name>: <seq config>
# usually none or 1 sequencer is configured for the node
# 0 enabled sequencers means node is a pure access node
# If sequencer is enabled, it is working as a separate and independent process inside the node
sequencers:
  boot:
    # start sequencer if true
    enable: true
    # chain ID of the sequencer
    # chain ID af7bedde1fea222230b82d63d5b665ac75afbe4ad3f75999bb3386cf994a6963 is
    # predefined chain ID of the genesis chain (the bootstrap sequencer)
    # Sequencer chain is created by 'proxi node mkchain' command
    # All chains controlled by the wallet can be displayed by 'proxi node chains'
    sequencer_id: <sequencer ID hex encoded>
    # sequencer chain controller's private key (hex-encoded)
    controller_key: <ED25519 private key of the controller>
    # sequencer pace. Distance in ticks between two subsequent sequencer transactions
    # cannot be less than the sequencer pace value set by the ledger
    pace: 5
    # maximum tag-along inputs allowed in the sequencer transaction (maximum value is 254)
    max_tag_along_inputs: 100

# logger config
# logger.previous can be 'erase' or 'save'
logger:
  level: info
  output: proxima.log
  # options: 'erase' (previous will be erased), 'save' (previous will be saved and then deleted)
  # Otherwise or when absent: log will be appended in the same existing file
  previous: erase
#  log_attacher_stats: true

# Other parameters used for tracing and debugging
# Prometheus metrics exposure
metrics:
  # if false or absent Prometheus metrics are not exposed
  enable: false
  port: 14000

# list of enabled trace tags.
# When enabled, it forces tracing of the specified module.
# It may be very verbose
# Search the code for "TraceTag"
trace_tags:
#  - autopeering
#  - inclusion
#  - backlog
#  - gossip
#  - pull_server
#  - txinput
#  - txStore
#  - global
#  - backlog
#  - propose-base
#  - pruner`
