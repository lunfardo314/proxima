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
	initLedgerIDCmd := &cobra.Command{
		Use:   "node_config [<test nodes index 0-4>]",
		Args:  cobra.MaximumNArgs(1),
		Short: "creates initial config file for the Proxima node",
		Long:  `if parameter <test nodes index> is provided, generates 4 deterministic peers and peers with it`,
		Run:   runNodeConfigCommand,
	}
	return initLedgerIDCmd
}

const (
	proximaNodeProfile               = "proxima.yaml"
	deterministicSeedForTestingNodes = 31415926535 + 2718281828
	peeringPort                      = 4000
	apiPortStart                     = 8000
)

func runNodeConfigCommand(_ *cobra.Command, args []string) {
	if fileExists(proximaNodeProfile) {
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

const configFileTemplate = `# FOR TESTING ONLY!!! PRIVATE KEYS AND DERIVED DATA SHOULD NOT BE USED IN PRODUCTION
%s
#
# Peering configuration
peering:
  # libp2p host data: 
  host:
    # host ID private key (auto-generated, hex-encoded)
    id_private_key: %s
    # host ID is derived from the host ID public key. 
    id: %s
    # port to connect from other peers
    port: %d

  # configuration of known peers. Each known peer is specified as a pair <name>: <multiaddr>, where:
  # - <name> is unique mnemonic name used for convenience locally
  # - <multiaddr> is the libp2p multi-address in the form '/ip4/<IPaddr ir URL>/<port>/tcp/p2p/<hostID>'
  peers:
%s	

# Node's API config
api:
  server:
    # server port
    port: %d


# map of maps of sequencers <seq name>: <seq config>
# usually none or 1 sequencer is configured for the node
sequencers:
  boot:
    enable: false
    loglevel: info
    # chain ID of the sequencer
    sequencer_id: 
    # chain controller's private key (hex-encoded)
    controller_key: 
    # sequencer pace
    pace: 5
    # maximum fee inputs allowed in the sequencer milestone transaction
    max_fee_inputs: 50
    trace_tippool: false

# logger config
logger:
  level: info
  output: testlog.log

# Other parameters used for tracing and debugging
# pprof config
pprof:
  enable: false
  port: 8080

# workflow debug config
workflow:
  addtx:
    loglevel: info
  events:
    loglevel: info
  input:
    loglevel: info
  reject:
    loglevel: info
  prevalid:
    loglevel: info
  solidify:
    loglevel: info
  validate:
    loglevel: info

# specify milestone proposer strategies to trace
trace_proposers:
  base: false
  btrack2: false
`
