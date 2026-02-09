package init_cmd

import (
	"bytes"
	_ "embed"
	"encoding/hex"
	"os"
	"text/template"

	p2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

//go:embed node_config.template
var configFileTemplate string

//go:embed sequencer_config.template
var sequencerConfigTemplate string

var (
	includeSeq  bool
	includeBoot bool
)

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

	initNodeConfig.PersistentFlags().BoolVarP(&includeBoot, "boot", "b", false, "include enabled bootstrap sequencer config")
	err = viper.BindPFlag("boot", initNodeConfig.PersistentFlags().Lookup("boot"))
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

	privateKey := glb.AskEntropyGenEd25519PrivateKey("please enter at least 10 random seed symbols for the private key and ID of the peering host and press ENTER:", 10)
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
	if includeSeq || includeBoot {
		seqData := struct {
			SeqName    string
			SeqEnable  string
			SeqChainID string
		}{
			SeqName:    "<mandatory name>",
			SeqEnable:  "false",
			SeqChainID: "<sequencer id hex encoded>",
		}
		if includeBoot {
			seqData.SeqName = "boot"
			seqData.SeqEnable = "true"
			seqData.SeqChainID = ledger.BoostrapSequencerIDHex
		}
		seqTempl, errSeq := template.New("seq").Parse(sequencerConfigTemplate)
		glb.AssertNoError(errSeq)
		var seqBuf bytes.Buffer
		errSeq = seqTempl.Execute(&seqBuf, seqData)
		glb.AssertNoError(errSeq)
		data.SequencerConfig = seqBuf.String()
	}
	err = templ.Execute(&buf, data)
	glb.AssertNoError(err)

	err = os.WriteFile(proximaNodeProfile, buf.Bytes(), 0600)
	glb.AssertNoError(err)

	glb.Infof("initial Proxima node configuration file has been saved as '%s'", proximaNodeProfile)
}
