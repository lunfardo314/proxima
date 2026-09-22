package consolidate

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The two deciders of the delegation mode read only a delegation's balance,
// whether the master can spend it now, and whether its target still serves
// it; the rest of ownDelegation is left empty here.

func dlg(balance uint64, consumable bool, stale string) *ownDelegation {
	return &ownDelegation{balance: balance, consumable: consumable, stale: stale}
}

const size = 10_000 * prox

// Placement: grow the smallest delegation while it is below the target size,
// frozen or not (a frozen one is topped up through its target); start a new
// one only when none is below it and the count is under the target; beyond
// the target count, top up the smallest.
func TestPickPlacement(t *testing.T) {
	// nothing yet: create
	d, create := pickPlacement(nil, 5, size)
	require.Nil(t, d)
	require.True(t, create)

	// one small delegation: it grows before a second is started
	small := dlg(100*prox, true, "")
	d, create = pickPlacement([]*ownDelegation{small}, 5, size)
	require.Same(t, small, d)
	require.False(t, create)

	// the smallest one below the size is the one topped up
	smaller := dlg(50*prox, true, "")
	d, _ = pickPlacement([]*ownDelegation{small, smaller}, 5, size)
	require.Same(t, smaller, d)

	// a frozen one is topped up through its target when it is the smallest
	frozen := dlg(10*prox, false, "")
	d, _ = pickPlacement([]*ownDelegation{small, frozen}, 5, size)
	require.Same(t, frozen, d)

	// all at size and under the count: create
	full := dlg(size, true, "")
	d, create = pickPlacement([]*ownDelegation{full, dlg(size+1, true, "")}, 5, size)
	require.Nil(t, d)
	require.True(t, create)

	// at the count with everything at size: the smallest is topped up
	set := []*ownDelegation{dlg(size+5, true, ""), full, dlg(size+9, false, "")}
	d, create = pickPlacement(set, 3, size)
	require.Same(t, full, d)
	require.False(t, create)

	// at the count with everything frozen: the smallest, by request
	frozenA, frozenB := dlg(size, false, ""), dlg(size+1, false, "")
	d, create = pickPlacement([]*ownDelegation{frozenB, frozenA}, 2, size)
	require.Same(t, frozenA, d)
	require.False(t, create)
}

// Tidying: above the target count the smallest consumable delegation is folded
// into the largest consumable one; otherwise a stale consumable one is
// re-delegated; frozen delegations are never touched.
func TestPickManagement(t *testing.T) {
	big := dlg(500*prox, true, "")
	mid := dlg(200*prox, true, "sequencer keeps more")
	tiny := dlg(10*prox, true, "")
	frozenTiny := dlg(1*prox, false, "")
	frozenBig := dlg(900*prox, false, "")

	// over the count: fold tiny into big, whatever the frozen ones hold
	into, kill, re := pickManagement([]*ownDelegation{frozenBig, mid, big, tiny, frozenTiny}, 3)
	require.Same(t, big, into)
	require.Same(t, tiny, kill)
	require.Nil(t, re)

	// over the count but only one consumable: nothing to fold, so the stale one is re-delegated
	into, kill, re = pickManagement([]*ownDelegation{frozenBig, mid, frozenTiny}, 2)
	require.Nil(t, into)
	require.Nil(t, kill)
	require.Same(t, mid, re)

	// at the count: no folding, the stale one is re-delegated
	_, _, re = pickManagement([]*ownDelegation{big, mid, tiny}, 3)
	require.Same(t, mid, re)

	// at the count with nothing stale: nothing
	into, kill, re = pickManagement([]*ownDelegation{big, tiny, frozenBig}, 3)
	require.Nil(t, into)
	require.Nil(t, kill)
	require.Nil(t, re)

	// a stale but frozen delegation waits for its target
	_, _, re = pickManagement([]*ownDelegation{dlg(size, false, "sequencer not active")}, 5)
	require.Nil(t, re)
}
