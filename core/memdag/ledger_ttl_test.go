package memdag

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A vertex old by ledger time but attached just now stays until it has been in the memDAG more than
// branchPruneDepth slots. After a restart a node attached committed milestones 14-20 slots old and the
// next GC pass, 5 seconds later, detached them under the attachers that needed them: 1,426 at once,
// each reattached at once.
func TestLedgerTTLWaitsForMinimumAge(t *testing.T) {
	const latestBranch, now = 194486, 194488
	old := uint32(194470) // 16 slots behind the latest branch, past the ledger TTL of 12

	require.False(t, ledgerTTLExpired(old, now, now, latestBranch), "attached this slot")
	require.False(t, ledgerTTLExpired(old, now-branchPruneDepth, now, latestBranch), "exactly the minimum age")
	require.True(t, ledgerTTLExpired(old, now-branchPruneDepth-1, now, latestBranch), "past the minimum age")

	// a recent transaction is never past the ledger TTL, whatever its age in the memDAG
	require.False(t, ledgerTTLExpired(latestBranch-1, now-20, now, latestBranch))
	// without a latest branch there is no ledger time to measure against
	require.False(t, ledgerTTLExpired(old, now-20, now, 0))
}
