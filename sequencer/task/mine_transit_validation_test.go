package task

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

// The canonical winner rule must recognise a mine transit with the flags a
// normal non-sequencer attachment leaves on it. Such a vertex has its
// constraints validated but never becomes Good globally: it is defined only in
// its attacher's past cone. When the rule required Good, every live transit
// passed as an ordinary tag-along and was consumed early and unranked (take 1,
// slot 5843: both competing transits consumed at tick 14, settlement from 86).
//
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

func candidateOf(vid *vertex.WrappedTx, index byte) *_inputCandidate {
	c := &_inputCandidate{wOut: vertex.WrappedOutput{VID: vid, Index: index}}
	c.o = c.wOut.OutputWithID()
	mineTransitDescriptor(c)
	return c
}

func TestMineTransitValidatedNonSequencer(t *testing.T) {
	vid, index := mineTransitFixture(t)
	slot := vid.Slot()

	// attachment started, constraints not validated yet: the transit is
	// recognised, but pending, and held whatever the target timestamp
	vid.SetFlagsUp(vertex.FlagVertexTxAttachmentStarted)
	c := candidateOf(vid, index)
	require.NotNil(t, c.mine)
	require.True(t, c.mine.pending)
	require.Empty(t, settleMineTransits([]*_inputCandidate{c}, base.T(slot, 14), pbcTicks))
	require.Empty(t, settleMineTransits([]*_inputCandidate{c}, settled(slot), pbcTicks))
	require.Empty(t, settleMineTransits([]*_inputCandidate{c}, base.T(slot+1, 14), pbcTicks))

	// the global flags finalTouchNonSequencer leaves: validated, yet not Good
	vid.SetFlagsUp(vertex.FlagVertexTxAttachmentFinished | vertex.FlagVertexConstraintsValid)
	require.Equal(t, vertex.Undefined, vid.GetTxStatus())

	// ranked with its VRF output and held until the settlement window
	c = candidateOf(vid, index)
	require.NotNil(t, c.mine)
	require.False(t, c.mine.pending)
	require.NotEmpty(t, c.mine.vrfOutput)
	require.Empty(t, settleMineTransits([]*_inputCandidate{c}, base.T(slot, 14), pbcTicks))
	require.Len(t, settleMineTransits([]*_inputCandidate{c}, settled(slot), pbcTicks), 1)
}

// A pending competitor takes no part in the ranking: it cannot displace the
// validated transit, whatever VRF output its unverified proof claims.
func TestMineTransitPendingCompetitorIgnored(t *testing.T) {
	vid, index := mineTransitFixture(t)
	vid.SetFlagsUp(vertex.FlagVertexTxAttachmentStarted | vertex.FlagVertexTxAttachmentFinished | vertex.FlagVertexConstraintsValid)
	c := candidateOf(vid, index)
	require.False(t, c.mine.pending)

	forged := transitCandidate(c.mine.pred, c.mine.slot, make([]byte, len(c.mine.vrfOutput)), 0)
	forged.mine.pending = true
	ret := settleMineTransits([]*_inputCandidate{forged, c}, settled(c.mine.slot), pbcTicks)
	require.Equal(t, []*_inputCandidate{c}, ret)
}
