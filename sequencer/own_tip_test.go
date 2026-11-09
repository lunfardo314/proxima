package sequencer

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A sequencer extends its own latest milestone while it is at most MaxOwnTipAgeSlots old, or when a
// reliable branch includes it. An older tip no branch took is stale: extending it, as a sequencer
// waking from a 14-slot stall did on 2026-11-09, makes every node reattach the cone behind it.
func TestOwnTipStale(t *testing.T) {
	inLRB := func() bool { return true }
	notInLRB := func() bool { return false }

	const now = 1000
	require.False(t, OwnTipStale(now, now, notInLRB))
	require.False(t, OwnTipStale(now-MaxOwnTipAgeSlots, now, notInLRB))
	require.True(t, OwnTipStale(now-MaxOwnTipAgeSlots-1, now, notInLRB))
	require.True(t, OwnTipStale(now-14, now, notInLRB))
	// an old tip a reliable branch includes is committed and may be extended
	require.False(t, OwnTipStale(now-14, now, inLRB))

	// the LRB is consulted only for an old tip
	called := false
	OwnTipStale(now, now, func() bool { called = true; return false })
	require.False(t, called)
}
