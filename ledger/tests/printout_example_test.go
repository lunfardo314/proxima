package tests

import (
	"fmt"
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util/testutil/txbtest"
	"github.com/stretchr/testify/require"
)

// TestPrintoutExample builds a foundry "mint" transit transaction (consumes the
// foundry chain output, mints native tokens) and prints the full
// transaction-context human-readable form. It mirrors the `proxi node foundry
// mint` flow. Used only to regenerate the example printout image in the docs
// site (txdocs/tx.md); it asserts nothing about the printed content.
func TestPrintoutExample(t *testing.T) {
	const mintAmount = uint64(1_000_000)

	// Fresh in-memory ledger; create and submit a foundry origin (a chained
	// account that can mint a native token).
	e := newFoundryTestEnv(t, 10_000_000_000)
	chainID := e.createFoundryOrigin(t, 200_000_000, nil)

	// Build the mint transit: foundry as input 0, signature unlock, extra
	// base-token funding appended, a minted tokenAmount(chainID, mintAmount)
	// output to the wallet, and a base-token remainder.
	in := e.foundryInputData(t, chainID)
	fIn, err := ledger.FoundryFromBytes(mustConstraintAt(t, parseOutput(t, in), ledger.ConstraintIndexFoundry))
	require.NoError(t, err)

	txb := exhelp.New()
	_, err = txb.TransitFoundry(in, fIn.Supply+mintAmount)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	ts := in.ID.Timestamp().AddSlots(1)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}
	ts = base.MaximumTime(ts, e.appendExtraFunding(t, txb, 0))

	tokOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(100_000_000).WithLock(e.addr).WithTokenAmount(chainID, mintAmount)
	})
	require.NoError(t, tokOut.EnoughAmountForStorageDeposit())
	_, err = txb.ProduceOutput(tokOut)
	require.NoError(t, err)

	addRemainderIfNeeded(t, txb, e.addr)

	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(e.privKey)

	txBytes, _, failedTx, err := txbtest.BuildAndValidate(txb)
	require.NoError(t, err, "mint build/validation failed: %s", failedTx)

	// Parse + full-context validate BEFORE settling (consumed UTXOs still in
	// state). Validation populates the consumed/produced amount totals so the
	// printout shows real numbers.
	tx0, err := transaction.Parse(txBytes)
	require.NoError(t, err)
	txv, err := transaction.ParseAndValidate(txBytes, tx0.InputLoaderByIndex(e.u.StateReader().GetUTXO))
	require.NoError(t, err)
	fmt.Println("=====BEGIN PRINTOUT=====")
	fmt.Println(txv.String())
	fmt.Println("=====END PRINTOUT=====")
}
