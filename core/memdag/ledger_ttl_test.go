package memdag

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A vertex old by ledger time but attached recently stays until it has been in the memDAG more than
// half the wall-clock TTL. After a restart a node rebuilds the delta of every live milestone, which
// can reach a hundred slots back along a sequencer chain nobody included. With a minimum age of three
// slots, the GC detached those vertices under the attachers still resting on them: thousands were
// reattached at once and the attacher cap stayed full for minutes.
func TestLedgerTTLWaitsForMinimumAge(t *testing.T) {
	const latestBranch, now = 194486, 194488
	old := uint32(194470) // 16 slots behind the latest branch, past the ledger TTL of 12
	dwell := vertexTTLSlots / 2

	require.False(t, ledgerTTLExpired(old, now, now, latestBranch), "attached this slot")
	require.False(t, ledgerTTLExpired(old, now-branchPruneDepth-1, now, latestBranch), "past the confirmed-deep age only")
	require.False(t, ledgerTTLExpired(old, now-dwell, now, latestBranch), "exactly the minimum age")
	require.True(t, ledgerTTLExpired(old, now-dwell-1, now, latestBranch), "past the minimum age")

	// a recent transaction is never past the ledger TTL, whatever its age in the memDAG
	require.False(t, ledgerTTLExpired(latestBranch-1, now-20, now, latestBranch))
	// without a latest branch there is no ledger time to measure against
	require.False(t, ledgerTTLExpired(old, now-20, now, 0))
}
