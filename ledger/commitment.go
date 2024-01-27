package ledger

import (
	"crypto/rand"

	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
	"github.com/lunfardo314/unitrie/models/trie_blake2b"
)

// This defines the commitment used in the trie of the state.
// It will be using blake2b 32-byte hash as a vector commitment method
// in hexary radix tree, i.e. internal node has up to 16 children (kind of Patricia).
// This is pretty optimal setup.
// Other possibilities:
// - use 24 hash as commitment method (should be enough -> require storage)
// - use polynomial vector commitments instead of hash function (verkle tree)

const (
	TrieArity    = common.PathArity16
	TrieHashSize = trie_blake2b.HashSize256
)

var CommitmentModel = trie_blake2b.New(TrieArity, TrieHashSize)

// RandomVCommitment for testing
func RandomVCommitment() common.VCommitment {
	var data [TrieHashSize]byte
	n, err := rand.Read(data[:])
	util.AssertNoError(err)
	util.Assertf(n == 32, "n == 32")
	ret, err := common.VectorCommitmentFromBytes(CommitmentModel, data[:])
	util.AssertNoError(err)
	return ret
}
