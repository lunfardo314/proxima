package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/stretchr/testify/require"
)

// TestRepeatedFullContextValidation checks that validating the same Transaction
// object twice gives the same verdict. A node re-attaches a transaction evicted
// from the memDAG from the txstore writer cache, which still holds the object of
// the first attachment, so ValidateFullContext runs again on it. Before the
// per-validation state was reset at entry, the consumed total accumulated across
// runs and the second run failed the conservation check on a valid transaction
// (hboot outage of 2026-09-15).
func TestRepeatedFullContextValidation(t *testing.T) {
	const initAmount = 1_000_000_000
	u, privKey, srcAddr := newTestEnv(t, initAmount)
	_, _, dstAddr := u.GenerateAddress(2)

	txBytes, txb := buildValidTransferTxBytes(t, u, privKey, srcAddr, dstAddr, 100_000_000)

	tx, err := transaction.Parse(txBytes)
	require.NoError(t, err)
	require.NoError(t, tx.SetFullContext(txb.LoadInputBytes))

	for i := 0; i < 3; i++ {
		require.NoError(t, tx.ValidateFullContext(), "run %d", i)
		require.EqualValues(t, initAmount, tx.TotalAmount())
	}
}
