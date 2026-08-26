package delegationpool

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// mergeDiscovered is the seam of the periodic LRB rescan. It must enroll
// delegations the pool has never seen (the missed-listener case), realign
// entries whose delegation was transitioned by its master behind the
// sequencer's back, and drop entries whose delegation left this target —
// all without disturbing an entry that carries a pending transition, since
// the LRB legitimately lags a freeze until the next branch.
func TestMergeDiscovered(t *testing.T) {
	newPool := func(entries map[base.ChainID]*delegationEntry) *DelegationPool {
		if entries == nil {
			entries = make(map[base.ChainID]*delegationEntry)
		}
		return &DelegationPool{entries: entries}
	}
	// outputID at a chosen slot: the drop rule compares it against the scanned
	// branch's slot, and a refresh is detected by the outputID changing.
	oid := func(slot uint32) base.OutputID {
		return base.RandomOutputID(base.LedgerTime{Slot: slot, Tick: 1})
	}
	const lrbSlot = 1000

	cidA, cidB := base.RandomChainID(), base.RandomChainID()

	t.Run("empty pool: plain fill, the startup case", func(t *testing.T) {
		p := newPool(nil)
		added, refreshed, dropped := p.mergeDiscovered(map[base.ChainID]*delegationEntry{
			cidA: {outputID: oid(900), amount: 100, confirmed: true},
			cidB: {outputID: oid(900), amount: 200, confirmed: true},
		}, lrbSlot)
		require.Equal(t, [3]int{2, 0, 0}, [3]int{added, refreshed, dropped})
		require.Len(t, p.entries, 2)
		require.EqualValues(t, 100, p.entries[cidA].amount)
	})

	t.Run("unknown delegation is enrolled", func(t *testing.T) {
		known := oid(900)
		p := newPool(map[base.ChainID]*delegationEntry{cidA: {outputID: known, amount: 100, confirmed: true}})
		added, refreshed, dropped := p.mergeDiscovered(map[base.ChainID]*delegationEntry{
			cidA: {outputID: known, amount: 100, confirmed: true},
			cidB: {outputID: oid(900), amount: 200, confirmed: true},
		}, lrbSlot)
		require.Equal(t, [3]int{1, 0, 0}, [3]int{added, refreshed, dropped}, "cidA is already the LRB's truth")
		require.Len(t, p.entries, 2)
		require.EqualValues(t, 200, p.entries[cidB].amount)
	})

	t.Run("a settled entry the master moved on from is refreshed", func(t *testing.T) {
		// the master topped up / re-targeted the delegation with a non-sequencer
		// transaction: the pool's outputID now points at an output that is gone,
		// and every freeze attempt would fail the objective read in the proposer
		stale := &delegationEntry{outputID: oid(500), amount: 100, confirmed: true, addedSlot: 42}
		p := newPool(map[base.ChainID]*delegationEntry{cidA: stale})
		current := oid(990)
		added, refreshed, dropped := p.mergeDiscovered(map[base.ChainID]*delegationEntry{
			cidA: {outputID: current, amount: 150, confirmed: true, addedSlot: 990},
		}, lrbSlot)
		require.Equal(t, [3]int{0, 1, 0}, [3]int{added, refreshed, dropped})
		require.Equal(t, current, p.entries[cidA].outputID)
		require.EqualValues(t, 150, p.entries[cidA].amount, "the top-up must be reflected in the freeze weight")
		require.EqualValues(t, 42, p.entries[cidA].addedSlot, "addedSlot drives the TTL; must not be reset")
	})

	t.Run("a pending transition is never overwritten", func(t *testing.T) {
		pending := &delegationEntry{
			outputID: oid(500),
			amount:   100,
			state:    ledger.DelegateLockStateUndef,
			pending:  &pendingTransition{kind: transitionFreeze, untilEpoch: 7, amount: 100},
		}
		p := newPool(map[base.ChainID]*delegationEntry{cidA: pending})
		// the LRB still shows the pre-freeze state; adopting it would drop the
		// pending freeze and let the sequencer re-freeze the same delegation
		added, refreshed, dropped := p.mergeDiscovered(map[base.ChainID]*delegationEntry{
			cidA: {outputID: oid(990), amount: 100, state: ledger.DelegateLockStateUndef, confirmed: true},
		}, lrbSlot)
		require.Equal(t, [3]int{0, 0, 0}, [3]int{added, refreshed, dropped})
		require.Same(t, pending, p.entries[cidA])
		require.NotNil(t, p.entries[cidA].pending)
		require.EqualValues(t, 7, p.entries[cidA].pending.untilEpoch)
	})

	t.Run("a tentative entry on the same output keeps its unconfirmed flag", func(t *testing.T) {
		same := oid(900)
		tentative := &delegationEntry{outputID: same, amount: 100, confirmed: false, addedSlot: 42}
		p := newPool(map[base.ChainID]*delegationEntry{cidA: tentative})
		added, refreshed, dropped := p.mergeDiscovered(map[base.ChainID]*delegationEntry{
			cidA: {outputID: same, amount: 100, confirmed: true, addedSlot: 1},
		}, lrbSlot)
		require.Equal(t, [3]int{0, 0, 0}, [3]int{added, refreshed, dropped})
		require.Same(t, tentative, p.entries[cidA])
		require.False(t, p.entries[cidA].confirmed, "Reconcile owns confirmation, not discovery")
		require.EqualValues(t, 42, p.entries[cidA].addedSlot)
	})

	t.Run("a delegation that left this target is dropped", func(t *testing.T) {
		// re-targeted away or revoked: it is absent from the scan, so it must stop
		// weighting the freeze-epoch load vector
		p := newPool(map[base.ChainID]*delegationEntry{
			cidA: {outputID: oid(500), amount: 100, confirmed: true, state: ledger.DelegateLockStateFrozen},
		})
		added, refreshed, dropped := p.mergeDiscovered(nil, lrbSlot)
		require.Equal(t, [3]int{0, 0, 1}, [3]int{added, refreshed, dropped})
		require.Empty(t, p.entries)
	})

	t.Run("an entry newer than the scanned branch is not dropped", func(t *testing.T) {
		// the scan read an older LRB than the one this entry was settled from;
		// dropping it here would undo a newer, correct enrolment
		newer := &delegationEntry{outputID: oid(lrbSlot + 5), amount: 100, confirmed: true}
		tentative := &delegationEntry{outputID: oid(500), amount: 100, confirmed: false}
		p := newPool(map[base.ChainID]*delegationEntry{cidA: newer, cidB: tentative})
		added, refreshed, dropped := p.mergeDiscovered(nil, lrbSlot)
		require.Equal(t, [3]int{0, 0, 0}, [3]int{added, refreshed, dropped})
		require.Len(t, p.entries, 2, "unconfirmed entries are Reconcile's to age out")
	})
}
