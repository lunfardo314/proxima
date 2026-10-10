package multistate

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

// TestOutputIsConsumed pins the predicate the sequencer backlog purges by:
// an output counts as consumed only when the state knows its transaction and
// lacks the output. An output whose transaction the state does not know at
// all was never included and must not count as consumed, or a request that
// arrived while the reliable branch stalled would be purged as if spent. The
// genesis state is the fixture: the stem output is unspent, an output index
// the genesis transaction never produced is absent under a known transaction,
// and a made-up transaction ID is unknown.
func TestOutputIsConsumed(t *testing.T) {
	ledger.InitWithTestingLedgerData()

	store := common.NewInMemoryKVStore()
	_, root := InitStateStoreFromGlobals(store)
	rdr := MakeSugared(MustNewReadable(store, root))

	stem := ledger.GenesisStemOutput().ID
	require.True(t, rdr.KnowsCommittedTransaction(stem.TransactionID()))
	require.False(t, rdr.OutputIsConsumed(stem), "unspent output of a known transaction")

	// an index the genesis transaction never produced; set directly, since the
	// constructor refuses an index beyond the count the transaction ID carries
	missing := stem
	missing[len(missing)-1] = 200
	require.True(t, rdr.OutputIsConsumed(missing), "absent output of a known transaction")

	var unknown base.TransactionID
	unknown[len(unknown)-1] = 1
	never, err := base.NewOutputID(unknown, 0)
	require.NoError(t, err)
	require.False(t, rdr.HasUTXO(never))
	require.False(t, rdr.OutputIsConsumed(never), "unknown transaction: never included, not consumed")
}
