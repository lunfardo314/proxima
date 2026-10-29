package forward_sync

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// Selecting the retarget branch: the chain is oldest-first, so the newest branch at or
// before the requested slot is the last one that does not overshoot it.
func TestBranchAtOrBefore(t *testing.T) {
	chain := []base.TransactionID{
		base.RandomTransactionID(true, 1, base.LedgerTime{Slot: 10}),
		base.RandomTransactionID(true, 1, base.LedgerTime{Slot: 20}),
		base.RandomTransactionID(true, 1, base.LedgerTime{Slot: 30}),
	}

	got, ok := branchAtOrBefore(chain, 25)
	require.True(t, ok)
	require.Equal(t, uint32(20), got.Slot())

	got, ok = branchAtOrBefore(chain, 30)
	require.True(t, ok)
	require.Equal(t, uint32(30), got.Slot())

	_, ok = branchAtOrBefore(chain, 9)
	require.False(t, ok)
	_, ok = branchAtOrBefore(nil, 30)
	require.False(t, ok)
}
