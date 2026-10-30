package mine

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// The registry holds transits the backlog never sees and validates them with
// the ledger's own full-context check, fetching the spent output from the
// transits it holds or the tx store. Only a validated transit addressed to an
// active sequencer is a rival; one whose spent output is not available yet is
// tried again later, one that fails validation never counts.
func TestMineTransitRegistry(t *testing.T) {
	vid, _ := mineTransitFixture(t)
	tx := vid.GetTransaction()
	parentBytes := fixtureParentBytes(t)
	pred := tx.MustInputAt(0)
	parentID := pred.TransactionID()
	all := func(base.ChainID) bool { return true }
	none := func(base.ChainID) bool { return false }

	// the spent output is nowhere to be found: not validated, not a rival, retried later
	var store []byte
	r := NewRegistry(func(txid base.TransactionID) []byte {
		if txid == parentID {
			return store
		}
		return nil
	})
	r.Add(tx.Bytes())
	_, ok := r.Validated(tx.ID())
	require.False(t, ok)
	require.Empty(t, r.Rivals(tx.MustInputAt(0), base.TransactionID{}, all))

	// the parent reaches the tx store: validated, with the proven VRF output
	store = parentBytes
	m, ok := r.Validated(tx.ID())
	require.True(t, ok)
	require.False(t, m.Pending)
	require.NotEmpty(t, m.VRFOutput)
	require.False(t, m.Target == base.ChainID{}, "the target is read off the tag-along")

	require.Empty(t, r.Rivals(m.Pred, m.TxID, all), "a transit is not its own rival")
	require.Len(t, r.Rivals(m.Pred, base.TransactionID{}, all), 1, "a rival of any other candidate on its predecessor")
	require.Empty(t, r.Rivals(m.Pred, base.TransactionID{}, none), "not when addressed to an inactive sequencer")

	// the spent output is wrong: validation fails and the transit never counts
	r = NewRegistry(func(txid base.TransactionID) []byte {
		if txid == parentID {
			return tx.Bytes()
		}
		return nil
	})
	r.Add(tx.Bytes())
	_, ok = r.Validated(tx.ID())
	require.False(t, ok)
	require.Empty(t, r.Rivals(m.Pred, base.TransactionID{}, all))
}
