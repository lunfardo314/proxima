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

// Placement is an order of attempts: the delegations below the target size,
// smallest first, frozen or not (a frozen one is topped up through its
// target); then a new one (nil) while the count is under the target; then the
// rest, smallest first. The consolidator takes the first that can be made.
func TestPlacementOrder(t *testing.T) {
	// nothing yet: create
	require.Equal(t, []*ownDelegation{nil}, placementOrder(nil, 5, size))

	// one small delegation: it grows before a second is started
	small := dlg(100*prox, true, "")
	require.Equal(t, []*ownDelegation{small, nil}, placementOrder([]*ownDelegation{small}, 5, size))

	// the smallest one below the size comes first, frozen or not
	smaller := dlg(50*prox, true, "")
	frozen := dlg(10*prox, false, "")
	require.Equal(t, []*ownDelegation{frozen, smaller, small, nil}, placementOrder([]*ownDelegation{small, frozen, smaller}, 5, size))

	// all at size and under the count: create first, then the smallest
	full := dlg(size, true, "")
	fuller := dlg(size+1, true, "")
	require.Equal(t, []*ownDelegation{nil, full, fuller}, placementOrder([]*ownDelegation{fuller, full}, 5, size))

	// at the count: no creation, smallest first across the size boundary
	frozenBig := dlg(size+9, false, "")
	require.Equal(t, []*ownDelegation{small, full, frozenBig}, placementOrder([]*ownDelegation{frozenBig, full, small}, 3, size))
}

// A stale delegation too small to stand on its own is folded into the
// largest other consumable delegation; frozen ones and itself never qualify.
func TestLargestConsumableOther(t *testing.T) {
	tiny := dlg(5*prox, true, "sequencer not active")
	big := dlg(500*prox, true, "")
	mid := dlg(200*prox, true, "")
	frozenBig := dlg(900*prox, false, "")
	require.Same(t, big, largestConsumableOther([]*ownDelegation{tiny, mid, frozenBig, big}, tiny))
	require.Nil(t, largestConsumableOther([]*ownDelegation{tiny, frozenBig}, tiny))
	require.Nil(t, largestConsumableOther([]*ownDelegation{tiny}, tiny))
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
