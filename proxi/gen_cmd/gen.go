package gen_cmd

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"os"

	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/unitrie/common"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/blake2b"
)

func Init() *cobra.Command {
	genCmd := &cobra.Command{
		Use:   "gen",
		Args:  cobra.NoArgs,
		Short: "utility data generation functions",
		Run: func(cmd *cobra.Command, args []string) {
		},
	}
	genCmd.AddCommand(
		genEd25519Cmd(),
		genHostIDCmd(),
	)
	genCmd.InitDefaultHelpCmd()
	return genCmd
}

func askEntropyGenEd25519PrivateKey() ed25519.PrivateKey {
	glb.Infof("please enter 10 or more random seed symbols: ")
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Scan()
	seedSynbols := scanner.Bytes()
	if len(seedSynbols) < 10 {
		glb.Infof("error: must be at least 10 seed symbols")
		os.Exit(1)
	}

	var rndBytes [32]byte
	n, err := rand.Read(rndBytes[:])
	glb.AssertNoError(err)
	glb.Assertf(n == 32, "error while generating random bytes")

	seed := blake2b.Sum256(common.ConcatBytes(seedSynbols, rndBytes[:]))
	return ed25519.NewKeyFromSeed(seed[:])
}
