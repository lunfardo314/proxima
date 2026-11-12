package task

import (
	"strings"
	"testing"
	"time"

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

// The slot stats carry the v4 counters: factory builds cut by the fallback reserve, and the branch
// timing against the boundary once recorded.
func TestSlotDataV4Fields(t *testing.T) {
	sd := NewSlotData(45164)
	line := sd.Lines().Join(", ")
	require.Contains(t, line, "reserve cuts: 0")
	require.False(t, strings.Contains(line, "branch start"))

	sd.ReserveCut()
	sd.ZoneMilestone("last", true)
	sd.BranchTiming(-80*time.Millisecond, 35*time.Millisecond)
	line = sd.Lines().Join(", ")
	require.Contains(t, line, "reserve cuts: 1")
	require.Contains(t, line, "last issued")
	require.Contains(t, line, "branch start: -80ms, build: 35ms")
}
