package txinput_queue

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/stretchr/testify/require"
)

// A miner may send one solution as several transits, one per sequencer. The intake
// admits the first transit of a solution (signer, predecessor, VRF output) and drops
// later copies; a solution is forgotten after mineSolutionSlots, so the memory stays
// bounded. The transit is the public take 1 fixture of the sequencer's settlement tests.
func TestFirstCopyOfMineSolution(t *testing.T) {
	var fixture struct {
		LibraryJSON string `json:"library_json"`
		TxBytes     string `json:"tx_bytes"`
	}
	raw, err := os.ReadFile("../../../sequencer/task/testdata/mine_transit_5843.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &fixture))
	ledger.MustInitLibraryCacheFromJSON([]byte(fixture.LibraryJSON))
	t.Cleanup(ledger.ResetForTesting)

	txBytes, err := hex.DecodeString(fixture.TxBytes)
	require.NoError(t, err)
	tx, err := transaction.ParseWithPartialValidation(txBytes)
	require.NoError(t, err)
	require.True(t, tx.IsMiningTransaction())

	q := &TxInputQueue{mineSolutions: make(map[string]uint32)}
	require.True(t, q.firstCopyOfMineSolution(tx), "the first transit of a solution is admitted")
	require.False(t, q.firstCopyOfMineSolution(tx), "a later transit of the same solution is a copy")
	require.Len(t, q.mineSolutions, 1)

	// a solution older than the window is pruned when a newer slot arrives
	q.mineSolutions["older"] = tx.Slot() - mineSolutionSlots - 1
	q.mineSolutionsSlot = tx.Slot() - 1
	require.False(t, q.firstCopyOfMineSolution(tx))
	q.mineSolutionsSlot = 0
	q.mineSolutions = map[string]uint32{"older": tx.Slot() - mineSolutionSlots - 1}
	require.True(t, q.firstCopyOfMineSolution(tx))
	_, kept := q.mineSolutions["older"]
	require.False(t, kept, "pruned once a newer slot is recorded")
}
