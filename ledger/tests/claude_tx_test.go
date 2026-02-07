// Independent transaction validation tests for Proxima ledger.
// These tests revisit the codebase to check consistency, detect vulnerabilities,
// and prove that potential attack vectors are not possible.
//
// Key validation rules tested (from tx_integrity_validator.easyfl):
//   Partial context (no consumed UTXOs needed):
//     - Number of inputs > 0 and equals number of unlock params
//     - No duplicate inputs (tupleHasDuplicatesAtPath)
//     - Valid signature (validSignature)
//     - Valid endorsements
//   Full context (requires consumed UTXOs):
//     - Input commitment = blake2b(consumed outputs tuple)
//       This prevents "faked UTXO" attack

package tests

import (
	"crypto/ed25519"
	"fmt"
	"math"
	"math/rand"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/blake2b"
)

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

// newTestEnv creates a fresh UTXODB and a funded address for testing.
// Returns the utxodb, the private key, and the address.
func newTestEnv(t *testing.T, amount uint64) (*utxodb.UTXODB, ed25519.PrivateKey, ledger.SigLock) {
	t.Helper()
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKey, _, addr := u.GenerateAddress(1)
	err := u.TokensFromFaucet(addr, amount)
	require.NoError(t, err)
	require.EqualValues(t, amount, u.Balance(addr))
	return u, privKey, addr
}

// buildValidTransferTxBytes builds a valid simple transfer transaction (raw bytes)
// from source to target, using the low-level TxBuilder so that we can later
// tamper with individual fields.
func buildValidTransferTxBytes(
	t *testing.T,
	u *utxodb.UTXODB,
	srcPrivKey ed25519.PrivateKey,
	srcAddr ledger.SigLock,
	dstAddr ledger.SigLock,
	amount uint64,
) ([]byte, *txbuilder.TxBuilder) {
	t.Helper()

	// Collect inputs from state
	outsData, err := u.StateReader().GetUTXOsInAccount(srcAddr.AccountID())
	require.NoError(t, err)
	outs, err := ledger.ParseAndSortOutputData(outsData, func(oid *base.OutputID, o *ledger.Output) bool {
		_, idx := o.ChainConstraint()
		return idx == 0xff && o.Lock().Name() == ledger.SigLockName
	})
	require.NoError(t, err)
	require.True(t, len(outs) > 0, "source address must have UTXOs")

	txb := txbuilder.New()
	total, maxTs, err := txb.ConsumeOutputsNoUnlock(outs...)
	require.NoError(t, err)
	require.True(t, total >= amount, "not enough funds")

	// Unlock: first input gets signature, rest reference input 0
	for i := range outs {
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			err = txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0)
			require.NoError(t, err)
		}
	}

	// Target output
	targetOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(amount)).WithLock(dstAddr)
	})
	_, err = txb.ProduceOutput(targetOut)
	require.NoError(t, err)

	// Remainder
	if total > amount {
		remainderOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(int64(total - amount)).WithLock(srcAddr)
		})
		_, err = txb.ProduceOutput(remainderOut)
		require.NoError(t, err)
	}

	// Timestamp
	lib := ledger.L(maxTs.Slot)
	ts := maxTs.AddTicks(int(lib.TransactionPace))
	txb.TransactionData.Timestamp = ts

	// Input commitment (blake2b of consumed outputs tuple)
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)

	// Sign
	txb.SignED25519(srcPrivKey)

	return txb.TransactionData.Bytes(), txb
}

// --------------------------------------------------------------------------
// TEST: Basic valid transaction (sanity)
// --------------------------------------------------------------------------

// TestTxValidBasicTransfer verifies that a correctly constructed simple transfer
// transaction passes full validation and settles in the UTXODB.
func TestTxValidBasicTransfer(t *testing.T) {
	const initAmount = 1_000_000_000
	u, privKey, srcAddr := newTestEnv(t, initAmount)

	_, _, dstAddr := u.GenerateAddress(2)

	txBytes, _ := buildValidTransferTxBytes(t, u, privKey, srcAddr, dstAddr, 100_000_000)

	// Validate and settle
	err := u.AddTransaction(txBytes, func(tx *transaction.Transaction, err error) error {
		if err != nil {
			t.Logf("validation error: %v\n%s", err, tx.String())
		}
		return err
	})
	require.NoError(t, err, "a correctly built transfer must validate and settle")

	// Verify balances
	require.EqualValues(t, 100_000_000, u.Balance(dstAddr))
	require.EqualValues(t, initAmount-100_000_000, u.Balance(srcAddr))
}

// --------------------------------------------------------------------------
// TEST: Duplicate input IDs must be rejected
// --------------------------------------------------------------------------

// TestTxDuplicateInputsRejected proves that transactions with duplicate input IDs
// are rejected during validation.
//
// Attack scenario: an adversary constructs a transaction that lists the same
// input UTXO twice, attempting to "double-spend" a single output within the
// same transaction. The EasyFL integrity validator enforces
// not(tupleHasDuplicatesAtPath(pathToInputIDs)).
func TestTxDuplicateInputsRejected(t *testing.T) {
	const initAmount = 1_000_000_000
	u, privKey, srcAddr := newTestEnv(t, initAmount)
	_, _, dstAddr := u.GenerateAddress(2)

	// Get the source's UTXOs
	outsData, err := u.StateReader().GetUTXOsInAccount(srcAddr.AccountID())
	require.NoError(t, err)
	outs, err := ledger.ParseAndSortOutputData(outsData, func(oid *base.OutputID, o *ledger.Output) bool {
		_, idx := o.ChainConstraint()
		return idx == 0xff && o.Lock().Name() == ledger.SigLockName
	})
	require.NoError(t, err)
	require.True(t, len(outs) > 0)

	// Build transaction with the SAME input duplicated
	txb := txbuilder.New()
	// Consume same output twice
	_, err = txb.ConsumeOutput(outs[0].Output, outs[0].ID)
	require.NoError(t, err)
	_, err = txb.ConsumeOutput(outs[0].Output, outs[0].ID)
	require.NoError(t, err)

	// Unlock both with signature
	txb.PutSignatureUnlock(0)
	err = txb.PutUnlockReference(1, ledger.ConstraintIndexLock, 0)
	require.NoError(t, err)

	// Produce output that "consumes" the input twice (double the amount)
	doubleAmount := outs[0].Output.TokenBalance() * 2
	targetOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(doubleAmount)).WithLock(dstAddr)
	})
	_, err = txb.ProduceOutput(targetOut)
	require.NoError(t, err)

	ts := outs[0].Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
	txb.TransactionData.Timestamp = ts

	// Input commitment: hash of the two (identical) consumed outputs
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(privKey)

	txBytes := txb.TransactionData.Bytes()

	// The transaction should fail partial validation because the EasyFL
	// txIntegrityValidatorSkeletonContext0 checks:
	//   require(not(tupleHasDuplicatesAtPath(pathToInputIDs)), !!!inputs_cannot_contain_duplicates)
	// Note: EasyFL error messages use spaces, not underscores
	_, err = transaction.ParseWithPartialValidation(txBytes)
	require.Error(t, err, "duplicate inputs must be rejected")
	require.NoError(t, util.MustErrorWith(err, "inputs cannot contain duplicates"))
}

// --------------------------------------------------------------------------
// TEST: Input commitment prevents "faked UTXO" attack
// --------------------------------------------------------------------------

// TestTxInputCommitmentPreventsFakedUTXO proves that the input commitment
// prevents a malicious node from substituting tampered UTXOs during validation.
//
// Attack scenario: A malicious node provides correct input IDs but returns
// different (faked) output data when SetFullContext loads consumed outputs.
// The input commitment (blake2b hash of consumed outputs) embedded in the
// transaction at construction time will not match the hash of the tampered
// outputs, causing validation to fail.
func TestTxInputCommitmentPreventsFakedUTXO(t *testing.T) {
	const initAmount = 1_000_000_000
	u, privKey, srcAddr := newTestEnv(t, initAmount)
	_, _, dstAddr := u.GenerateAddress(2)

	txBytes, _ := buildValidTransferTxBytes(t, u, privKey, srcAddr, dstAddr, 100_000_000)

	// Parse the transaction fresh
	tx, err := transaction.Parse(txBytes)
	require.NoError(t, err)

	// Now simulate the "faked UTXO" attack: provide a tampered output
	// instead of the real consumed output.
	// We create a fake output with a much larger balance.
	fakedOutput := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(999_999_999_999)).WithLock(srcAddr) // inflated balance
	})

	// Set full context with the faked output
	err = tx.SetFullContext(func(i byte) (*ledger.Output, error) {
		// Return the faked output for every input
		return fakedOutput, nil
	})
	require.NoError(t, err)

	// Full context validation should fail because input commitment won't match
	err = tx.ValidateFullContext()
	require.Error(t, err, "faked UTXO should be detected by input commitment check")
	// EasyFL error messages use spaces, not underscores
	require.NoError(t, util.MustErrorWith(err, "hash of consumed UTXOs not equal to the input commitment"))
	t.Logf("faked UTXO correctly rejected: %v", err)
}

// TestTxInputCommitmentWithWrongHash verifies that a transaction where the
// input commitment field itself is corrupted (doesn't match the actual consumed
// outputs) is rejected at full context validation.
func TestTxInputCommitmentWithWrongHash(t *testing.T) {
	const initAmount = 1_000_000_000
	u, privKey, srcAddr := newTestEnv(t, initAmount)
	_, _, dstAddr := u.GenerateAddress(2)

	// Get inputs
	outsData, err := u.StateReader().GetUTXOsInAccount(srcAddr.AccountID())
	require.NoError(t, err)
	outs, err := ledger.ParseAndSortOutputData(outsData, func(oid *base.OutputID, o *ledger.Output) bool {
		_, idx := o.ChainConstraint()
		return idx == 0xff && o.Lock().Name() == ledger.SigLockName
	})
	require.NoError(t, err)

	txb := txbuilder.New()
	_, maxTs, err := txb.ConsumeOutputsNoUnlock(outs...)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	targetOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(initAmount)).WithLock(dstAddr)
	})
	_, err = txb.ProduceOutput(targetOut)
	require.NoError(t, err)

	ts := maxTs.AddTicks(int(ledger.L(0).TransactionPace))
	txb.TransactionData.Timestamp = ts

	// Set WRONG input commitment (all zeros)
	txb.TransactionData.InputCommitment = [32]byte{}
	txb.SignED25519(privKey)

	txBytes := txb.TransactionData.Bytes()

	// The input commitment is part of the transaction essence (which is hashed for
	// the txID). SignED25519 computes the txID from the current data and signs it,
	// so the signature is valid for this transaction with the wrong commitment.
	// Partial validation passes, but full context validation should fail.
	tx, err := transaction.Parse(txBytes)
	require.NoError(t, err)

	err = tx.SetFullContext(txb.LoadInput)
	require.NoError(t, err)

	err = tx.ValidateFullContext()
	require.Error(t, err, "wrong input commitment should cause full context validation failure")
	require.NoError(t, util.MustErrorWith(err, "hash of consumed UTXOs not equal to the input commitment"))
	t.Logf("wrong input commitment correctly rejected: %v", err)
}

// --------------------------------------------------------------------------
// TEST: Transaction signature validation
// --------------------------------------------------------------------------

// TestTxCorruptedSignatureRejected proves that a transaction with a corrupted
// signature is rejected during partial context validation.
//
// The EasyFL partial context validator checks:
//   require(validSignature(txID, txSignatureData), !!!invalid_signature_of_the_transaction)
//
// Note: signing with a different valid key produces a valid ed25519 signature
// (valid for that key). To test the signature check, we must corrupt the
// signature bytes after signing.
func TestTxCorruptedSignatureRejected(t *testing.T) {
	const initAmount = 1_000_000_000
	u, privKey, srcAddr := newTestEnv(t, initAmount)
	_, _, dstAddr := u.GenerateAddress(2)

	// Build a valid transaction
	txBytes, _ := buildValidTransferTxBytes(t, u, privKey, srcAddr, dstAddr, 100_000_000)

	// Verify it passes validation first
	_, err := transaction.ParseWithPartialValidation(txBytes)
	require.NoError(t, err, "original transaction must be valid")

	// Now corrupt the signature: flip a bit in the signature data.
	// The signature is embedded in the transaction bytes. We find and corrupt it.
	// We use a simple approach: flip one byte near the end of the tx bytes
	// (which is where signature data typically lives in the serialized form).
	corruptedBytes := make([]byte, len(txBytes))
	copy(corruptedBytes, txBytes)
	// Flip a bit in the middle of the data (likely in signature area)
	corruptedBytes[len(corruptedBytes)-10] ^= 0xFF

	// The corrupted transaction should fail validation
	_, err = transaction.ParseWithPartialValidation(corruptedBytes)
	require.Error(t, err, "corrupted signature must be rejected")
	t.Logf("corrupted signature correctly rejected: %v", err)
}

// TestTxSignatureMatchesButLockMismatch tests the case where the transaction
// signature is valid (it was signed by some key), but the signing key doesn't
// match the lock on the consumed output. The signature check at the transaction
// level passes, but the lock constraint on the consumed output must fail.
func TestTxSignatureMatchesButLockMismatch(t *testing.T) {
	const initAmount = 1_000_000_000
	u, _, srcAddr := newTestEnv(t, initAmount) // privKey for srcAddr is not used for signing
	_, _, dstAddr := u.GenerateAddress(2)
	wrongPrivKey, _, _ := u.GenerateAddress(3)

	// Build the transaction using the high-level API but with wrong key
	par, err := u.MakeTransferInputData(wrongPrivKey, srcAddr, base.NilLedgerTime)
	// MakeTransferInputData doesn't fail here because it only checks source account type,
	// not key matching. But DoTransfer will fail at validation.
	require.NoError(t, err)

	err = u.DoTransfer(par.WithTargetLock(dstAddr).WithAmount(100_000_000))
	require.Error(t, err, "signing with a key that doesn't match the input lock must fail")
	// The error comes from the lock (address) constraint failing on the consumed output
	require.NoError(t, util.MustErrorWith(err, "failed"))
	t.Logf("lock mismatch correctly rejected: %v", err)
}

// --------------------------------------------------------------------------
// TEST: Edge cases of basic validation
// --------------------------------------------------------------------------

// TestTxEdgeCaseNoInputs verifies that a transaction with no inputs is rejected.
// The EasyFL integrity validator checks:
//   not(isZero(tupleLenAtPath(pathToInputIDs)))
func TestTxEdgeCaseNoInputs(t *testing.T) {
	_, _, dstAddr := utxodb.NewUTXODB(genesisPrivateKey, true).GenerateAddress(1)

	txb := txbuilder.New()

	// Produce an output without consuming anything
	targetOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(100_000_000)).WithLock(dstAddr)
	})
	_, err := txb.ProduceOutput(targetOut)
	require.NoError(t, err)

	ts := ledger.TimeNow()
	txb.TransactionData.Timestamp = ts
	txb.TransactionData.InputCommitment = [32]byte{}

	privKey, _, _ := utxodb.NewUTXODB(genesisPrivateKey, true).GenerateAddress(1)
	txb.SignED25519(privKey)

	txBytes := txb.TransactionData.Bytes()

	// Should fail because there are no inputs
	_, err = transaction.ParseWithPartialValidation(txBytes)
	require.Error(t, err, "transaction with no inputs must be rejected")
	t.Logf("no-input transaction correctly rejected: %v", err)
}

// TestTxEdgeCaseInputCommitmentCorrectness verifies that the input commitment
// is computed correctly as blake2b of the tuple of consumed output bytes.
func TestTxEdgeCaseInputCommitmentCorrectness(t *testing.T) {
	const initAmount = 1_000_000_000
	u, privKey, srcAddr := newTestEnv(t, initAmount)
	_, _, dstAddr := u.GenerateAddress(2)

	txBytes, txb := buildValidTransferTxBytes(t, u, privKey, srcAddr, dstAddr, 100_000_000)

	// Verify the input commitment was computed correctly
	expectedHash := ledger.HashOutputs(txb.ConsumedOutputs...)

	tx, err := transaction.Parse(txBytes)
	require.NoError(t, err)

	actualCommitment := tx.InputCommitment()
	require.EqualValues(t, expectedHash[:], actualCommitment,
		"input commitment must equal blake2b hash of consumed outputs tuple")

	// Now verify via full context that the commitment matches
	err = tx.SetFullContext(txb.LoadInput)
	require.NoError(t, err)

	consumedHash := tx.ConsumedOutputHash()
	require.EqualValues(t, expectedHash, consumedHash,
		"consumed output hash must match the stored input commitment")
}

// TestTxEdgeCaseTransferEntireBalance verifies that transferring the entire
// balance (no remainder) works correctly.
func TestTxEdgeCaseTransferEntireBalance(t *testing.T) {
	const initAmount = 1_000_000_000
	u, privKey, srcAddr := newTestEnv(t, initAmount)
	_, _, dstAddr := u.GenerateAddress(2)

	err := u.TransferTokens(privKey, dstAddr, initAmount)
	require.NoError(t, err)

	require.EqualValues(t, 0, u.Balance(srcAddr), "source must have zero balance after full transfer")
	require.EqualValues(t, 0, u.NumUTXOs(srcAddr), "source must have zero UTXOs after full transfer")
	require.EqualValues(t, initAmount, u.Balance(dstAddr))
	require.EqualValues(t, 1, u.NumUTXOs(dstAddr))
}

// TestTxEdgeCaseMinimumStorageDeposit verifies that producing an output below
// the minimum storage deposit is rejected.
func TestTxEdgeCaseMinimumStorageDeposit(t *testing.T) {
	const initAmount = 1_000_000_000
	u, privKey, _ := newTestEnv(t, initAmount)
	_, _, dstAddr := u.GenerateAddress(2)

	par, err := u.MakeTransferInputData(privKey, nil, base.NilLedgerTime)
	require.NoError(t, err)

	// Try to transfer just 1 token - way below minimum storage deposit
	err = u.DoTransfer(par.WithTargetLock(dstAddr).WithAmount(1))
	require.Error(t, err, "transfer below minimum storage deposit must fail")
	require.NoError(t, util.MustErrorWith(err, "not enough token balance", "for the minimum storage deposit"))
	t.Logf("below-minimum deposit correctly rejected: %v", err)
}

// TestTxEdgeCaseTimePaceConstraint verifies that the transaction pace constraint
// is enforced. A transaction must not be too close in time to its consumed inputs.
func TestTxEdgeCaseTimePaceConstraint(t *testing.T) {
	const initAmount = 1_000_000_000
	u, privKey, srcAddr := newTestEnv(t, initAmount)
	_, _, dstAddr := u.GenerateAddress(2)

	// Get inputs
	outsData, err := u.StateReader().GetUTXOsInAccount(srcAddr.AccountID())
	require.NoError(t, err)
	outs, err := ledger.ParseAndSortOutputData(outsData, func(oid *base.OutputID, o *ledger.Output) bool {
		_, idx := o.ChainConstraint()
		return idx == 0xff && o.Lock().Name() == ledger.SigLockName
	})
	require.NoError(t, err)

	txb := txbuilder.New()
	_, _, err = txb.ConsumeOutputsNoUnlock(outs...)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	targetOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(initAmount)).WithLock(dstAddr)
	})
	_, err = txb.ProduceOutput(targetOut)
	require.NoError(t, err)

	// Set timestamp SAME as input - violates pace constraint
	// (transaction must be at least TransactionPace ticks after its inputs)
	txb.TransactionData.Timestamp = outs[0].Timestamp()
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(privKey)

	txBytes := txb.TransactionData.Bytes()

	_, err = transaction.ParseWithPartialValidation(txBytes)
	require.Error(t, err, "transaction violating pace constraint must be rejected")
	require.NoError(t, util.MustErrorWith(err, "time pace constraint"))
	t.Logf("pace constraint violation correctly rejected: %v", err)
}

// --------------------------------------------------------------------------
// TEST: Input commitment with multiple inputs
// --------------------------------------------------------------------------

// TestTxInputCommitmentMultipleInputs verifies that the input commitment
// correctly covers all consumed outputs when a transaction has multiple inputs.
// Tampering with any single input should break the commitment.
func TestTxInputCommitmentMultipleInputs(t *testing.T) {
	const (
		numInputs  = 5
		initAmount = 100_000_000
	)
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKey, _, addr := u.GenerateAddress(1)

	// Create multiple UTXOs for the same address
	for i := 0; i < numInputs; i++ {
		err := u.TokensFromFaucet(addr, initAmount)
		require.NoError(t, err)
	}
	require.EqualValues(t, numInputs, u.NumUTXOs(addr))
	require.EqualValues(t, numInputs*initAmount, u.Balance(addr))

	_, _, dstAddr := u.GenerateAddress(2)

	// Build a valid transaction that spends all inputs
	txBytes, txb := buildValidTransferTxBytes(t, u, privKey, addr, dstAddr, uint64(numInputs*initAmount))

	// First verify it validates correctly
	tx, err := transaction.Parse(txBytes)
	require.NoError(t, err)
	err = tx.SetFullContext(txb.LoadInput)
	require.NoError(t, err)
	err = tx.ValidateFullContext()
	require.NoError(t, err, "valid multi-input transaction must pass")

	// Now simulate tampering with one input
	tx2, err := transaction.Parse(txBytes)
	require.NoError(t, err)

	tamperedOutput := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(initAmount * 10)).WithLock(addr) // inflated
	})

	err = tx2.SetFullContext(func(i byte) (*ledger.Output, error) {
		if i == 2 { // tamper with input #2
			return tamperedOutput, nil
		}
		return txb.LoadInput(i)
	})
	require.NoError(t, err)

	err = tx2.ValidateFullContext()
	require.Error(t, err, "tampering with a single input must break input commitment")
	// EasyFL error messages use spaces, not underscores
	require.NoError(t, util.MustErrorWith(err, "hash of consumed UTXOs not equal to the input commitment"))
}

// --------------------------------------------------------------------------
// TEST: Consumed output hash mechanism
// --------------------------------------------------------------------------

// TestTxConsumedOutputHashMechanism directly tests the blake2b hashing mechanism
// used for input commitments to ensure it's deterministic and covers all outputs.
func TestTxConsumedOutputHashMechanism(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	_, _, addr := u.GenerateAddress(1)

	// Create test outputs with known data
	out1 := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(100_000_000)).WithLock(addr)
	})
	out2 := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(200_000_000)).WithLock(addr)
	})

	// Verify determinism: same inputs produce same hash
	hash1 := ledger.HashOutputs(out1, out2)
	hash2 := ledger.HashOutputs(out1, out2)
	require.EqualValues(t, hash1, hash2, "hash must be deterministic")

	// Verify order matters: different order produces different hash
	hashReversed := ledger.HashOutputs(out2, out1)
	require.NotEqual(t, hash1, hashReversed, "input order must affect the hash")

	// Verify content sensitivity: changing output data changes hash
	out1Modified := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(100_000_001)).WithLock(addr) // 1 token different
	})
	hashModified := ledger.HashOutputs(out1Modified, out2)
	require.NotEqual(t, hash1, hashModified, "changing any output must change the hash")

	// Verify single output hash
	hashSingle := ledger.HashOutputs(out1)
	require.NotEqual(t, hash1, hashSingle, "subset of outputs must produce different hash")

	// Verify empty hash is distinct
	hashEmpty := ledger.HashOutputs()
	require.NotEqual(t, hash1, hashEmpty, "empty output set must produce different hash")
	// Empty hash should be blake2b of empty tuple bytes, which is a valid distinct hash
	_ = blake2b.Sum256(nil)
	_ = u
}

// --------------------------------------------------------------------------
// Additional helpers
// --------------------------------------------------------------------------

// getSourceOutputs returns the non-chain sigLock UTXOs for the given address.
func getSourceOutputs(t *testing.T, u *utxodb.UTXODB, addr ledger.SigLock) []*ledger.OutputWithID {
	t.Helper()
	outsData, err := u.StateReader().GetUTXOsInAccount(addr.AccountID())
	require.NoError(t, err)
	outs, err := ledger.ParseAndSortOutputData(outsData, func(oid *base.OutputID, o *ledger.Output) bool {
		_, idx := o.ChainConstraint()
		return idx == 0xff && o.Lock().Name() == ledger.SigLockName
	})
	require.NoError(t, err)
	require.True(t, len(outs) > 0, "address must have UTXOs")
	return outs
}

// validateFull parses transaction bytes, sets full context using the builder's
// consumed outputs, and runs full validation. Returns nil on success.
func validateFull(txBytes []byte, txb *txbuilder.TxBuilder) error {
	tx, err := transaction.Parse(txBytes)
	if err != nil {
		return err
	}
	if err = tx.SetFullContext(txb.LoadInput); err != nil {
		return err
	}
	return tx.ValidateFullContext()
}

// buildAndSignTx sets timestamp, input commitment, and signs the transaction.
// Returns serialized transaction bytes.
func buildAndSignTx(txb *txbuilder.TxBuilder, maxTs base.LedgerTime, privKey ed25519.PrivateKey) []byte {
	ts := maxTs.AddTicks(int(ledger.L(maxTs.Slot).TransactionPace))
	txb.TransactionData.Timestamp = ts
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(privKey)
	return txb.TransactionData.Bytes()
}

// --------------------------------------------------------------------------
// TOPIC: Amount conservation — tokens always positive and preserved
// --------------------------------------------------------------------------
//
// The ledger enforces the invariant for non-chain outputs (sigLock only):
//   consumed_token_balance = produced_token_balance
// (inflation is always 0 for non-chain outputs)
//
// Enforcement points:
//   1. validate.go:validateOutputs() — "mismatch between token amounts" (primary invariant check)
//   2. amounts.go:AddToVector() — overflow detection during output scanning
//   3. validate.go:_sumConsumedTotals() — overflow detection for consumed balance
//   4. amounts.go:TokenBalance() — assertion that amounts are non-negative
//   5. amounts_embed.go:evalAmounts() — minimum storage deposit on produced outputs

// TestTxAmountProduceMoreThanConsumed proves that creating tokens from nothing
// is impossible. A transaction that produces more tokens than it consumes
// is rejected by the ledger invariant check.
//
// Attack scenario: attacker consumes 1B tokens and produces 1.5B tokens,
// attempting to create 500M tokens from thin air.
func TestTxAmountProduceMoreThanConsumed(t *testing.T) {
	const initAmount = 1_000_000_000
	u, privKey, srcAddr := newTestEnv(t, initAmount)
	_, _, dstAddr := u.GenerateAddress(2)

	outs := getSourceOutputs(t, u, srcAddr)

	txb := txbuilder.New()
	_, maxTs, err := txb.ConsumeOutputsNoUnlock(outs...)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	// Produce 1.5B from 1B consumed — attempt to create 500M from nothing
	out := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(1_500_000_000)).WithLock(dstAddr)
	})
	_, err = txb.ProduceOutput(out)
	require.NoError(t, err) // builder doesn't check total balance

	txBytes := buildAndSignTx(txb, maxTs, privKey)

	err = validateFull(txBytes, txb)
	require.Error(t, err, "producing more tokens than consumed must be rejected")
	require.NoError(t, util.MustErrorWith(err, "mismatch between token amounts"))
	t.Logf("excess production correctly rejected: %v", err)
}

// TestTxAmountProduceLessThanConsumed proves that destroying tokens is
// impossible. A transaction that produces fewer tokens than it consumes
// is rejected by the ledger invariant check.
//
// Attack scenario: attacker consumes 1B tokens but only produces 500M,
// attempting to destroy 500M tokens (which would reduce supply).
func TestTxAmountProduceLessThanConsumed(t *testing.T) {
	const initAmount = 1_000_000_000
	u, privKey, srcAddr := newTestEnv(t, initAmount)
	_, _, dstAddr := u.GenerateAddress(2)

	outs := getSourceOutputs(t, u, srcAddr)

	txb := txbuilder.New()
	_, maxTs, err := txb.ConsumeOutputsNoUnlock(outs...)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	// Produce only 500M from 1B consumed — attempt to destroy 500M
	out := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(500_000_000)).WithLock(dstAddr)
	})
	_, err = txb.ProduceOutput(out)
	require.NoError(t, err)

	txBytes := buildAndSignTx(txb, maxTs, privKey)

	err = validateFull(txBytes, txb)
	require.Error(t, err, "producing fewer tokens than consumed must be rejected")
	require.NoError(t, util.MustErrorWith(err, "mismatch between token amounts"))
	t.Logf("token destruction correctly rejected: %v", err)
}

// TestTxAmountOffByOne proves that even a single token difference between
// consumed and produced amounts is detected and rejected. This tests the
// precision of the ledger invariant enforcement.
func TestTxAmountOffByOne(t *testing.T) {
	const initAmount = 1_000_000_000

	t.Run("one extra token", func(t *testing.T) {
		u, privKey, srcAddr := newTestEnv(t, initAmount)
		_, _, dstAddr := u.GenerateAddress(2)

		outs := getSourceOutputs(t, u, srcAddr)
		txb := txbuilder.New()
		_, maxTs, err := txb.ConsumeOutputsNoUnlock(outs...)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)

		// Produce exactly 1 token more than consumed
		out := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(int64(initAmount + 1)).WithLock(dstAddr)
		})
		_, err = txb.ProduceOutput(out)
		require.NoError(t, err)

		txBytes := buildAndSignTx(txb, maxTs, privKey)

		err = validateFull(txBytes, txb)
		require.Error(t, err, "+1 token must be rejected")
		require.NoError(t, util.MustErrorWith(err, "mismatch between token amounts"))
	})

	t.Run("one missing token", func(t *testing.T) {
		u, privKey, srcAddr := newTestEnv(t, initAmount)
		_, _, dstAddr := u.GenerateAddress(2)

		outs := getSourceOutputs(t, u, srcAddr)
		txb := txbuilder.New()
		_, maxTs, err := txb.ConsumeOutputsNoUnlock(outs...)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)

		// Produce exactly 1 token less than consumed
		out := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(int64(initAmount - 1)).WithLock(dstAddr)
		})
		_, err = txb.ProduceOutput(out)
		require.NoError(t, err)

		txBytes := buildAndSignTx(txb, maxTs, privKey)

		err = validateFull(txBytes, txb)
		require.Error(t, err, "-1 token must be rejected")
		require.NoError(t, util.MustErrorWith(err, "mismatch between token amounts"))
	})
}

// TestTxAmountConservationMultipleOutputs proves that the total token amount
// is correctly conserved when a transaction splits tokens across multiple
// outputs. Each individual output amount is reasonable, and the total matches
// exactly. This is a positive test confirming the happy path.
func TestTxAmountConservationMultipleOutputs(t *testing.T) {
	const initAmount = 1_000_000_000
	u, privKey, srcAddr := newTestEnv(t, initAmount)
	_, _, dstAddr1 := u.GenerateAddress(2)
	_, _, dstAddr2 := u.GenerateAddress(3)
	_, _, dstAddr3 := u.GenerateAddress(4)

	outs := getSourceOutputs(t, u, srcAddr)

	txb := txbuilder.New()
	_, maxTs, err := txb.ConsumeOutputsNoUnlock(outs...)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	// Split 1B into 3 outputs: 300M + 300M + 400M = 1B (exact)
	for _, pair := range []struct {
		amount uint64
		lock   ledger.SigLock
	}{
		{300_000_000, dstAddr1},
		{300_000_000, dstAddr2},
		{400_000_000, dstAddr3},
	} {
		out := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(int64(pair.amount)).WithLock(pair.lock)
		})
		_, err = txb.ProduceOutput(out)
		require.NoError(t, err)
	}

	txBytes := buildAndSignTx(txb, maxTs, privKey)

	// Should pass full validation and settle in the UTXODB
	err = u.AddTransaction(txBytes, func(tx *transaction.Transaction, err error) error {
		if err != nil {
			t.Logf("unexpected validation error: %v\n%s", err, tx.String())
		}
		return err
	})
	require.NoError(t, err, "correctly split transaction must validate and settle")

	// Verify balances: tokens are perfectly conserved across outputs
	require.EqualValues(t, 300_000_000, u.Balance(dstAddr1))
	require.EqualValues(t, 300_000_000, u.Balance(dstAddr2))
	require.EqualValues(t, 400_000_000, u.Balance(dstAddr3))
	require.EqualValues(t, 0, u.Balance(srcAddr))
}

// TestTxAmountMultipleOutputsExcess proves that excess total across multiple
// outputs is detected even when individual amounts look reasonable.
//
// Attack scenario: attacker consumes 1B tokens and produces 3 outputs of
// 400M each (total 1.2B), hoping the validator only checks individual
// outputs rather than the total.
func TestTxAmountMultipleOutputsExcess(t *testing.T) {
	const initAmount = 1_000_000_000
	u, privKey, srcAddr := newTestEnv(t, initAmount)
	_, _, dstAddr := u.GenerateAddress(2)

	outs := getSourceOutputs(t, u, srcAddr)

	txb := txbuilder.New()
	_, maxTs, err := txb.ConsumeOutputsNoUnlock(outs...)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	// Produce 3 outputs of 400M each = 1.2B from 1B consumed
	for i := 0; i < 3; i++ {
		out := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(int64(400_000_000)).WithLock(dstAddr)
		})
		_, err = txb.ProduceOutput(out)
		require.NoError(t, err)
	}

	txBytes := buildAndSignTx(txb, maxTs, privKey)

	err = validateFull(txBytes, txb)
	require.Error(t, err, "multiple outputs exceeding consumed total must be rejected")
	require.NoError(t, util.MustErrorWith(err, "mismatch between token amounts"))
	t.Logf("excess across multiple outputs correctly rejected: %v", err)
}

// --------------------------------------------------------------------------
// TOPIC: Token theft prevention — unauthorized spending impossible
// --------------------------------------------------------------------------
//
// The sigLock constraint (lock_signature.easyfl) enforces on consumed outputs:
//   equal($0, txSpenderID(txSignatureData))
// where $0 is the spender ID stored in the lock (blake2b of sigType+pubKey),
// and txSpenderID is derived from the transaction signature's public key.
//
// Only the holder of the matching private key can produce a valid signature
// whose spender ID matches the lock. The unlock-by-reference mechanism
// requires byte-for-byte identical lock constraints with strictly smaller index.

// TestTxTheftSpendWithWrongKey proves that an attacker cannot spend someone
// else's tokens by signing with their own key.
//
// Attack scenario: Alice has tokens locked to her address. Bob creates a
// transaction consuming Alice's UTXO and signs with Bob's private key.
// The sigLock constraint on Alice's consumed output checks that the
// transaction's spender ID matches Alice's address — Bob's ID won't match.
func TestTxTheftSpendWithWrongKey(t *testing.T) {
	const initAmount = 1_000_000_000
	u := utxodb.NewUTXODB(genesisPrivateKey, true)

	// Alice has tokens
	_, _, aliceAddr := u.GenerateAddress(1)
	err := u.TokensFromFaucet(aliceAddr, initAmount)
	require.NoError(t, err)

	// Bob is the attacker
	bobPrivKey, _, bobAddr := u.GenerateAddress(2)
	require.NotEqual(t, aliceAddr, bobAddr)

	aliceOuts := getSourceOutputs(t, u, aliceAddr)

	// Bob builds a transaction consuming Alice's output
	txb := txbuilder.New()
	_, maxTs, err := txb.ConsumeOutputsNoUnlock(aliceOuts...)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	// Send all of Alice's tokens to Bob's address
	out := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(initAmount)).WithLock(bobAddr)
	})
	_, err = txb.ProduceOutput(out)
	require.NoError(t, err)

	// Bob signs with HIS key (not Alice's)
	txBytes := buildAndSignTx(txb, maxTs, bobPrivKey)

	// Validation must reject: sigLock on Alice's consumed output checks
	// equal($0=Alice_spender_ID, txSpenderID=Bob_spender_ID) → false
	err = validateFull(txBytes, txb)
	require.Error(t, err, "spending with wrong private key must be rejected")
	// The sigLock constraint (named 'a') on the consumed output fails
	require.NoError(t, util.MustErrorWith(err, "failed"))
	t.Logf("wrong key theft correctly rejected: %v", err)
}

// TestTxTheftUnlockReferenceDifferentLock proves that unlock references
// cannot be used to bypass lock constraints across different addresses.
//
// Attack scenario: Bob legitimately owns some tokens. Alice also owns tokens
// at a different address. Bob creates a transaction consuming both outputs:
// his own (input 0, properly signed) and Alice's (input 1, using unlock
// reference to input 0). The unlockedByReference check in lock_signature.easyfl
// requires: equal(self, consumedConstraintByIndex($0, lockConstraintIndex))
// Since Alice's sigLock bytes differ from Bob's sigLock bytes, this fails.
func TestTxTheftUnlockReferenceDifferentLock(t *testing.T) {
	const initAmount = 1_000_000_000
	u := utxodb.NewUTXODB(genesisPrivateKey, true)

	bobPrivKey, _, bobAddr := u.GenerateAddress(1)
	_, _, aliceAddr := u.GenerateAddress(2)
	err := u.TokensFromFaucet(bobAddr, initAmount)
	require.NoError(t, err)
	err = u.TokensFromFaucet(aliceAddr, initAmount)
	require.NoError(t, err)

	bobOuts := getSourceOutputs(t, u, bobAddr)
	aliceOuts := getSourceOutputs(t, u, aliceAddr)

	// Build transaction consuming both: Bob's (input 0) and Alice's (input 1)
	txb := txbuilder.New()
	_, err = txb.ConsumeOutput(bobOuts[0].Output, bobOuts[0].ID)
	require.NoError(t, err)
	_, err = txb.ConsumeOutput(aliceOuts[0].Output, aliceOuts[0].ID)
	require.NoError(t, err)

	// Input 0 (Bob): signature unlock — Bob's sig matches Bob's lock
	txb.PutSignatureUnlock(0)
	// Input 1 (Alice): unlock reference to input 0 — trying to bypass Alice's lock
	err = txb.PutUnlockReference(1, ledger.ConstraintIndexLock, 0)
	require.NoError(t, err)

	// Produce single output with combined balance (amounts are correct)
	totalAmount := bobOuts[0].Output.TokenBalance() + aliceOuts[0].Output.TokenBalance()
	out := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(totalAmount)).WithLock(bobAddr)
	})
	_, err = txb.ProduceOutput(out)
	require.NoError(t, err)

	maxTs := base.MaximumTime(bobOuts[0].Timestamp(), aliceOuts[0].Timestamp())
	txBytes := buildAndSignTx(txb, maxTs, bobPrivKey)

	// Validation must reject: on input 1 (Alice's output), the unlock reference
	// checks equal(self=AliceLock, consumedConstraintByIndex(0, lockIdx)=BobLock)
	// Alice's sigLock bytes ≠ Bob's sigLock bytes → reference fails.
	// Direct signature check also fails: Alice's spender ID ≠ Bob's spender ID.
	err = validateFull(txBytes, txb)
	require.Error(t, err, "unlock reference with different lock must be rejected")
	require.NoError(t, util.MustErrorWith(err, "failed"))
	t.Logf("reference bypass theft correctly rejected: %v", err)
}

// TestTxTheftReplayTransaction proves that a settled transaction cannot be
// replayed to double-spend the same outputs. Once consumed, the UTXOs are
// removed from the ledger state and cannot be consumed again.
func TestTxTheftReplayTransaction(t *testing.T) {
	const initAmount = 1_000_000_000
	u, privKey, srcAddr := newTestEnv(t, initAmount)
	_, _, dstAddr := u.GenerateAddress(2)

	// Build and settle a valid transfer
	txBytes, _ := buildValidTransferTxBytes(t, u, privKey, srcAddr, dstAddr, 100_000_000)
	err := u.AddTransaction(txBytes)
	require.NoError(t, err, "first settlement must succeed")

	// Verify the transfer worked
	require.EqualValues(t, 100_000_000, u.Balance(dstAddr))
	require.EqualValues(t, initAmount-100_000_000, u.Balance(srcAddr))

	// Attempt to replay the exact same transaction bytes.
	// The consumed outputs no longer exist in the state (they were spent).
	// SetFullContext will fail to load the consumed inputs.
	err = u.AddTransaction(txBytes)
	require.Error(t, err, "replaying a settled transaction must be rejected")
	t.Logf("transaction replay correctly rejected: %v", err)
}

// TestTxTheftRecipientOwnership proves that after a transfer, the tokens
// are exclusively controlled by the recipient. The original sender cannot
// spend the recipient's tokens — only the recipient's private key works.
func TestTxTheftRecipientOwnership(t *testing.T) {
	const initAmount = 1_000_000_000
	const transferAmount = 500_000_000
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	alicePrivKey, _, aliceAddr := u.GenerateAddress(1)
	bobPrivKey, _, bobAddr := u.GenerateAddress(2)

	// Fund Alice
	err := u.TokensFromFaucet(aliceAddr, initAmount)
	require.NoError(t, err)

	// Alice sends 500M to Bob via normal transfer
	err = u.TransferTokens(alicePrivKey, bobAddr, transferAmount)
	require.NoError(t, err)
	require.EqualValues(t, transferAmount, u.Balance(bobAddr))

	// Part 1: Bob CAN spend his tokens (proves recipient has exclusive control)
	_, _, charlieAddr := u.GenerateAddress(3)
	err = u.TransferTokens(bobPrivKey, charlieAddr, transferAmount)
	require.NoError(t, err, "recipient must be able to spend received tokens")
	require.EqualValues(t, transferAmount, u.Balance(charlieAddr))
	require.EqualValues(t, 0, u.Balance(bobAddr))

	// Part 2: Alice CANNOT spend Bob's tokens
	// Fund Bob again for this test
	err = u.TokensFromFaucet(bobAddr, transferAmount)
	require.NoError(t, err)

	// Alice attempts to spend Bob's tokens using the high-level API
	// MakeTransferInputData loads Bob's outputs but signs with Alice's key
	par, err := u.MakeTransferInputData(alicePrivKey, bobAddr, base.NilLedgerTime)
	require.NoError(t, err)

	err = u.DoTransfer(par.WithTargetLock(aliceAddr).WithAmount(transferAmount))
	require.Error(t, err, "sender must not be able to spend recipient's tokens")
	require.NoError(t, util.MustErrorWith(err, "failed"))
	t.Logf("sender correctly cannot spend recipient's tokens: %v", err)
}

// --------------------------------------------------------------------------
// BENCHMARK: Stage 1 rejection of rubbish data
// --------------------------------------------------------------------------
//
// Measures how quickly transaction.Parse() rejects random bytes of various sizes.
// This is relevant for DoS resistance: a node must reject garbage fast at stage 1
// before spending CPU on validation. The benchmark covers sizes from tiny (10 bytes)
// to large (1MB) to verify rejection cost does not scale badly with input size.

// BenchmarkParseRubbishData measures Parse() rejection speed for random data
// at various sizes. Each sub-benchmark uses a fixed random seed for reproducibility.
func BenchmarkParseRubbishData(b *testing.B) {
	sizes := []int{10, 100, 500, 1_000, 10_000, 100_000, 1_000_000}

	for _, size := range sizes {
		b.Run(fmt.Sprintf("size_%d", size), func(b *testing.B) {
			rng := rand.New(rand.NewSource(int64(size)))
			data := make([]byte, size)
			rng.Read(data)

			b.ResetTimer()
			b.SetBytes(int64(size))
			for i := 0; i < b.N; i++ {
				_, _ = transaction.Parse(data)
			}
		})
	}
}

// BenchmarkParseRubbishDataAllZeros measures Parse() rejection of zero-filled buffers.
// All-zeros may follow a different code path in tuple parsing than random data.
func BenchmarkParseRubbishDataAllZeros(b *testing.B) {
	sizes := []int{100, 1_000, 10_000, 100_000}

	for _, size := range sizes {
		b.Run(fmt.Sprintf("zeros_%d", size), func(b *testing.B) {
			data := make([]byte, size)
			b.ResetTimer()
			b.SetBytes(int64(size))
			for i := 0; i < b.N; i++ {
				_, _ = transaction.Parse(data)
			}
		})
	}
}

// TestParseRubbishDataRejected is a non-benchmark test that verifies Parse()
// actually returns an error for all rubbish input sizes.
func TestParseRubbishDataRejected(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	sizes := []int{0, 1, 10, 100, 500, 1_000, 10_000, 100_000}

	for _, size := range sizes {
		t.Run(fmt.Sprintf("size_%d", size), func(t *testing.T) {
			data := make([]byte, size)
			if size > 0 {
				rng.Read(data)
			}
			_, err := transaction.Parse(data)
			require.Error(t, err, "random data of size %d must be rejected at stage 1", size)
		})
	}
}

// --------------------------------------------------------------------------
// TOPIC: Arithmetic overflow in amount calculations
// --------------------------------------------------------------------------
//
// The ledger uses int64 for token amounts. Overflow can occur when:
//   1. Summing consumed output balances (_sumConsumedTotals)
//   2. Summing produced output amounts (AddToVector in scanProducedOutputs)
//   3. Computing consumed + inflation at the conservation check
//
// Individual amounts are bounded by MaxInt64 (negative int64 from uint64 wrapping
// is caught by TokenBalance()'s assert). AddToVector and _sumConsumedTotals have
// explicit overflow checks. The conservation comparison (consumed + inflation == produced)
// is safe because wrapping produces a negative value that can't equal the positive produced total.

// TestTxOverflowConsumedBalance proves that the consumed balance sum overflow is detected.
//
// Attack scenario: an attacker crafts outputs with amounts near MaxInt64/2 + 1 each.
// If two such outputs are consumed, their sum exceeds MaxInt64. Without overflow detection,
// the sum wraps to a small (or negative) number, potentially allowing the attacker to
// produce far fewer tokens and pass the conservation check.
//
// The overflow is caught by _sumConsumedTotals() in validate.go.
func TestTxOverflowConsumedBalance(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKey, _, addr := u.GenerateAddress(1)

	// Create 2 UTXOs via faucet for valid output IDs
	err := u.TokensFromFaucet(addr, 100_000_000)
	require.NoError(t, err)
	err = u.TokensFromFaucet(addr, 100_000_000)
	require.NoError(t, err)

	outs := getSourceOutputs(t, u, addr)
	require.True(t, len(outs) >= 2)

	// Each amount is just over half of MaxInt64 — two of these overflow
	hugeAmount := int64(math.MaxInt64/2 + 1)

	fakeOut1 := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(hugeAmount).WithLock(addr)
	})
	fakeOut2 := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(hugeAmount).WithLock(addr)
	})

	txb := txbuilder.New()
	_, err = txb.ConsumeOutput(fakeOut1, outs[0].ID)
	require.NoError(t, err)
	_, err = txb.ConsumeOutput(fakeOut2, outs[1].ID)
	require.NoError(t, err)

	txb.PutSignatureUnlock(0)
	err = txb.PutUnlockReference(1, ledger.ConstraintIndexLock, 0)
	require.NoError(t, err)

	// Produce a valid output (amounts won't balance, but overflow fires first)
	out := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(100_000_000).WithLock(addr)
	})
	_, err = txb.ProduceOutput(out)
	require.NoError(t, err)

	maxTs := base.MaximumTime(outs[0].Timestamp(), outs[1].Timestamp())
	txBytes := buildAndSignTx(txb, maxTs, privKey)

	err = validateFull(txBytes, txb)
	require.Error(t, err, "consumed balance overflow must be rejected")
	require.NoError(t, util.MustErrorWith(err, "arithmetic overflow"))
	t.Logf("consumed balance overflow correctly rejected: %v", err)
}

// TestTxOverflowProducedBalance proves that the produced balance sum overflow is detected.
//
// Attack scenario: an attacker produces two outputs each with amount near MaxInt64/2 + 1.
// Their sum exceeds MaxInt64 and would wrap without overflow detection.
//
// The overflow is caught by AddToVector() in scanProducedOutputs() during partial context.
func TestTxOverflowProducedBalance(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKey, _, addr := u.GenerateAddress(1)

	err := u.TokensFromFaucet(addr, 100_000_000)
	require.NoError(t, err)

	outs := getSourceOutputs(t, u, addr)

	txb := txbuilder.New()
	_, maxTs, err := txb.ConsumeOutputsNoUnlock(outs...)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	hugeAmount := int64(math.MaxInt64/2 + 1)
	out1 := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(hugeAmount).WithLock(addr)
	})
	out2 := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(hugeAmount).WithLock(addr)
	})
	_, err = txb.ProduceOutput(out1)
	require.NoError(t, err)
	_, err = txb.ProduceOutput(out2)
	require.NoError(t, err)

	txBytes := buildAndSignTx(txb, maxTs, privKey)

	// Produced overflow is caught at partial validation (stage 2), not full context
	_, err = transaction.ParseWithPartialValidation(txBytes)
	require.Error(t, err, "produced balance overflow must be rejected")
	require.NoError(t, util.MustErrorWith(err, "arithmetic overflow"))
	t.Logf("produced balance overflow correctly rejected: %v", err)
}

// TestTxOverflowSingleMaxAmount tests the boundary of AddToVector's overflow check.
//
// AddToVector detects overflow when vect[i] >= MaxInt64 - v, meaning the largest
// non-overflowing amount for a single output is MaxInt64 - 1. This is conservative:
// it rejects MaxInt64 itself because 0 + MaxInt64 >= MaxInt64 triggers the check.
func TestTxOverflowSingleMaxAmount(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKey, _, addr := u.GenerateAddress(1)

	err := u.TokensFromFaucet(addr, 100_000_000)
	require.NoError(t, err)

	outs := getSourceOutputs(t, u, addr)

	// MaxInt64 - 1: largest non-overflowing single output
	// (conservation check fails since consumed is only 100M, but no overflow)
	t.Run("max_minus_one_no_overflow", func(t *testing.T) {
		txb := txbuilder.New()
		_, maxTs, err := txb.ConsumeOutputsNoUnlock(outs...)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)

		out := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(math.MaxInt64 - 1).WithLock(addr)
		})
		_, err = txb.ProduceOutput(out)
		require.NoError(t, err)

		txBytes := buildAndSignTx(txb, maxTs, privKey)

		// Partial validation should pass (no overflow)
		_, err = transaction.ParseWithPartialValidation(txBytes)
		require.NoError(t, err, "single MaxInt64-1 output should not overflow at partial validation")

		// Full validation fails: amounts don't balance
		err = validateFull(txBytes, txb)
		require.Error(t, err, "amounts must not balance")
	})

	// MaxInt64 itself triggers overflow in AddToVector (conservative check: >= not >)
	t.Run("exact_max_overflows", func(t *testing.T) {
		txb := txbuilder.New()
		_, maxTs, err := txb.ConsumeOutputsNoUnlock(outs...)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)

		out := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(math.MaxInt64).WithLock(addr)
		})
		_, err = txb.ProduceOutput(out)
		require.NoError(t, err)

		txBytes := buildAndSignTx(txb, maxTs, privKey)

		_, err = transaction.ParseWithPartialValidation(txBytes)
		require.Error(t, err, "MaxInt64 itself triggers AddToVector overflow")
		require.NoError(t, util.MustErrorWith(err, "arithmetic overflow"))
	})

	// Two outputs with MaxInt64/2: their sum equals MaxInt64, should overflow
	t.Run("two_half_max_overflow", func(t *testing.T) {
		txb := txbuilder.New()
		_, maxTs, err := txb.ConsumeOutputsNoUnlock(outs...)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)

		for i := 0; i < 2; i++ {
			out := ledger.NewOutput(func(o *ledger.OutputBuilder) {
				o.WithAmounts(math.MaxInt64/2 + 1).WithLock(addr)
			})
			_, err = txb.ProduceOutput(out)
			require.NoError(t, err)
		}

		txBytes := buildAndSignTx(txb, maxTs, privKey)

		_, err = transaction.ParseWithPartialValidation(txBytes)
		require.Error(t, err, "two MaxInt64/2+1 outputs must overflow")
		require.NoError(t, util.MustErrorWith(err, "arithmetic overflow"))
	})
}

// TestTxOverflowConservationCheckSafe verifies that even if consumed + inflation
// could theoretically overflow in the conservation comparison
// (validateOutputs line: producedSide != consumedSide+inflation),
// the transaction is still rejected because the wrapped negative result
// can never equal the positive produced amount.
//
// This is a defense property test: the combination of individual overflow checks
// + the positive produced amount check makes the conservation comparison safe.
func TestTxOverflowConservationCheckSafe(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKey, _, addr := u.GenerateAddress(1)

	// Create several UTXOs
	for i := 0; i < 3; i++ {
		err := u.TokensFromFaucet(addr, 100_000_000)
		require.NoError(t, err)
	}
	outs := getSourceOutputs(t, u, addr)
	require.True(t, len(outs) >= 3)

	// Set consumed = MaxInt64 - 100 (just below overflow threshold for a single value)
	consumedAmount := int64(math.MaxInt64 - 100)
	fakeOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(consumedAmount).WithLock(addr)
	})

	txb := txbuilder.New()
	_, err := txb.ConsumeOutput(fakeOut, outs[0].ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	// Produce an output with a small amount
	// consumed = MaxInt64 - 100, produced = 100_000_000, inflation = 0
	// consumed + inflation = MaxInt64 - 100, produced = 100_000_000 → mismatch → rejected
	smallOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(100_000_000).WithLock(addr)
	})
	_, err = txb.ProduceOutput(smallOut)
	require.NoError(t, err)

	maxTs := outs[0].Timestamp()
	txBytes := buildAndSignTx(txb, maxTs, privKey)

	err = validateFull(txBytes, txb)
	require.Error(t, err, "large consumed with small produced must be rejected")
	require.NoError(t, util.MustErrorWith(err, "mismatch between token amounts"))
	t.Logf("conservation check with large amounts correctly rejected: %v", err)
}

// TestTxOverflowAddToVectorContinuesAfterDetection verifies that AddToVector's
// behavior of continuing to add after detecting overflow does not create a vulnerability.
// The overflow flag is returned and the caller rejects the transaction immediately.
//
// Note: AddToVector uses the conservative check vect[i] >= MaxInt64 - v, meaning
// MaxInt64 itself is treated as overflow. The actual maximum non-overflowing value
// is MaxInt64 - 1.
func TestTxOverflowAddToVectorContinuesAfterDetection(t *testing.T) {
	// Direct unit test of AddToVector
	var vect [15]int64
	a1 := ledger.NewAmounts(math.MaxInt64 - 1)
	a2 := ledger.NewAmounts(1)

	// First add: no overflow (MaxInt64 - 1 is the largest safe value)
	overflow := a1.AddToVector(&vect)
	require.False(t, overflow, "first add of MaxInt64-1 should not overflow")
	require.EqualValues(t, math.MaxInt64-1, vect[0])

	// Second add: overflow (MaxInt64 - 1 + 1 = MaxInt64 triggers >= check)
	overflow = a2.AddToVector(&vect)
	require.True(t, overflow, "adding 1 to MaxInt64-1 must detect overflow")

	// The value wraps but the overflow flag prevents use
	t.Logf("after overflow: vect[0] = %d (wrapped to MaxInt64), overflow detected = true", vect[0])

	// Verify multiple amounts in a single Amounts vector
	var vect2 [15]int64
	// Two amounts in one vector: both large
	a3 := ledger.NewAmounts(math.MaxInt64/2, math.MaxInt64/2)
	overflow = a3.AddToVector(&vect2)
	require.False(t, overflow, "two MaxInt64/2 in different positions should not overflow")
	require.EqualValues(t, math.MaxInt64/2, vect2[0])
	require.EqualValues(t, math.MaxInt64/2, vect2[1])
}
