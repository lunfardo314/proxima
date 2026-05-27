package config_cmd

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

var (
	includeSeq        bool
	includeStandalone bool
	includeTrace      bool
)

func configNodeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "node",
		Args:  cobra.NoArgs,
		Short: "creates config file for the Proxima node",
		Run:   runConfigNodeCommand,
	}

	cmd.PersistentFlags().BoolVarP(&includeSeq, "sequencer", "s", false, "include generic sequencer config template (disabled, placeholder chain ID)")
	err := viper.BindPFlag("sequencer", cmd.PersistentFlags().Lookup("sequencer"))
	glb.AssertNoError(err)

	cmd.PersistentFlags().BoolVar(&includeStandalone, "standalone", false, "include enabled bootstrap sequencer config with standalone=true (single-node dev network)")
	err = viper.BindPFlag("standalone", cmd.PersistentFlags().Lookup("standalone"))
	glb.AssertNoError(err)

	cmd.PersistentFlags().BoolVarP(&includeTrace, "trace", "t", false, "include trace_tags and txlogger config sections (disabled)")
	err = viper.BindPFlag("trace", cmd.PersistentFlags().Lookup("trace"))
	glb.AssertNoError(err)

	return cmd
}

const (
	proximaNodeProfile     = "proxima.yaml"
	peeringPort            = 4000
	apiPort                = 8000
	defaultMaxDynamicPeers = 10
)

type configFileData struct {
	HostPrivateKey string
	HostID         string
	HostPort       int
	APIPort        int
	StaticPeers    []struct {
		Name      string
		MultiAddr string
	}
	MaxDynamicPeers  int
	IncludeTrace     bool
	IncludeSequencer bool
	SeqName          string
	SeqEnable        string
	SeqChainID       string
	Standalone       bool
}

func runConfigNodeCommand(_ *cobra.Command, _ []string) {
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
		HostPrivateKey:   hex.EncodeToString(privateKey),
		HostID:           hid.String(),
		HostPort:         peeringPort,
		APIPort:          apiPort,
		StaticPeers:      nil,
		MaxDynamicPeers:  defaultMaxDynamicPeers,
		IncludeTrace:     includeTrace,
		IncludeSequencer: includeSeq || includeStandalone,
		SeqName:          "",
		SeqEnable:        "false",
		SeqChainID:       "<sequencer id hex encoded>",
	}
	if includeStandalone {
		data.SeqName = "boot"
		data.SeqEnable = "true"
		data.SeqChainID = ledger.BoostrapSequencerIDHex
		data.Standalone = true
	}
	err = templ.Execute(&buf, data)
	glb.AssertNoError(err)

	err = os.WriteFile(proximaNodeProfile, buf.Bytes(), 0600)
	glb.AssertNoError(err)

	glb.Infof("initial Proxima node configuration file has been saved as '%s'", proximaNodeProfile)
}
