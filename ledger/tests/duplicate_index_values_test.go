package tests

import (
	"strings"
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/stretchr/testify/require"
)

// An output's slot-1 index-value tuple is "pure data, never evaluated" — no
// lock constraint checks it (see utxo_indexing.md §4). The state indexer,
// however, inserts one trie account record per non-empty index-value entry and
// treats a pre-existing key as an error ("addOutputToTrie: index key should not
// exist"). An output carrying two IDENTICAL index-value entries used to pass
// full validation and fail only at the state mutation — which, on a live node,
// runs at BRANCH COMMIT, whose error path calls GracefulShutdown -> Stop(). The
// branch is part of consensus, so every committing node halted and re-hit the
// same branch on restart: a single cheap, fully-valid transaction was a
// network-wide DoS.
//
// validateOutputs now rejects duplicate non-empty index-value entries, so the
// transaction is refused up front and never reaches state.
func TestDuplicateIndexValuesRejectedByValidation(t *testing.T) {
	const initAmount = 1_000_000_000
	u, privKey, srcAddr := newTestEnv(t, initAmount)

	// truthy, opaque (non-constraint) lock: slot 2 validates without any
	// per-lock rule constraining slot 1.
	_, _, generalLock, err := ledger.L(base.MaxSlot).CompileExpression("equal(u64/1, u64/1)")
	require.NoError(t, err)

	h := make([]byte, 32) // one 32-byte index value, repeated
	for i := range h {
		h[i] = 0xAB
	}

	outsData, err := u.StateReader().GetUTXOsForController(srcAddr.ControllerID())
	require.NoError(t, err)
	outs, err := ledger.ParseAndSortOutputData(outsData, func(oid *base.OutputID, o *ledger.Output) bool {
		return o.ChainConstraint() == nil && o.Lock().Name() == ledger.SigLockName
	})
	require.NoError(t, err)
	require.True(t, len(outs) > 0)

	txb := exhelp.New()
	total, maxTs, err := txb.ConsumeOutputsNoUnlock(outs...)
	require.NoError(t, err)
	for i := range outs {
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			require.NoError(t, txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0))
		}
	}

	const badAmount = 100_000_000
	// malicious output: general lock + slot-1 tuple with two identical entries.
	badOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(badAmount))
		o.PutConstraint(generalLock, ledger.ConstraintIndexLock)
		o.PutConstraint(ledger.IndexValuesTupleBytes([][]byte{h, h}), ledger.ConstraintIndexIndexValues)
	})
	_, err = txb.ProduceOutput(badOut)
	require.NoError(t, err)

	remainderOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(total - badAmount)).WithLock(srcAddr)
	})
	_, err = txb.ProduceOutput(remainderOut)
	require.NoError(t, err)

	lib := ledger.L(maxTs.Slot)
	txb.SetTimestamp(maxTs.AddTicks(int(lib.TransactionPace)))
	txb.ComputeInputCommitment()
	txb.SignED25519(privKey)

	// The transaction must be rejected by validation, BEFORE any state mutation:
	// the error must name the duplicate index-values and must NOT be the
	// state-indexer error (which would mean it slipped through to branch commit).
	err = u.AddTransaction(txb.Bytes(), func(_ *transaction.Transaction, e error) error { return e })
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "duplicate index-value entries"),
		"expected a validation rejection, got: %v", err)
	require.False(t, strings.Contains(err.Error(), "addOutputToTrie"),
		"must be rejected at validation, not at state mutation; got: %v", err)
}

// TestDuplicateEmptyIndexValuesAccepted guards the empty-skip: the indexer
// ignores empty index-value entries, so two empty entries never collide and
// must NOT be rejected by validation (only non-empty duplicates are).
func TestDuplicateEmptyIndexValuesAccepted(t *testing.T) {
	const initAmount = 1_000_000_000
	u, privKey, srcAddr := newTestEnv(t, initAmount)

	_, _, generalLock, err := ledger.L(base.MaxSlot).CompileExpression("equal(u64/1, u64/1)")
	require.NoError(t, err)

	outsData, err := u.StateReader().GetUTXOsForController(srcAddr.ControllerID())
	require.NoError(t, err)
	outs, err := ledger.ParseAndSortOutputData(outsData, func(oid *base.OutputID, o *ledger.Output) bool {
		return o.ChainConstraint() == nil && o.Lock().Name() == ledger.SigLockName
	})
	require.NoError(t, err)
	require.True(t, len(outs) > 0)

	txb := exhelp.New()
	total, maxTs, err := txb.ConsumeOutputsNoUnlock(outs...)
	require.NoError(t, err)
	for i := range outs {
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			require.NoError(t, txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0))
		}
	}

	const badAmount = 100_000_000
	// slot-1 tuple: a real 32-byte value plus two EMPTY entries. The empties are
	// skipped by the indexer, so this is not a collision.
	h := make([]byte, 32)
	out := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(badAmount))
		o.PutConstraint(generalLock, ledger.ConstraintIndexLock)
		o.PutConstraint(ledger.IndexValuesTupleBytes([][]byte{h, {}, {}}), ledger.ConstraintIndexIndexValues)
	})
	_, err = txb.ProduceOutput(out)
	require.NoError(t, err)
	rem := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(total - badAmount)).WithLock(srcAddr)
	})
	_, err = txb.ProduceOutput(rem)
	require.NoError(t, err)

	lib := ledger.L(maxTs.Slot)
	txb.SetTimestamp(maxTs.AddTicks(int(lib.TransactionPace)))
	txb.ComputeInputCommitment()
	txb.SignED25519(privKey)

	err = u.AddTransaction(txb.Bytes(), func(_ *transaction.Transaction, e error) error { return e })
	require.NoError(t, err, "duplicate EMPTY index-value entries are skipped by the indexer and must stay valid")
}
