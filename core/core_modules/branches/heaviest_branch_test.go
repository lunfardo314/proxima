package branches

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// The heaviest branch is chosen by absolute coverage measured at one common slot (D8), not by slot.
// At the 2026-11-09 rollback the healthy lineage's branch of slot 187949 (about 142T) and a branch
// of slot 187951 on a baseline 393 slots old (absolute coverage 0) were both healthy: the older
// branch must win.
func TestHeavierBranchAcrossSlots(t *testing.T) {
	const T = uint64(1_000_000_000_000)
	healthy := base.RandomTransactionID(true, 1, base.LedgerTime{Slot: 187949})
	rollback := base.RandomTransactionID(true, 1, base.LedgerTime{Slot: 187951})

	require.True(t, heavierBranch(healthy, 142*T, rollback, 0, 187951))
	require.False(t, heavierBranch(rollback, 0, healthy, 142*T, 187951))

	// a normal newer branch outweighs its halved predecessor
	next := base.RandomTransactionID(true, 1, base.LedgerTime{Slot: 187950})
	require.True(t, heavierBranch(next, 71*T+71*T, healthy, 142*T, 187950))
}

// Equal coverage at the common slot goes to the newer slot, then to the larger ID, so every node
// picks the same branch whatever order it meets them in.
func TestHeavierBranchTies(t *testing.T) {
	older := base.RandomTransactionID(true, 1, base.LedgerTime{Slot: 10})
	newer := base.RandomTransactionID(true, 1, base.LedgerTime{Slot: 11})
	// 200 at slot 10 measured at slot 11 is 100
	require.True(t, heavierBranch(newer, 100, older, 200, 11))
	require.False(t, heavierBranch(older, 200, newer, 100, 11))

	a := base.RandomTransactionID(true, 1, base.LedgerTime{Slot: 11})
	b := base.RandomTransactionID(true, 1, base.LedgerTime{Slot: 11})
	require.NotEqual(t, heavierBranch(a, 100, b, 100, 11), heavierBranch(b, 100, a, 100, 11))
}
