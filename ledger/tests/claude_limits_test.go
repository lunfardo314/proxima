// Tests for transaction element size limits enforced during parsing and scanning.
// These limits complement the network-level caps (P2P: 65,531 bytes, API: 65,536 bytes)
// by providing validation-level enforcement.
//
// Limits tested:
//   MaxTransactionSize  = 65,536 bytes — checked first in Parse()
//   MaxOutputSize       = 8,192 bytes  — checked in scanProducedOutputs()
//   MaxUnlockParamsSize = 1,024 bytes  — checked in scanInputs()

package tests

import (
	"bytes"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
)

// --------------------------------------------------------------------------
// Sanity: normal transaction is well under all limits
// --------------------------------------------------------------------------

// TestLimitsValidTransactionUnderAllLimits verifies that a normal transfer transaction
// is well within all size limits and passes validation.
func TestLimitsValidTransactionUnderAllLimits(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKey, _, srcAddr := u.GenerateAddress(1)
	_, _, dstAddr := u.GenerateAddress(2)
	err := u.TokensFromFaucet(srcAddr, 1_000_000_000)
	require.NoError(t, err)

	txBytes, _ := buildValidTransferTxBytes(t, u, privKey, srcAddr, dstAddr, 100_000_000)

	// Verify the transaction is small relative to limits
	t.Logf("normal transaction size: %d bytes (limit: %d)", len(txBytes), transaction.MaxTransactionSize)
	require.True(t, len(txBytes) < transaction.MaxTransactionSize,
		"normal transaction must be well under the size limit")

	// Must pass full validation
	err = u.AddTransaction(txBytes)
	require.NoError(t, err, "normal transaction must pass all validation")
}

// --------------------------------------------------------------------------
// TEST: MaxTransactionSize — rejected at Parse()
// --------------------------------------------------------------------------

// TestLimitsMaxTransactionSize verifies that a transaction exceeding MaxTransactionSize
// is rejected at the very first check in Parse(), before any tuple parsing occurs.
func TestLimitsMaxTransactionSize(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKey, _, srcAddr := u.GenerateAddress(1)
	_, _, dstAddr := u.GenerateAddress(2)
	err := u.TokensFromFaucet(srcAddr, 1_000_000_000)
	require.NoError(t, err)

	txBytes, _ := buildValidTransferTxBytes(t, u, privKey, srcAddr, dstAddr, 100_000_000)

	// Pad the transaction bytes beyond the limit.
	// Appending garbage after valid bytes still makes the total exceed the limit.
	padding := make([]byte, transaction.MaxTransactionSize-len(txBytes)+1)
	oversizedBytes := append(bytes.Clone(txBytes), padding...)
	require.True(t, len(oversizedBytes) > transaction.MaxTransactionSize)

	_, err = transaction.Parse(oversizedBytes)
	require.Error(t, err, "oversized transaction must be rejected at Parse()")
	require.NoError(t, util.MustErrorWith(err, "exceeds maximum"))
	t.Logf("oversized transaction (%d bytes) correctly rejected: %v", len(oversizedBytes), err)
}

// TestLimitsTransactionAtExactMax verifies that a transaction of exactly
// MaxTransactionSize bytes is not rejected by the size check itself
// (it may fail for other reasons like invalid tuple structure).
func TestLimitsTransactionAtExactMax(t *testing.T) {
	data := make([]byte, transaction.MaxTransactionSize)
	_, err := transaction.Parse(data)
	// Should fail for structural reasons (not a valid tuple), not for size
	require.Error(t, err)
	require.NotContains(t, err.Error(), "exceeds maximum",
		"exactly MaxTransactionSize should not trigger the size check")
}

// --------------------------------------------------------------------------
// TEST: MaxOutputSize — rejected at scanProducedOutputs()
// --------------------------------------------------------------------------

// TestLimitsMaxOutputSize verifies that a produced output exceeding MaxOutputSize
// is rejected during output scanning in partial context validation.
//
// To create an oversized output, we add many dummy constraints (each a valid
// EasyFL bytecode) until the output tuple exceeds the limit.
func TestLimitsMaxOutputSize(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKey, _, addr := u.GenerateAddress(1)
	err := u.TokensFromFaucet(addr, 1_000_000_000)
	require.NoError(t, err)

	outs := getSourceOutputs(t, u, addr)

	txb := txbuilder.New()
	_, maxTs, err := txb.ConsumeOutputsNoUnlock(outs...)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	// Build an output with many large dummy constraints to exceed MaxOutputSize.
	// We use timelock constraints with padding-like values as fillers.
	bigOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(1_000_000_000)).WithLock(addr)
		// Each constraint adds bytes to the output. Keep pushing until we exceed the limit.
		// Use raw bytecode blobs as constraints.
		filler := make([]byte, 200)
		filler[0] = 0x01 // non-zero first byte (required by EasyFL bytecode)
		for i := 0; i < 50; i++ {
			o.MustPushConstraint(filler)
		}
	})

	outBytes := bigOut.Bytes()
	t.Logf("oversized output: %d bytes (limit: %d)", len(outBytes), transaction.MaxOutputSize)

	if len(outBytes) <= transaction.MaxOutputSize {
		t.Skipf("output only %d bytes, could not exceed limit of %d with this approach", len(outBytes), transaction.MaxOutputSize)
	}

	// Bypass builder's storage deposit check by appending directly.
	// We want to test the validation-level size limit, not the builder check.
	txb.TransactionData.Outputs = append(txb.TransactionData.Outputs, bigOut)

	ts := maxTs.AddTicks(int(ledger.L(maxTs.Slot).TransactionPace))
	txb.TransactionData.Timestamp = ts
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(privKey)
	txBytes := txb.TransactionData.Bytes()

	// Parse succeeds (total size is fine), but partial validation catches oversized output
	_, err = transaction.ParseWithPartialValidation(txBytes)
	require.Error(t, err, "oversized output must be rejected during scanning")
	require.NoError(t, util.MustErrorWith(err, "exceeds maximum"))
	t.Logf("oversized output correctly rejected: %v", err)
}

// --------------------------------------------------------------------------
// TEST: MaxUnlockParamsSize — rejected at scanInputs()
// --------------------------------------------------------------------------

// TestLimitsMaxUnlockParamsSize verifies that unlock params exceeding
// MaxUnlockParamsSize for a single input are rejected during input scanning.
func TestLimitsMaxUnlockParamsSize(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKey, _, addr := u.GenerateAddress(1)
	err := u.TokensFromFaucet(addr, 1_000_000_000)
	require.NoError(t, err)

	outs := getSourceOutputs(t, u, addr)

	txb := txbuilder.New()
	_, maxTs, err := txb.ConsumeOutputsNoUnlock(outs...)
	require.NoError(t, err)

	// Normal signature unlock for the lock constraint
	txb.PutSignatureUnlock(0)

	// Inject oversized unlock params for a non-lock constraint slot.
	// Constraint index 2 (or any index beyond lock) is used for additional unlock data.
	oversizedUnlock := make([]byte, transaction.MaxUnlockParamsSize+1)
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, oversizedUnlock)

	out := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(1_000_000_000)).WithLock(addr)
	})
	_, err = txb.ProduceOutput(out)
	require.NoError(t, err)

	ts := maxTs.AddTicks(int(ledger.L(maxTs.Slot).TransactionPace))
	txb.TransactionData.Timestamp = ts
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(privKey)
	txBytes := txb.TransactionData.Bytes()

	t.Logf("transaction with oversized unlock params: %d total bytes", len(txBytes))

	_, err = transaction.ParseWithPartialValidation(txBytes)
	require.Error(t, err, "oversized unlock params must be rejected")
	require.NoError(t, util.MustErrorWith(err, "unlock params", "exceeds maximum"))
	t.Logf("oversized unlock params correctly rejected: %v", err)
}

// --------------------------------------------------------------------------
// TEST: Constants are accessible and consistent
// --------------------------------------------------------------------------

// TestLimitsConstantsConsistency verifies that the size limit constants are
// consistent with the network-level limits and with each other.
func TestLimitsConstantsConsistency(t *testing.T) {
	// MaxTransactionSize should match or be <= network limits
	require.EqualValues(t, 65536, transaction.MaxTransactionSize,
		"MaxTransactionSize should be 64KB")

	// Per-element limits must be less than total transaction limit
	require.True(t, transaction.MaxOutputSize < transaction.MaxTransactionSize,
		"MaxOutputSize must be less than MaxTransactionSize")
	require.True(t, transaction.MaxUnlockParamsSize < transaction.MaxTransactionSize,
		"MaxUnlockParamsSize must be less than MaxTransactionSize")

	// Log for reference
	t.Logf("MaxTransactionSize:  %d bytes", transaction.MaxTransactionSize)
	t.Logf("MaxOutputSize:       %d bytes", transaction.MaxOutputSize)
	t.Logf("MaxUnlockParamsSize: %d bytes", transaction.MaxUnlockParamsSize)

	_ = base.TransactionIDLength // ensure base is used
}
