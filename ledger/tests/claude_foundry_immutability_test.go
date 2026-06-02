// Wave 4: foundry constraint position immutability.
//
// Once a chain output carries a foundry constraint at slot 4 at origin,
// the foundry constraint cannot be dropped or moved off slot 4 for the
// lifetime of the chain. The supply arg itself is allowed to change
// (mint/burn legitimately move it), so the in-EasyFL self-lock checks
// the SYMBOL only (#foundry), not byte-equality.
//
// What we exercise here:
//   1. successor drops the foundry constraint  → rejected
//   2. successor moves foundry to a different slot → rejected
//   3. foundry origin produced at a non-foundryConstraintIndex slot → rejected
//
// Delegating a foundry chain (lock swap + delegateLockState append, foundry
// preserved at slot 4) is the canonical "still allowed" path; it is
// covered by TestDelegateFoundryChainNoPolicy / NonDestructible in
// claude_delegation_2_test.go and we deliberately do not duplicate it.

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

// transitFoundryDropping drops the foundry constraint on the successor
// (PutConstraint with empty bytecode at the same slot). Should be
// rejected by foundry()'s self-lock — parseBytecode panics because the
// successor's slot at foundryConstraintIndex isn't a foundry call.
func TestFoundryConstraintCannotBeDroppedOnTransit(t *testing.T) {
	e := newFoundryTestEnv(t, 10_000_000_000)
	chainID := e.createFoundryOrigin(t, 500_000_000, nil)

	fIn := e.foundryInputData(t, chainID)
	chainIN, err := ledger.OutputFromBytesWithLib(fIn.Data, ledger.L(fIn.ID.Slot()))
	require.NoError(t, err)
	cc := chainIN.ChainConstraint()
	require.NotNil(t, cc)

	txb := exhelp.New()
	predIdx, err := txb.ConsumeOutput(chainIN, fIn.ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(predIdx)

	successor := ledger.NewChainConstraint(
		fIn.ChainID, predIdx, cc.OriginSlot,
		cc.CumulativeChainInflation, cc.CumulativeBranchBonus,
		cc.TransitionCounter+1, cc.BranchCounter,
	)
	// Build a successor that intentionally drops the foundry: replace
	// slot 4 with the empty bytecode placeholder.
	chainOut := chainIN.Clone(func(o *ledger.OutputBuilder) {
		o.PutConstraint(successor.Bytes(), ledger.ConstraintIndexChain)
		o.PutConstraint(nil, ledger.ConstraintIndexFoundry)
	})
	prodIdx, err := txb.ProduceOutput(chainOut)
	require.NoError(t, err)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(prodIdx))

	ts := fIn.ID.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(e.privKey)

	_, _, _, err = txbtest.BuildAndValidate(txb)
	require.Error(t, err, "dropping foundry constraint on transit must be rejected")
	// Successor's slot 4 is empty bytecode → parseBytecode panics with
	// "unexpected EOF" before it can even check the call prefix. Either
	// EOF or a prefix mismatch is an acceptable rejection; both flow
	// through evalParseBytecode.
	require.NoError(t, util.MustErrorWith(err, "evalParseBytecode"))
}

// The successor carries a non-foundry call at slot 4 (an amounts
// constraint as a stand-in). parseBytecode in the consumed-side foundry
// check rejects it with "unexpected call prefix 'amounts'". This makes
// the symbol-check failure mode distinct from the empty-bytecode case
// covered by TestFoundryConstraintCannotBeDroppedOnTransit.
func TestFoundryConstraintCannotBeReplacedByOtherConstraint(t *testing.T) {
	e := newFoundryTestEnv(t, 10_000_000_000)
	chainID := e.createFoundryOrigin(t, 500_000_000, nil)

	fIn := e.foundryInputData(t, chainID)
	chainIN, err := ledger.OutputFromBytesWithLib(fIn.Data, ledger.L(fIn.ID.Slot()))
	require.NoError(t, err)
	cc := chainIN.ChainConstraint()
	require.NotNil(t, cc)

	txb := exhelp.New()
	predIdx, err := txb.ConsumeOutput(chainIN, fIn.ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(predIdx)

	successor := ledger.NewChainConstraint(
		fIn.ChainID, predIdx, cc.OriginSlot,
		cc.CumulativeChainInflation, cc.CumulativeBranchBonus,
		cc.TransitionCounter+1, cc.BranchCounter,
	)
	// Replace slot 4 with a real EasyFL function call that isn't a
	// foundry — a chain-origin bytecode works (prefix == #chain).
	// parseBytecode then asserts the prefix matches #foundry → fails
	// with "unexpected call prefix 'chain'".
	notFoundry := ledger.NewChainOrigin(cc.OriginSlot).Bytes()
	chainOut := chainIN.Clone(func(o *ledger.OutputBuilder) {
		o.PutConstraint(successor.Bytes(), ledger.ConstraintIndexChain)
		o.PutConstraint(notFoundry, ledger.ConstraintIndexFoundry)
	})
	prodIdx, err := txb.ProduceOutput(chainOut)
	require.NoError(t, err)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(prodIdx))

	ts := fIn.ID.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(e.privKey)

	_, _, _, err = txbtest.BuildAndValidate(txb)
	require.Error(t, err, "replacing foundry with another call at slot 4 must be rejected")
	require.NoError(t, util.MustErrorWith(err, "unexpected call prefix"))
}

// Creating a foundry origin output whose foundry constraint sits at the
// wrong slot must be rejected: foundry() on the produced side requires
// `selfBlockIndex == foundryConstraintIndex`.
func TestFoundryOriginAtWrongSlotRejected(t *testing.T) {
	e := newFoundryTestEnv(t, 10_000_000_000)
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

	// Manually build a foundry-bearing origin output but place foundry
	// at slot 5 (foundryPolicyConstraintIndex) while slot 4 is empty.
	const foundryOnChain = uint64(500_000_000)
	badOrigin := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(foundryOnChain)).WithLock(e.addr)
		o.PutConstraint(ledger.NewChainOrigin(ts.Slot).Bytes(), ledger.ConstraintIndexChain)
		o.PutConstraint(nil, ledger.ConstraintIndexFoundry) // slot 4 empty
		o.PutConstraint(ledger.NewFoundry(0).Bytes(), ledger.ConstraintIndexFoundryPolicy)
	})
	_, err = txb.ProduceOutput(badOrigin)
	require.NoError(t, err)
	addRemainderIfNeeded(t, txb, e.addr)

	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(e.privKey)

	_, _, _, err = txbtest.BuildAndValidate(txb)
	require.Error(t, err, "foundry origin at wrong slot must be rejected")
	require.NoError(t, util.MustErrorWith(err, "foundry must be at foundryConstraintIndex"))
}
