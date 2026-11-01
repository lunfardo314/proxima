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
