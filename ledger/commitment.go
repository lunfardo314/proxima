package ledger

import "github.com/lunfardo314/proxima/ledger/txbuildercore"

// This defines the commitment used in the trie of the state.
// It will be using blake2b 32-byte hash as a vector commitment method
// in hexary radix tree, i.e. internal node has up to 16 children (kind of Patricia).
// This is pretty optimal setup.
// Other options:
// - use 24 hash as commitment method (should be enough -> require storage)
// - use polynomial vector commitments instead of hash function (verkle tree)

// Defined wallet-side, where the library commitment proof is verified.
const (
	TrieArity    = txbuildercore.TrieArity
	TrieHashSize = txbuildercore.TrieHashSize
)

var CommitmentModel = txbuildercore.StateCommitmentModel
