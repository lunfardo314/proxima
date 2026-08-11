package factory

import (
	"testing"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/stretchr/testify/require"
)

// stubEnv supplies just the own-milestone outputs the extend heuristics read. Only that one
// method is exercised here; the rest of the environment is not reached.
type stubEnv struct {
	environment
	own []vertex.WrappedOutput
}

func (s stubEnv) OwnMilestoneOutputsInMemDAGAscending() []vertex.WrappedOutput {
	ret := make([]vertex.WrappedOutput, len(s.own))
	copy(ret, s.own)
	return ret
}

// The two heuristics of a group must actually search differently. A previous change wired the
// group up but left the heuristic unread — both factories ran the same greedy search, the build
// succeeded, and the liveness tests passed, so a no-op shipped as a feature. These assert on the
// functions themselves, which is the part that silently went missing.
func TestHeuristicsAreDistinct(t *testing.T) {
	t.Run("group holds two differently named heuristics", func(t *testing.T) {
		names := map[string]bool{}
		for _, h := range []heuristic{greedyHeuristic, randomHeuristic} {
			require.NotEmpty(t, h.name)
			require.False(t, names[h.name], "duplicate heuristic name %s", h.name)
			names[h.name] = true
			require.NotNil(t, h.endorseCandidates, "%s: endorseCandidates must be set", h.name)
			require.NotNil(t, h.ownExtendCandidates, "%s: ownExtendCandidates must be set", h.name)
		}
		require.Len(t, names, 2)
	})

	t.Run("greedy orders own outputs newest first", func(t *testing.T) {
		// the ordering is what distinguishes the heuristics, so it is asserted directly rather
		// than through a running factory, where a wrong order merely looks like a slower search
		in := []vertex.WrappedOutput{{Index: 0}, {Index: 1}, {Index: 2}}
		f := &Factory{environment: stubEnv{own: in}, h: greedyHeuristic}
		got := f.h.ownExtendCandidates(f)
		require.Len(t, got, 3)
		require.EqualValues(t, 2, got[0].Index, "newest own output must come first")
		require.EqualValues(t, 0, got[2].Index)
	})

	t.Run("both offer the whole own past cone, not just the head", func(t *testing.T) {
		// reverting own state is inside the search space: a heuristic which offers only the head
		// can never leave the lineage its own chain is on
		in := []vertex.WrappedOutput{{Index: 0}, {Index: 1}, {Index: 2}}
		for _, h := range []heuristic{greedyHeuristic, randomHeuristic} {
			f := &Factory{environment: stubEnv{own: in}, h: h}
			require.Len(t, f.h.ownExtendCandidates(f), len(in), "%s must offer every own output", h.name)
		}
	})
}
