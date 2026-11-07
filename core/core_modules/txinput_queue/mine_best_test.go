package txinput_queue

import (
	"encoding/binary"
	"testing"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// TestImprovesBestMineTransit covers the gate that lets a transaction-hash transit go
// further only if it is strictly better, by MineSolutionKey, than the best one already
// seen for the same predecessor: a worse or equal one is dropped, a better one replaces
// the best, and predecessors are kept apart and forgotten after mineSolutionSlots.
func TestImprovesBestMineTransit(t *testing.T) {
	q := &TxInputQueue{mineBest: make(map[base.OutputID][]byte)}
	key := func(slot uint32, work uint64) []byte {
		ret := binary.BigEndian.AppendUint32(nil, slot)
		return binary.BigEndian.AppendUint64(ret, work)
	}
	predA := base.MustNewOutputID(base.NewTransactionID(base.T(100, 0), base.TransactionIDShort{1}, false), 0)
	predB := base.MustNewOutputID(base.NewTransactionID(base.T(100, 0), base.TransactionIDShort{2}, false), 0)

	require.True(t, q.improvesBestMineTransit(predA, key(101, 500), 101), "the first transit of a predecessor")
	require.False(t, q.improvesBestMineTransit(predA, key(101, 500), 101), "an equal one is not an improvement")
	require.False(t, q.improvesBestMineTransit(predA, key(101, 900), 101), "a larger work value is worse")
	require.True(t, q.improvesBestMineTransit(predA, key(101, 100), 101), "a smaller work value is better")
	require.False(t, q.improvesBestMineTransit(predA, key(102, 1), 102), "a later slot is worse whatever its work")
	require.True(t, q.improvesBestMineTransit(predB, key(101, 900), 101), "another predecessor has its own best")

	// far later, the old predecessors are forgotten
	later := uint32(101 + mineSolutionSlots + 10)
	predC := base.MustNewOutputID(base.NewTransactionID(base.T(later-1, 0), base.TransactionIDShort{3}, false), 0)
	require.True(t, q.improvesBestMineTransit(predC, key(later, 1), later))
	require.NotContains(t, q.mineBest, predA)
	require.NotContains(t, q.mineBest, predB)
}
