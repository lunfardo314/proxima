package ledger

import (
	"github.com/lunfardo314/unitrie/common"
	"github.com/lunfardo314/unitrie/models/trie_blake2b"
)

const (
	TrieArity    = common.PathArity16
	TrieHashSize = trie_blake2b.HashSize256
)

var CommitmentModel = trie_blake2b.New(TrieArity, TrieHashSize)
