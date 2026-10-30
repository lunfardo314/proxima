package mine

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/stretchr/testify/require"
)

// The fixture is public take 1 data: the ledger library, the mine transit
// 000016d30202a8b9.. and the transaction producing the mine output it spends.

// mineTransitFixture loads the fixture, initialises the take 1 library and
// returns the transit, wrapped as a full vertex with no flags, and the index of
// its tag-along output.
func mineTransitFixture(t *testing.T) (*vertex.WrappedTx, byte) {
	var fixture struct {
		LibraryJSON string `json:"library_json"`
		TxID        string `json:"txid"`
		TxBytes     string `json:"tx_bytes"`
		ParentBytes string `json:"parent_bytes"`
	}
	raw, err := os.ReadFile("testdata/mine_transit_5843.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &fixture))
	ledger.MustInitLibraryCacheFromJSON([]byte(fixture.LibraryJSON))
	t.Cleanup(ledger.ResetForTesting)

	txBytes, err := hex.DecodeString(fixture.TxBytes)
	require.NoError(t, err)
	tx, err := transaction.ParseWithPartialValidation(txBytes)
	require.NoError(t, err)
	require.Equal(t, fixture.TxID, tx.IDStringHex())
	require.True(t, tx.IsMiningTransaction())

	// the transit is valid against its real parent: what is tested below is the
	// sequencer's handling of a valid transit, not of a bad proof
	parentBytes, err := hex.DecodeString(fixture.ParentBytes)
	require.NoError(t, err)
	parent, err := transaction.ParseWithPartialValidation(parentBytes)
	require.NoError(t, err)
	require.NoError(t, tx.SetFullContextWithFetch(func(oid base.OutputID) ([]byte, bool) {
		if oid.TransactionID() != parent.ID() || int(oid.Index()) >= parent.NumProducedOutputs() {
			return nil, false
		}
		return parent.MustOutputDataAt(oid.Index()), true
	}))
	require.NoError(t, tx.ValidateFullContext())

	vid := vertex.WrapTxID(tx.ID())
	vid.UnwrapVirtualTx(func(_ *vertex.VirtualTransaction) {
		vid.ConvertVirtualTxToVertexNoLock(vertex.NewVertex(tx))
	})
	for i := 0; i < tx.NumProducedOutputs(); i++ {
		if tx.MustProducedOutputAt(byte(i)).Lock().Name() == ledger.TagAlongLockName {
			return vid, byte(i)
		}
	}
	t.Fatal("the transit has no tag-along output")
	return nil, 0
}

// fixtureParentBytes is the transaction producing the mine output the fixture
// transit spends.
func fixtureParentBytes(t *testing.T) []byte {
	var fixture struct {
		ParentBytes string `json:"parent_bytes"`
	}
	raw, err := os.ReadFile("testdata/mine_transit_5843.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &fixture))
	ret, err := hex.DecodeString(fixture.ParentBytes)
	require.NoError(t, err)
	return ret
}
