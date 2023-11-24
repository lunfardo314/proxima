package init_cmd

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initNodeConfigCmd() *cobra.Command {
	initLedgerIDCmd := &cobra.Command{
		Use:   "node_config",
		Args:  cobra.NoArgs,
		Short: "creates initial config file for the Proxima node",
		Run:   runNodeConfigCommand,
	}
	return initLedgerIDCmd
}

const proximaNodeProfile = "proxima.yaml"

func runNodeConfigCommand(_ *cobra.Command, _ []string) {
	mustNotExist(proximaNodeProfile)
	_, hostPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	glb.AssertNoError(err)
	lppHostPrivateKey, err := crypto.UnmarshalEd25519PrivateKey(hostPrivateKey)
	glb.AssertNoError(err)
	hostID, err := peer.IDFromPrivateKey(lppHostPrivateKey)
	glb.AssertNoError(err)

	yamlStr := fmt.Sprintf(configFileTemplate, hex.EncodeToString(hostPrivateKey), hostID.String())
	err = os.WriteFile(proximaNodeProfile, []byte(yamlStr), 0666)
	glb.AssertNoError(err)

	glb.Infof("initial Proxima node configuration file has been saved as 'proxima.yaml'")
}

const configFileTemplate = `# Configuration of the Proxima node
#
# Peering configuration
peering:
  # libp2p host data: 
  host:
    # host ID private key (auto-generated, hex-encoded)
    id_private_key: %s
    # host ID is derived from the host ID public key. 
    id: %s
    # port to connect to other peers
    port: 4000

  # configuration of known peers. Each known peer is specified as a pair <name>: <multiaddr>, where:
  # - <name> is unique mnemonic name used for convenience locally
  # - <multiaddr> is the libp2p multi-address in the form '/ip4/<IPaddr ir URL>/<port>/tcp/p2p/<hostID>'
  peers:
    # peer0: "<multiaddr0>"
    # peer1: "<multiaddr1>"

# Node's API config
api:
  server:
    # server port
    port: 8000


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
