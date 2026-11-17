package vertex

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// milestone wraps a random sequencer transaction ID of the given tick as a vertex with the
// given ledger coverage and inflation amount; tick 0 makes it a branch.
func milestone(slot uint32, tick byte, coverage, inflation uint64) *WrappedTx {
	vid := WrapTxID(base.RandomTransactionID(true, 1, base.T(slot, tick)))
	vid.coverage.Store(&coverage)
	vid.UnwrapVirtualTx(func(v *VirtualTransaction) { v.inflation = inflation })
	return vid
}

// Coverage decides first; other ties fall to the transaction ID, which only keeps the order total.
// Between branches of equal coverage, from library upgrade 7 (folded into the test genesis) the
// larger sigma x u - B decides when both transactions are held; branches held without them, as
// here, fall to the transaction ID, the same rule both ways round, so the order stays antisymmetric
// whatever the bonuses.
func TestPreferredMilestone(t *testing.T) {
	t.Run("coverage first", func(t *testing.T) {
		heavy, light := milestone(10, 0, 200, 1), milestone(10, 0, 100, 1_000_000)
		require.True(t, IsPreferredMilestoneAgainstTheOther(heavy, light))
		require.False(t, IsPreferredMilestoneAgainstTheOther(light, heavy))
	})
	t.Run("tied branches without transactions: transaction ID, antisymmetric", func(t *testing.T) {
		for i := 0; i < 50; i++ {
			a, b := milestone(10, 0, 100, 3_000_000), milestone(10, 0, 100, 2_000_000)
			require.Equal(t, base.LessTxID(b.id, a.id), IsPreferredMilestoneAgainstTheOther(a, b))
			require.NotEqual(t, IsPreferredMilestoneAgainstTheOther(a, b), IsPreferredMilestoneAgainstTheOther(b, a))
		}
	})
	t.Run("tied non-branch milestones: transaction ID, as before", func(t *testing.T) {
		a, b := milestone(10, 5, 100, 3_000_000), milestone(10, 5, 100, 2_000_000)
		require.Equal(t, base.LessTxID(b.id, a.id), IsPreferredMilestoneAgainstTheOther(a, b))
		require.NotEqual(t, IsPreferredMilestoneAgainstTheOther(a, b), IsPreferredMilestoneAgainstTheOther(b, a))
	})
}
