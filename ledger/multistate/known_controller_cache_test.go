package multistate

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

// IsKnownController reads the candidate outputs from the trie and leaves nothing in the reader's
// caches: the candidates come from the transaction being checked, and a cache entry per candidate,
// kept for the life of a shared reader, would grow with whatever IDs transactions name. An owned
// candidate still makes the account known.
func TestKnownControllerCandidatesNotCached(t *testing.T) {
	ledger.InitWithTestingLedgerData()
	store := common.NewInMemoryKVStore()
	_, root := InitStateStoreFromGlobals(store)
	rdr := MustNewReadable(store, root)

	made := make([]base.OutputID, 256)
	for i := range made {
		var txid base.TransactionID
		txid[0], txid[len(txid)-1] = byte(i), 1
		oid, err := base.NewOutputID(txid, 0)
		require.NoError(t, err)
		made[i] = oid
	}
	var account base.HolderID
	account[0] = 7
	require.False(t, rdr.IsKnownController(ledger.SigLock(account).ControllerID(), made...))

	stem := ledger.GenesisStemOutput().ID
	require.True(t, rdr.IsKnownController(ledger.StemAccountID, append(made, stem)...), "an owned candidate")

	require.Empty(t, rdr.txCache, "no transaction record cached for a candidate")
	require.Empty(t, rdr.utxoCache, "no output cached for a candidate")
}
