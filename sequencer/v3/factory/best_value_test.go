package factory

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBestValueBoundToSlot replays the race behind silent slots: a round of the previous slot, still
// finishing when the target slot changes, posts a skeleton from the end of that slot. It must not become
// the new slot's threshold, which every early skeleton of the new slot would stay below, nor mark the new
// slot as having an endorsing skeleton, which releases the first-move hold.
func TestBestValueBoundToSlot(t *testing.T) {
	f := &Factory{targetSlot: 10}

	// within the slot: the threshold only rises
	require.True(t, f.raiseBestValue(10, 100))
	require.False(t, f.raiseBestValue(10, 50))
	require.True(t, f.raiseBestValue(10, 100), "an equal value is still posted")
	require.EqualValues(t, 100, f.BestValue())

	// the slot changes, as SetTargetSlot does it
	f.slotMutex.Lock()
	f.targetSlot, f.bestValue, f.endorsingPosted = 11, 0, false
	f.slotMutex.Unlock()

	// the late round of slot 10 is refused and leaves the new slot untouched
	require.False(t, f.raiseBestValue(10, 147))
	f.posted(10, true)
	require.EqualValues(t, 0, f.BestValue())
	require.False(t, f.EndorsingPosted())

	// the new slot's own skeletons are posted from zero
	require.True(t, f.raiseBestValue(11, 85))
	f.posted(11, false)
	require.False(t, f.EndorsingPosted(), "a skeleton without endorsements does not release the hold")
	f.posted(11, true)
	require.True(t, f.EndorsingPosted())

	require.Equal(t, "belowBest=1 posted=3 stalePost=1", f.counters.String())
}
