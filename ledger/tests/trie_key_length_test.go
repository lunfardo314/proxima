package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/unitrie/immutable"
	"github.com/stretchr/testify/require"
)

// The ledger's commitment model declares a minimum key length of 12 bytes, which
// keeps the commitment the state has always had: node paths that fit in the hash
// size stay unhashed, unambiguous only with keys that long. The trie refuses a
// shorter key with a panic, so a key family under the minimum would crash a node.
// This test builds a state with every key family (outputs, transaction records,
// account index, chain IDs) and checks that each key meets the minimum: a new key
// family breaking it fails here.
func TestTrieKeysAreLong(t *testing.T) {
	u, privKey, srcAddr := newTestEnv(t, 1_000_000_000)
	// an ordinary transfer: transaction record, outputs and account entries
	extra := make([]byte, 1) // the shortest non-empty index value
	extra[0] = 0x55
	produceWithExtraIndexValue(t, u, privKey, srcAddr, extra)

	rdr, err := immutable.NewTrieReader(ledger.CommitmentModel, u.Store(), u.Root())
	require.NoError(t, err)

	perPartition := make(map[byte]int)
	rdr.IterateKeys(func(k []byte) bool {
		if len(k) == 0 {
			return true // the root identity
		}
		require.GreaterOrEqual(t, len(k), ledger.CommitmentModel.MinKeyLength(), "key %x", k)
		perPartition[k[0]]++
		return true
	})
	// every key family is present, so the check covered all of them
	for _, p := range []byte{multistate.TriePartitionLedgerState, multistate.TriePartitionControllers, multistate.TriePartitionChainID} {
		require.Positive(t, perPartition[p], "no key in partition %s", multistate.PartitionToString(p))
	}
}
