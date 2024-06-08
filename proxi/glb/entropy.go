package glb

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"os"

	"github.com/lunfardo314/unitrie/common"
	"golang.org/x/crypto/blake2b"
)

func AskEntropyGenEd25519PrivateKey(msg string, minSeedLength ...int) ed25519.PrivateKey {
	const minimumSeedLength = 10

	seedLen := minimumSeedLength
	if len(minSeedLength) > 0 && minSeedLength[0] > minimumSeedLength {
		seedLen = minSeedLength[0]
	}

	Infof(msg)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Scan()
	seedSymbols := scanner.Bytes()
	if len(seedSymbols) < seedLen {
		Infof("error: must be at least %d seed symbols", seedLen)
		os.Exit(1)
	}

	var rndBytes [32]byte
	n, err := rand.Read(rndBytes[:])
	AssertNoError(err)
	Assertf(n == 32, "error while generating random bytes")

	seed := blake2b.Sum256(common.ConcatBytes(seedSymbols, rndBytes[:]))
	return ed25519.NewKeyFromSeed(seed[:])
}
