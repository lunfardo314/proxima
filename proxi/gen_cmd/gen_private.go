package gen_cmd

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func genEd25519Cmd() *cobra.Command {
	genPrivateCommand := &cobra.Command{
		Use:   "ed25519",
		Args:  cobra.NoArgs,
		Short: fmt.Sprintf("generates ED25519 private key, public key and Proxima address"),
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			glb.ReadInConfig()
		},
		Run: runGenEd25519Cmd,
	}
	return genPrivateCommand
}

func runGenEd25519Cmd(_ *cobra.Command, _ []string) {
	glb.Infof("DISCLAIMER: USE AT YOUR OWN RISK!!. This program generates private key based on system randomness and on the entropy entered by the user")
	privateKey := askEntropyGenEd25519PrivateKey()
	publicKey := privateKey.Public().(ed25519.PublicKey)
	addr := ledger.AddressED25519FromPrivateKey(privateKey)

	glb.Infof("------>")
	glb.Infof("Priv: %s", hex.EncodeToString(privateKey))
	glb.Infof("Pub: %s", hex.EncodeToString(publicKey))
	glb.Infof("Address: %s", addr.String())
}
