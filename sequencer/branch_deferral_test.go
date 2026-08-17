package sequencer

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/stretchr/testify/require"
)

// betterBranch decides whether a deficient branch is dropped in favour of one already seen at the
// same slot. The comparison must be STRICT: a sibling which folded in the same number of
// sequencers is not an improvement, and dropping our branch for it would give away the slot (and
// the branch inflation bonus) for nothing.
func TestBetterBranch(t *testing.T) {
	branches := func(numSeq ...uint32) []*multistate.BranchData {
		ret := make([]*multistate.BranchData, len(numSeq))
		for i, n := range numSeq {
			ret[i] = &multistate.BranchData{NumSeq: n}
		}
		return ret
	}

	t.Run("no branches known", func(t *testing.T) {
		require.Nil(t, betterBranch(nil, 3))
	})
	t.Run("all weaker or equal", func(t *testing.T) {
		require.Nil(t, betterBranch(branches(1, 2, 3), 3))
	})
	t.Run("one stronger", func(t *testing.T) {
		found := betterBranch(branches(2, 3, 5), 3)
		require.NotNil(t, found)
		require.EqualValues(t, 5, found.NumSeq)
	})
	// A branch with the maximum possible count never defers, so it never reaches this comparison;
	// checked here anyway because the deficiency test and this one must not disagree.
	t.Run("ours is the strongest", func(t *testing.T) {
		require.Nil(t, betterBranch(branches(5, 4, 1), 5))
	})
}
