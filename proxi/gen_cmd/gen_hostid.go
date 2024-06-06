package gen_cmd

import (
	"encoding/hex"
	"fmt"

	p2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

func genHostIDCmd() *cobra.Command {
	genHostIdCommand := &cobra.Command{
		Use:   "hostid",
		Args:  cobra.NoArgs,
		Short: fmt.Sprintf("generates private key and host ID for libp2p host"),
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			glb.ReadInConfig()
		},
		Run: runGenHostIdCmd,
	}
	return genHostIdCommand
}

func runGenHostIdCmd(_ *cobra.Command, _ []string) {
	glb.Infof("DISCLAIMER: USE AT YOUR OWN RISK!!. This program generates private key based on system randomness and on the entropy entered by the user")
	privateKey := askEntropyGenEd25519PrivateKey()

	pklpp, err := p2pcrypto.UnmarshalEd25519PrivateKey(privateKey)
	util.AssertNoError(err)

	hid, err := peer.IDFromPrivateKey(pklpp)
	glb.Infof("------>")
	glb.Infof("libp2p host private key: %s", hex.EncodeToString(privateKey))
	glb.Infof("libp2p host ID: %s", hid.String())
}
