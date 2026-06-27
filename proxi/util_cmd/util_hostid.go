package util_cmd

import (
	"crypto/ed25519"
	"crypto/rand"
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
		Short: fmt.Sprintf("generates private key and host id for libp2p host"),
		PersistentPreRun: func(_ *cobra.Command, _ []string) {},
		Run: runGenHostIdCmd,
	}
	return genHostIdCommand
}

func runGenHostIdCmd(_ *cobra.Command, _ []string) {
	glb.Infof("This program generates a private key from system randomness.")
	glb.Infof("The private key will be displayed on the screen. It is intended for the libp2p host identification only")
	glb.Infof("IT SHOULD NEVER BE USED TO PROTECT TOKENS IN CRYPTO ACCOUNTS\n")
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	util.AssertNoError(err)

	pklpp, err := p2pcrypto.UnmarshalEd25519PrivateKey(privateKey)
	util.AssertNoError(err)

	hid, err := peer.IDFromPrivateKey(pklpp)
	glb.Infof("------>")
	glb.Infof("libp2p host private key: %s", hex.EncodeToString(privateKey))
	glb.Infof("libp2p host id: %s", hid.String())
}
