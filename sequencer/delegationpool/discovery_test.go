package delegationpool

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// mergeDiscovered is the seam of the periodic LRB rescan. It must enroll
// delegations the pool has never seen (the missed-listener case) without
// disturbing any entry already there — a known entry can carry a pending
// transition or a listener-added tentative state that the LRB does not
// reflect yet.
func TestMergeDiscovered(t *testing.T) {
	newPool := func(entries map[base.ChainID]*delegationEntry) *DelegationPool {
		if entries == nil {
			entries = make(map[base.ChainID]*delegationEntry)
		}
		return &DelegationPool{entries: entries}
	}

	cidA, cidB := base.RandomChainID(), base.RandomChainID()

	t.Run("empty pool: plain fill, the startup case", func(t *testing.T) {
		p := newPool(nil)
		n := p.mergeDiscovered(map[base.ChainID]*delegationEntry{
			cidA: {amount: 100, confirmed: true},
			cidB: {amount: 200, confirmed: true},
		})
		require.Equal(t, 2, n)
		require.Len(t, p.entries, 2)
		require.EqualValues(t, 100, p.entries[cidA].amount)
	})

	t.Run("unknown delegation is enrolled", func(t *testing.T) {
		p := newPool(map[base.ChainID]*delegationEntry{cidA: {amount: 100}})
		n := p.mergeDiscovered(map[base.ChainID]*delegationEntry{
			cidA: {amount: 999},
			cidB: {amount: 200, confirmed: true},
		})
		require.Equal(t, 1, n, "only the unknown one counts")
		require.Len(t, p.entries, 2)
		require.EqualValues(t, 200, p.entries[cidB].amount)
	})

	t.Run("a pending transition is never overwritten", func(t *testing.T) {
		pending := &delegationEntry{
			amount:  100,
			state:   ledger.DelegateLockStateUndef,
			pending: &pendingTransition{kind: transitionFreeze, untilEpoch: 7, amount: 100},
		}
		p := newPool(map[base.ChainID]*delegationEntry{cidA: pending})
		// the LRB still shows the pre-freeze state; adopting it would drop the
		// pending freeze and let the sequencer re-freeze the same delegation
		n := p.mergeDiscovered(map[base.ChainID]*delegationEntry{
			cidA: {amount: 100, state: ledger.DelegateLockStateUndef, confirmed: true},
		})
		require.Equal(t, 0, n)
		require.Same(t, pending, p.entries[cidA])
		require.NotNil(t, p.entries[cidA].pending)
		require.EqualValues(t, 7, p.entries[cidA].pending.untilEpoch)
	})

	t.Run("a tentative unconfirmed entry survives", func(t *testing.T) {
		tentative := &delegationEntry{amount: 100, confirmed: false, addedSlot: 42}
		p := newPool(map[base.ChainID]*delegationEntry{cidA: tentative})
		n := p.mergeDiscovered(map[base.ChainID]*delegationEntry{
			cidA: {amount: 100, confirmed: true, addedSlot: 1},
		})
		require.Equal(t, 0, n)
		require.Same(t, tentative, p.entries[cidA])
		require.False(t, p.entries[cidA].confirmed, "Reconcile owns confirmation, not discovery")
		require.EqualValues(t, 42, p.entries[cidA].addedSlot, "addedSlot drives the TTL; must not be reset")
	})

	t.Run("nothing found leaves the pool untouched", func(t *testing.T) {
		p := newPool(map[base.ChainID]*delegationEntry{cidA: {amount: 100}})
		require.Equal(t, 0, p.mergeDiscovered(nil))
		require.Len(t, p.entries, 1)
	})
}
