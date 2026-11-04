package seq_cmd

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The grid distance wraps around the epoch: offsets 10 and 590 of a 600-slot
// epoch are 20 slots apart, not 580.
func TestGridDistance(t *testing.T) {
	require.EqualValues(t, 20, gridDistance(10, 590, 600))
	require.EqualValues(t, 20, gridDistance(590, 10, 600))
	require.EqualValues(t, 300, gridDistance(0, 300, 600))
	require.EqualValues(t, 0, gridDistance(42, 42, 600))
}

// gridCloseness weighs each existing grid by its capital and by how far inside half
// an epoch it lies, so the grind prefers the offset far from heavy sequencers, even
// at the cost of sitting closer to light ones, and an offset half an epoch from
// every grid scores 0.
func TestGridCloseness(t *testing.T) {
	others := []gridWeight{{offset: 0, weight: 100}, {offset: 200, weight: 1}}
	require.EqualValues(t, 0, gridCloseness(300, []gridWeight{{offset: 0, weight: 100}}, 600))
	// 200 slots from the heavy grid at 0, on the light one at 200
	nearLight := gridCloseness(200, others, 600)
	require.EqualValues(t, 100*(300-200)+1*300, nearLight)
	// 50 slots from the heavy grid, 250 from the light one: worse
	nearHeavy := gridCloseness(50, others, 600)
	require.EqualValues(t, 100*(300-50)+1*(300-150), nearHeavy)
	require.Less(t, nearLight, nearHeavy)
	require.EqualValues(t, 200, nearestGrid(400, others, 600))
}
