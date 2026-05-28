// Foundry delegation ban via an inline sigLock-controller guard.
//
// `proxi node foundry create` (without --allow_delegation) appends an inline
// guard script after foundry(). The guard self-locks at its own position
// across every transit and requires the controller lock (lockConstraintIndex)
// to stay a sigLock on every produced foundry output. Swapping the controller
// to a non-sigLock (e.g. a delegateLock — i.e. delegating the foundry) is
// rejected; changing it to a DIFFERENT sigLock is still allowed.
//
// Nothing in the ledger library is modified — the guard is composed entirely
// from existing library symbols and compiled at foundry-creation time. These
// tests compile the same source and exercise it through real transitions.
//
// The end-to-end "delegate a guarded foundry is rejected" path (the actual
// `proxi node dlg chain` build) is verified live against a standalone node;
// here we prove the guard mechanics directly: any non-sigLock controller is
// rejected (a delegateLock is one such non-sigLock), while a sigLock→sigLock
// controller change passes.

package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil/txbtest"
	"github.com/stretchr/testify/require"
)

// foundrySigLockGuardSource MUST stay byte-identical to the constant of the
// same name in proxi/node_cmd/foundry/create.go.
const foundrySigLockGuardSource = "and(selfImmutableOnSuccessorIndex(selfBlockIndex),or(not(selfIsProducedOutput),require(equal(parseBytecode(selfSiblingConstraint(lockConstraintIndex),0x),#sigLock),!!!foundry_expects_siglock)))"

func compileFoundryGuard(t *testing.T) []byte {
	t.Helper()
	_, _, code, err := ledger.L(base.MaxSlot).CompileExpression(foundrySigLockGuardSource)
	require.NoError(t, err)
	return code
}

// createGuardedFoundryOrigin builds and submits a foundry origin with the
// sigLock-controller guard appended after foundry() (index 5, since no
// predefined policy is attached). Returns the future chain ID.
func (e *foundryTestEnv) createGuardedFoundryOrigin(t *testing.T, onChainAmount uint64) base.ChainID {
	t.Helper()
	guard := compileFoundryGuard(t)

	outs := getSourceOutputs(t, e.u, e.addr)
	ts := outs[0].ID.Timestamp().AddSlots(1)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}
	txb := exhelp.New()
	_, inTs, err := txb.ConsumeOutputsNoUnlock(outs...)
	require.NoError(t, err)
	ts = base.MaximumTime(inTs, ts)
	for i := range outs {
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			require.NoError(t, txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0))
		}
	}

	foundryOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(onChainAmount)).WithLock(e.addr)
		o.PutConstraint(ledger.NewChainOrigin(ts.Slot).Bytes(), ledger.ConstraintIndexChain)
		o.PutConstraint(ledger.NewFoundry(0).Bytes(), ledger.ConstraintIndexFoundry)
		o.MustPushConstraint(guard) // appended after foundry() — index 5
	})
	require.NoError(t, foundryOut.EnoughAmountForStorageDeposit())
	foundryIdx, err := txb.ProduceOutput(foundryOut)
	require.NoError(t, err)
	addRemainderIfNeeded(t, txb, e.addr)

	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(e.privKey)

	txBytes, txid, failedTx, err := txbtest.BuildAndValidate(txb)
	require.NoError(t, err, "guarded foundry-origin build/validation failed: %s", failedTx)
	require.NoError(t, e.u.AddTransaction(txBytes))

	foundryOid, err := base.NewOutputID(txid, foundryIdx)
	require.NoError(t, err)
	return base.MakeOriginChainID(foundryOid)
}

// transitFoundryControllerTo consumes the guarded foundry chain output and
// produces a successor whose controller lock (index 2) is `newController`.
// The foundry constraint at index 4 and the guard at index 5 carry over
// byte-equal. Returns the validation error (nil on success).
func (e *foundryTestEnv) transitFoundryControllerTo(t *testing.T, chainID base.ChainID, newController ledger.Lock) (string, error) {
	t.Helper()
	fIn := e.foundryInputData(t, chainID)
	chainIN, err := ledger.OutputFromBytesWithLib(fIn.Data, ledger.L(fIn.ID.Slot()))
	require.NoError(t, err)
	cc := chainIN.ChainConstraint()
	require.NotNil(t, cc)

	txb := exhelp.New()
	predIdx, err := txb.ConsumeOutput(chainIN, fIn.ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(predIdx)

	successorCC := ledger.NewChainConstraint(
		fIn.ChainID, predIdx, cc.OriginSlot,
		cc.CumulativeChainInflation, cc.CumulativeBranchBonus,
		cc.TransitionCounter+1, cc.BranchCounter,
	)
	// Clone preserves foundry (index 4) and the guard (index 5) byte-equal;
	// only the controller (index 1 index-values + index 2 lock) and the chain
	// constraint (index 3) change.
	succ := chainIN.Clone(func(o *ledger.OutputBuilder) {
		o.WithLock(newController)
		o.PutConstraint(successorCC.Bytes(), ledger.ConstraintIndexChain)
	})
	prodIdx, err := txb.ProduceOutput(succ)
	require.NoError(t, err)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(prodIdx))

	ts := fIn.ID.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(e.privKey)

	_, _, failedTx, err := txbtest.BuildAndValidate(txb)
	return failedTx, err
}

// The guard must allow changing the foundry controller to a DIFFERENT sigLock
// (only the lock kind is checked, not the holder). This is the explicit
// guarantee requested.
func TestFoundryGuardAllowsControllerChangeToAnotherSigLock(t *testing.T) {
	e := newFoundryTestEnv(t, 10_000_000_000)
	chainID := e.createGuardedFoundryOrigin(t, 500_000_000)

	_, _, addr2 := e.u.GenerateAddress(2) // a different sigLock holder
	require.NotEqualValues(t, e.addr, addr2)

	failedTx, err := e.transitFoundryControllerTo(t, chainID, addr2)
	require.NoError(t, err, "changing the foundry controller to another sigLock must be allowed: %s", failedTx)
}

// The guard must reject swapping the foundry controller to any non-sigLock.
// A chainLock stands in for the general non-sigLock case; a delegateLock (used
// when delegating a foundry) is likewise a non-sigLock and is rejected by the
// same prefix check, which is what bans foundry delegation by default.
func TestFoundryGuardRejectsNonSigLockController(t *testing.T) {
	e := newFoundryTestEnv(t, 10_000_000_000)
	chainID := e.createGuardedFoundryOrigin(t, 500_000_000)

	notSigLock := ledger.ChainLockFromChainID(chainID) // any non-sigLock controller

	_, err := e.transitFoundryControllerTo(t, chainID, notSigLock)
	require.Error(t, err, "swapping the foundry controller to a non-sigLock must be rejected")
	// '!!!foundry_expects_siglock' surfaces with spaces at runtime.
	require.NoError(t, util.MustErrorWith(err, "foundry expects siglock"))
}
