package tests

import (
	"crypto/ed25519"
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/stretchr/testify/require"
)

// Frozen coverage is a chain-only quantity: it exists to let a sequencer output
// carry the coverage of the real tokens frozen by the delegations targeting it,
// and it feeds consensus (ledger.Coverage adds frozenCoverage[0] for a consumed
// sequencer output, so it enters the branch coverage that decides the LRB).
//
// The invariant that keeps that coverage honest is "a produced output that is
// not a chain output must carry ZERO frozen coverage (and zero inflation)".
// If an arbitrary produced output could carry frozen coverage, a sequencer
// could satisfy the frozen-coverage conservation equation on its own output
// (2*succ = sum_over_all_produced + pred) with coverage placed on a junk
// output instead of on real-token-backed delegation outputs — manufacturing
// ledger coverage, i.e. consensus weight, without holding the tokens.
//
// The rule used to be enforced only inside the seven standard locks (each
// AND-ing a zero-amounts check into its own body). Slot 2 also admits arbitrary
// EasyFL bytecode as an opaque "general lock" (see constraints_serde.go /
// utxo_indexing.md §4), and a general lock has no obligation to run that check —
// so it bypassed the rule. Enforcement now lives once in validateOutputs, over
// every produced output. These tests build exactly that output and require the
// transaction to be rejected.

// buildFrozenCoverageOutputTx builds an otherwise valid transfer transaction in
// which the first produced output carries non-zero frozen coverage while being
// a plain (non-chained) output. applyLock writes the lock onto that output
// (slot 2, plus the slot-1 index values if the lock needs them). Returns the
// validation error (nil if accepted).
func buildFrozenCoverageOutputTx(
	t *testing.T,
	u *utxodb.UTXODB,
	srcPrivKey ed25519.PrivateKey,
	srcAddr ledger.SigLock,
	applyLock func(o *ledger.OutputBuilder),
	frozenCoverage int64,
) error {
	t.Helper()

	outsData, err := u.StateReader().GetUTXOsForController(srcAddr.ControllerID())
	require.NoError(t, err)
	outs, err := ledger.ParseAndSortOutputData(outsData, func(oid *base.OutputID, o *ledger.Output) bool {
		return o.ChainConstraint() == nil && o.Lock().Name() == ledger.SigLockName
	})
	require.NoError(t, err)
	require.True(t, len(outs) > 0, "source address must have UTXOs")

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

	const badAmount = 100_000_000 // well above the storage-deposit floor
	require.True(t, total > badAmount, "not enough funds")

	// The malicious output: token balance in slot 0 and a non-zero
	// frozen-coverage cell (epoch 0), under a lock supplied by the caller.
	// No chain constraint, so nothing else on the output governs its coverage.
	badOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		// amounts = (tokenBalance, inflation=0, bound(auto), frozenCoverage[0])
		o.WithAmounts(int64(badAmount), 0, 0, frozenCoverage)
		applyLock(o)
	})
	_, err = txb.ProduceOutput(badOut)
	require.NoError(t, err)

	// Remainder back to the source so token balances conserve.
	remainderOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(total - badAmount)).WithLock(srcAddr)
	})
	_, err = txb.ProduceOutput(remainderOut)
	require.NoError(t, err)

	lib := ledger.L(maxTs.Slot)
	txb.SetTimestamp(maxTs.AddTicks(int(lib.TransactionPace)))
	txb.ComputeInputCommitment()
	txb.SignED25519(srcPrivKey)

	return u.AddTransaction(txb.Bytes(), func(_ *transaction.Transaction, e error) error { return e })
}

// TestFrozenCoverageBypassGeneralLock is the core proof: a produced,
// non-chained output whose slot-2 lock is arbitrary bytecode (a "general lock",
// here the truthy non-constraint expression equal(1,1)) must NOT be allowed to
// carry frozen coverage. Before the tx-level enforcement this transaction
// validated and settled — that is the consensus-weight forgery.
func TestFrozenCoverageBypassGeneralLock(t *testing.T) {
	const initAmount = 1_000_000_000
	u, privKey, srcAddr := newTestEnv(t, initAmount)

	// A general lock: any expression whose top-level function is not a
	// registered constraint. equal(1,1) evaluates to a truthy value, so the
	// lock "passes" on the produced side, yet it is opaque to
	// NameByPrefixWithLib and never enforces zero amounts.
	_, _, generalLock, err := ledger.L(base.MaxSlot).CompileExpression("equal(u64/1, u64/1)")
	require.NoError(t, err)
	// sanity: this really is an unregistered (opaque) lock, not a known one
	parsed, err := ledger.LockFromOutputElements(nil, generalLock)
	require.NoError(t, err)
	require.Contains(t, parsed.Name(), "generalLock")

	applyGeneralLock := func(o *ledger.OutputBuilder) { o.PutConstraint(generalLock, ledger.ConstraintIndexLock) }
	err = buildFrozenCoverageOutputTx(t, u, privKey, srcAddr, applyGeneralLock, 10_000_000_000_000)
	require.Error(t, err, "a non-chained output with a general lock must not be allowed to carry frozen coverage")
	require.Contains(t, err.Error(), "frozen coverage")
}

// TestFrozenCoverageRejectedSigLock is the control: the same non-zero frozen
// coverage on an ordinary sigLock output is rejected too. It was already
// rejected by the per-lock rule and stays rejected once enforcement moves to
// the tx level, so the two paths now behave identically.
func TestFrozenCoverageRejectedSigLock(t *testing.T) {
	const initAmount = 1_000_000_000
	u, privKey, srcAddr := newTestEnv(t, initAmount)

	applySigLock := func(o *ledger.OutputBuilder) { o.WithLock(srcAddr) }
	err := buildFrozenCoverageOutputTx(t, u, privKey, srcAddr, applySigLock, 10_000_000_000_000)
	require.Error(t, err, "a non-chained sigLock output must not carry frozen coverage")
	require.Contains(t, err.Error(), "frozen coverage")
}

// TestPlainTransferStillValid guards against over-rejection: an ordinary
// transfer with zero frozen coverage under a general lock still validates.
func TestPlainTransferStillValid(t *testing.T) {
	const initAmount = 1_000_000_000
	u, privKey, srcAddr := newTestEnv(t, initAmount)

	_, _, generalLock, err := ledger.L(base.MaxSlot).CompileExpression("equal(u64/1, u64/1)")
	require.NoError(t, err)

	applyGeneralLock := func(o *ledger.OutputBuilder) { o.PutConstraint(generalLock, ledger.ConstraintIndexLock) }
	err = buildFrozenCoverageOutputTx(t, u, privKey, srcAddr, applyGeneralLock, 0)
	require.NoError(t, err, "zero frozen coverage on a general-lock output must remain valid")
}
