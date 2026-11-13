package v3

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFirstMoveHeld checks the decision behind holdFirstMove: the slot's first move waits only while
// the own branch is the only pair to build from (the factory has posted no skeleton endorsing another
// sequencer's milestone), and only until the grace past the post-branch consolidation tick ends.
func TestFirstMoveHeld(t *testing.T) {
	const pbc = 9
	tests := []struct {
		name          string
		tick          uint8
		ownBranchOnly   bool
		endorsingPosted bool
		held            bool
	}{
		// the race seen live: own branch attached, no pair on another branch posted yet
		{"own branch alone at the post-branch tick", pbc, true, false, true},
		{"own branch alone, last tick of the grace", pbc + firstMoveGraceTicks - 1, true, false, true},
		// grace over: a sequencer alone or far ahead must still make its first move
		{"own branch alone, grace over", pbc + firstMoveGraceTicks, true, false, false},
		{"own branch alone, well past the grace", 60, true, false, false},
		// a skeleton endorsing another sequencer's milestone is posted: the first move can leave the
		// own branch, no waiting
		{"endorsing skeleton posted", pbc, true, true, false},
		// the own latest milestone is not the own branch of the slot (no own branch, or the first
		// move already made): the gate does not apply
		{"no own branch of the slot", pbc, false, false, false},
		{"no own branch, endorsing skeleton posted", pbc, false, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.held, firstMoveHeld(tt.tick, pbc, tt.ownBranchOnly, tt.endorsingPosted))
		})
	}
}
