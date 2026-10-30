package backlog

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// One reason refusing a stream of outputs is logged in full for the first rejectionsInFull of
// them; after that the repeats are summed up, the first summary at once and then at most once per
// rejectionSummaryPeriod, each counting the repeats since the previous one.
func TestRejectionRecord(t *testing.T) {
	var r rejectionRecord
	t0 := time.Unix(1_800_000_000, 0)

	for i := 0; i < rejectionsInFull; i++ {
		inFull, n, _ := r.note("o", t0)
		require.True(t, inFull, "rejection %d is logged in full", i+1)
		require.Zero(t, n)
	}

	inFull, n, latest := r.note("o4", t0)
	require.False(t, inFull)
	require.Equal(t, 1, n, "the first repeat after the full ones is summed up at once")
	require.Equal(t, "o4", latest)

	// within the period the repeats accumulate silently
	for i := 0; i < 10; i++ {
		inFull, n, _ = r.note("o", t0.Add(time.Second))
		require.False(t, inFull)
		require.Zero(t, n)
	}
	// the next repeat after the period sums them all up, itself included
	inFull, n, latest = r.note("o15", t0.Add(rejectionSummaryPeriod))
	require.False(t, inFull)
	require.Equal(t, 11, n)
	require.Equal(t, "o15", latest)
	require.Zero(t, r.pending)
}
