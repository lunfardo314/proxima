package task

import (
	"strings"
	"testing"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// The slot stats name the branch submitted in the slot, and say NONE only when there was none.
func TestSlotDataBranch(t *testing.T) {
	sd := NewSlotData(45164)
	require.Contains(t, sd.Lines().Join(", "), "branch: NONE")

	txid := base.NewTransactionID(base.T(45165, 0), base.TransactionIDShort{1, 2, 3}, true)
	sd.BranchTxSubmitted(txid)
	line := sd.Lines().Join(", ")
	require.False(t, strings.Contains(line, "branch: NONE"))
	require.Contains(t, line, "branch: "+txid.StringShort())
}

// The eligibility bar of a milestone is the rank value of the own milestone built last in the slot:
// it is known only for that milestone, and a rank of 0 (the node could not score it) is no bar, so
// the task falls back to the classical coverage.
func TestSlotDataOwnRank(t *testing.T) {
	sd := NewSlotData(45164)
	tx1 := base.NewTransactionID(base.T(45164, 20), base.TransactionIDShort{1}, false)
	tx2 := base.NewTransactionID(base.T(45164, 27), base.TransactionIDShort{2}, false)

	_, ok := sd.OwnRank(tx1)
	require.False(t, ok)

	sd.SetOwnRank(tx1, 100)
	rank, ok := sd.OwnRank(tx1)
	require.True(t, ok)
	require.EqualValues(t, 100, rank)

	sd.SetOwnRank(tx2, 120)
	_, ok = sd.OwnRank(tx1)
	require.False(t, ok)
	rank, ok = sd.OwnRank(tx2)
	require.True(t, ok)
	require.EqualValues(t, 120, rank)

	sd.SetOwnRank(tx2, 0)
	_, ok = sd.OwnRank(tx2)
	require.False(t, ok)
}
