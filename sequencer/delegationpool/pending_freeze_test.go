package delegationpool

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// A freeze carried by an own milestone stays a candidate, marked with where it
// sits in the load vector, so the next milestone on the winning lineage can
// freeze the delegation again when the carrying milestone loses. Before, a
// pending freeze was withheld until Reconcile aged it out, and a sequencer
// whose first move of a slot sits on its own branch froze a fresh delegation
// only when that branch won. An unfreeze stays withheld, and so does a bare
// entry, which has no output to freeze.
func TestSnapshotOffersPendingFreeze(t *testing.T) {
	oid := func(slot uint32) base.OutputID { return base.RandomOutputID(base.LedgerTime{Slot: slot, Tick: 1}) }
	pendingFreeze := base.RandomChainID()
	plain := base.RandomChainID()
	pendingUnfreeze := base.RandomChainID()
	bare := base.RandomChainID()
	p := &DelegationPool{entries: map[base.ChainID]*delegationEntry{
		pendingFreeze: {outputID: oid(100), amount: 500, state: ledger.DelegateLockStateUndef, freezableFromSlot: 101, confirmed: true,
			pending: &pendingTransition{kind: transitionFreeze, slot: 110, untilEpoch: 7, amount: 500, successorID: oid(110)}},
		plain: {outputID: oid(100), amount: 300, state: ledger.DelegateLockStateUndef, freezableFromSlot: 101, confirmed: true},
		pendingUnfreeze: {outputID: oid(100), amount: 200, state: ledger.DelegateLockStateFrozen, lastFrozenEpoch: 3, freezableFromSlot: 101, confirmed: true,
			pending: &pendingTransition{kind: transitionUnfreeze, slot: 110, successorID: oid(110)}},
		bare: {addedSlot: 110, pending: &pendingTransition{kind: transitionFreeze, slot: 110, untilEpoch: 7, amount: 50, successorID: oid(110)}},
	}}

	candidates, load, count := p.Snapshot(120, 1000)
	byID := make(map[base.ChainID]Candidate)
	for _, c := range candidates {
		byID[c.ChainID] = c
	}
	require.Len(t, byID, 2)
	require.Contains(t, byID, plain)
	require.Nil(t, byID[plain].Pending)
	require.Equal(t, &PendingFreeze{UntilEpoch: 7, Amount: 500}, byID[pendingFreeze].Pending)

	// the pending freezes still weigh in the load vector at their epoch: the
	// proposer takes one back only on a lineage that does not hold it
	require.EqualValues(t, 550, load[7])
	require.EqualValues(t, 2, count[7])
}
