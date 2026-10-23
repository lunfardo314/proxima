package branches

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Every slot leaves all but one of its competing branches orphaned, so the cleanup logs them
// as one line grouped by slot, in ascending slot order and in the order they were found.
func TestOrphanedBySlot(t *testing.T) {
	got := orphanedBySlot([]orphanedBranch{
		{slot: 45164, name: "JANUS"},
		{slot: 45163, name: "SATURN"},
		{slot: 45164, name: "srv1"},
		{slot: 45163, name: "RAMEN"},
	})
	require.Equal(t, "s45163 (SATURN, RAMEN); s45164 (JANUS, srv1)", got)
	require.Empty(t, orphanedBySlot(nil))
}
