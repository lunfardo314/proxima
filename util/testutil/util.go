package testutil

import (
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"

	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
	"golang.org/x/crypto/blake2b"
)

// TestingDeterministicPrivateKey is used as a seed to generate deterministic private keys
const TestingDeterministicPrivateKey = "8ec47313c15c3a4443c41619735109b56bc818f4a6b71d6a1f186ec96d15f28f14117899305d99fb4775de9223ce9886cfaa3195da1e40c5db47c61266f04dd2"

func GetTestingPrivateKey(idx ...int) ed25519.PrivateKey {
	var ret ed25519.PrivateKey
	if len(idx) == 0 {
		pkBin, err := hex.DecodeString(TestingDeterministicPrivateKey)
		ret = pkBin
		util.AssertNoError(err)
	} else {
		var u64 [8]byte
		binary.BigEndian.PutUint64(u64[:], uint64(idx[0]))
		seed := blake2b.Sum256(common.Concat([]byte(TestingDeterministicPrivateKey), u64[:]))
		ret = ed25519.NewKeyFromSeed(seed[:])
	}
	return ret
}

func GetTestingPrivateKeys(n int, offsIndex ...int) []ed25519.PrivateKey {
	offs := 31415
	if len(offsIndex) > 0 {
		offs = offsIndex[0]
	}
	ret := make([]ed25519.PrivateKey, n)
	for i := range ret {
		ret[i] = GetTestingPrivateKey(offs + i + 1)
	}
	return ret
}
