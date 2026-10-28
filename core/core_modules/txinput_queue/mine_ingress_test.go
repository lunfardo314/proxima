package txinput_queue

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/stretchr/testify/require"
)

// loadMineTransitFixture returns the public take 1 transit of the sequencer's
// settlement tests and the transaction producing the mine output it spends.
func loadMineTransitFixture(t *testing.T) (tx, parent *transaction.Transaction) {
	var fixture struct {
		LibraryJSON string `json:"library_json"`
		TxBytes     string `json:"tx_bytes"`
		ParentBytes string `json:"parent_bytes"`
	}
	raw, err := os.ReadFile("../../../sequencer/task/testdata/mine_transit_5843.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &fixture))
	ledger.MustInitLibraryCacheFromJSON([]byte(fixture.LibraryJSON))
	t.Cleanup(ledger.ResetForTesting)

	for _, p := range []struct {
		hexBytes string
		ret      **transaction.Transaction
	}{{fixture.TxBytes, &tx}, {fixture.ParentBytes, &parent}} {
		b, err := hex.DecodeString(p.hexBytes)
		require.NoError(t, err)
		*p.ret, err = transaction.ParseWithPartialValidation(b)
		require.NoError(t, err)
	}
	require.True(t, tx.IsMiningTransaction())
	return tx, parent
}

// A transit is validated at ingress against the bytes of the output it spends, so
// its proof is checked and the live difficulty required, not only the floor. The
// transaction handed on to attachment keeps no full context from the check.
func TestValidateMineTransitAgainstPredecessor(t *testing.T) {
	tx, parent := loadMineTransitFixture(t)
	pred := tx.MustInputAt(0)
	predBytes := parent.MustOutputDataAt(pred.Index())

	require.NoError(t, validateAgainst(tx, predBytes))
	require.Error(t, tx.ValidateFullContext(), "the checked transaction keeps no full context")

	// the right input ID with other bytes: the transit's own successor output,
	// whose mine lock carries other arguments, does not validate it
	require.Error(t, validateAgainst(tx, tx.MustOutputDataAt(0)))

	// a proof byte changed: it still parses, but no longer validates
	unlock, err := tx.UnlockParameters(0, ledger.ConstraintIndexLock)
	require.NoError(t, err)
	b := tx.Bytes()
	at := bytes.Index(b, unlock)
	require.True(t, at >= 0)
	forged := append([]byte(nil), b...)
	forged[at+1] ^= 0xff
	ftx, err := transaction.Parse(forged)
	require.NoError(t, err)
	require.Error(t, validateAgainst(ftx, predBytes))
}

// A validated mine output is remembered for its successors and forgotten after
// mineSolutionSlots, so the memory stays bounded.
func TestAddMineOutputPrunes(t *testing.T) {
	tx, _ := loadMineTransitFixture(t)
	older, newer := tx.MustInputAt(0), tx.OutputID(0)
	q := &TxInputQueue{mineOutputs: make(map[base.OutputID][]byte)}

	q.addMineOutput(older, []byte{1}, older.Slot())
	q.addMineOutput(newer, []byte{2}, older.Slot()+mineSolutionSlots)
	require.Len(t, q.mineOutputs, 2, "kept within the window")

	q.addMineOutput(newer, []byte{2}, older.Slot()+mineSolutionSlots+1)
	_, kept := q.mineOutputs[older]
	require.False(t, kept, "pruned once a slot past the window is recorded")
	require.Equal(t, []byte{2}, q.mineOutputs[newer])
}
