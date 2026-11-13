package v3

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFirstMoveHeld checks the decision behind holdFirstMove: the slot's first move waits only while
// the own branch is the sole milestone of the slot in view, and only until the grace past the
// post-branch consolidation tick ends.
func TestFirstMoveHeld(t *testing.T) {
	const pbc = 9
	tests := []struct {
		name          string
		tick          uint8
		ownBranchOnly bool
		foreignInSlot bool
		held          bool
	}{
		// the race seen live: own branch attached, no other branch yet, at the post-branch tick
		{"own branch alone at the post-branch tick", pbc, true, false, true},
		{"own branch alone, last tick of the grace", pbc + firstMoveGraceTicks - 1, true, false, true},
		// grace over: a sequencer alone or far ahead must still make its first move
		{"own branch alone, grace over", pbc + firstMoveGraceTicks, true, false, false},
		{"own branch alone, well past the grace", 60, true, false, false},
		// another sequencer's milestone of the slot means a foreign branch has attached: no waiting
		{"foreign milestone in the slot", pbc, true, true, false},
		// the own latest milestone is not the own branch of the slot (no own branch, or the first
		// move already made): the gate does not apply
		{"no own branch of the slot", pbc, false, false, false},
		{"no own branch, foreign in slot", pbc, false, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.held, firstMoveHeld(tt.tick, pbc, tt.ownBranchOnly, tt.foreignInSlot))
		})
	}
}
