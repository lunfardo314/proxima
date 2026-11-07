package mine

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
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
	}, nil)
	r.Add(tx.Bytes())
	_, ok := r.Validated(tx.ID())
	require.False(t, ok)
	require.Empty(t, r.Rivals(tx.MustInputAt(0), base.TransactionID{}, all))

	// the parent reaches the tx store: validated, with the proven VRF output
	store = parentBytes
	m, ok := r.Validated(tx.ID())
	require.True(t, ok)
	require.False(t, m.Pending)
	require.NotEmpty(t, m.Work)
	require.NotEmpty(t, m.Targets, "the targets are read off the tag-alongs")

	require.Empty(t, r.Rivals(m.Pred, m.TxID, all), "a transit is not its own rival")
	require.Len(t, r.Rivals(m.Pred, base.TransactionID{}, all), 1, "a rival of any other candidate on its predecessor")
	require.Empty(t, r.Rivals(m.Pred, base.TransactionID{}, none), "not when addressed to an inactive sequencer")

	// the spent output is wrong: validation fails and the transit never counts
	r = NewRegistry(func(txid base.TransactionID) []byte {
		if txid == parentID {
			return tx.Bytes()
		}
		return nil
	}, nil)
	r.Add(tx.Bytes())
	_, ok = r.Validated(tx.ID())
	require.False(t, ok)
	require.Empty(t, r.Rivals(m.Pred, base.TransactionID{}, all))
}

// A predecessor no stored transaction produced, as the genesis mine output, is read from
// the ledger state: without that fallback the first transit of a fresh mine chain would
// never be validated, and no sequencer would settle it.
func TestMineTransitRegistryOutputFromState(t *testing.T) {
	vid, _ := mineTransitFixture(t)
	tx := vid.GetTransaction()
	parentTx, err := transaction.ParseWithPartialValidation(fixtureParentBytes(t))
	require.NoError(t, err)
	pred := tx.MustInputAt(0)

	r := NewRegistry(func(base.TransactionID) []byte { return nil }, func(oid base.OutputID) []byte {
		if oid == pred {
			return parentTx.MustOutputDataAt(pred.Index())
		}
		return nil
	})
	r.Add(tx.Bytes())
	m, ok := r.Validated(tx.ID())
	require.True(t, ok)
	require.False(t, m.Pending)
}
