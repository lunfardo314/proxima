package tests

import (
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
)

// TestNativeTokenCounterfeitViaHiddenTokenAmountRejected is the regression for
// the native-token counterfeit finding (audit FECON-4).
//
// Native-token amounts were once summed as a side effect of EVALUATING a
// tokenAmount(tag, amount) constraint. EasyFL's if(...) is lazy, so a
// tokenAmount nested in `if(selfIsConsumedOutput, tokenAmount(T, x), 0x01)`
// never fired when its output was PRODUCED but did fire when the same output was
// later CONSUMED, crediting x to the consumed side out of nothing. The spender
// declared token(T, 0xff) (accepted for any tag, no foundry) and produced a real
// tokenAmount(T, x): produced == consumed, balance held, x units conjured.
//
// tokenAmount is now a pure EasyFL predicate and the summation is a structural
// scan that counts only TOP-LEVEL tokenAmount constraints. A nested tokenAmount
// is not top-level, so the consumed side stays 0 and the closing balance fails.
func TestNativeTokenCounterfeitViaHiddenTokenAmountRejected(t *testing.T) {
	const initAmount = 10_000_000_000
	u, privKey, srcAddr := newTestEnv(t, initAmount)

	lib := ledger.L(base.MaxSlot)

	// arbitrary tag: no real foundry needed — token(T, 0xFF) declares any tag.
	var tag base.ChainID
	for i := range tag {
		tag[i] = 0x7C
	}
	const counterfeit = uint64(1_000_000)

	// truthy opaque lock so the carrier output is freely consumable.
	_, _, generalLock, err := lib.CompileExpression("equal(u64/1, u64/1)")
	require.NoError(t, err)

	// the hidden constraint: it fires only on the consumed side.
	hiddenSrc := fmt.Sprintf("if(selfIsConsumedOutput, tokenAmount(0x%s, z64/%d), 0x01)",
		hex.EncodeToString(tag[:]), counterfeit)
	_, _, hidden, err := lib.CompileExpression(hiddenSrc)
	require.NoError(t, err)

	// --- tx1: park an output carrying the hidden tokenAmount constraint ---
	outsData, err := u.StateReader().GetUTXOsForController(srcAddr.ControllerID())
	require.NoError(t, err)
	outs, err := ledger.ParseAndSortOutputData(outsData, func(oid *base.OutputID, o *ledger.Output) bool {
		return o.ChainConstraint() == nil && o.Lock().Name() == ledger.SigLockName
	})
	require.NoError(t, err)
	require.True(t, len(outs) > 0)

	txb1 := exhelp.New()
	total1, maxTs1, err := txb1.ConsumeOutputsNoUnlock(outs...)
	require.NoError(t, err)
	for i := range outs {
		if i == 0 {
			txb1.PutSignatureUnlock(0)
		} else {
			require.NoError(t, txb1.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0))
		}
	}
	const carrierAmount = 500_000_000
	carrier := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(carrierAmount))
		o.PutConstraint(generalLock, ledger.ConstraintIndexLock)
		// hidden constraint at the first extra (post-lock) slot.
		o.PutConstraint(hidden, ledger.ConstraintIndexChain)
	})
	carrierIdx, err := txb1.ProduceOutput(carrier)
	require.NoError(t, err)
	rem1 := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(total1 - carrierAmount)).WithLock(srcAddr)
	})
	_, err = txb1.ProduceOutput(rem1)
	require.NoError(t, err)

	txb1.SetTimestamp(maxTs1.AddTicks(int(lib.TransactionPace)))
	txb1.ComputeInputCommitment()
	txb1.SignED25519(privKey)

	// tx1 must validate: on the PRODUCED side the hidden if(...) returns 0x01 and
	// tokenAmount is never evaluated; no token() declaration is needed.
	require.NoError(t, u.AddTransaction(txb1.Bytes(), func(_ *transaction.Transaction, e error) error { return e }),
		"tx1 (parking the carrier) must validate")

	tx1, err := transaction.Parse(txb1.Bytes())
	require.NoError(t, err)
	carrierOID := base.MustNewOutputID(tx1.ID(), carrierIdx)

	// --- tx2: try to mint `counterfeit` units of T from nothing ---
	txb2 := exhelp.New()
	_, err = txb2.ConsumeOutput(carrier, carrierOID)
	require.NoError(t, err)
	txb2.PutSignatureUnlock(0) // generalLock ignores it; the tx still carries a signature

	minted := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(carrierAmount).WithLock(srcAddr).WithTokenAmount(tag, counterfeit)
	})
	require.NoError(t, minted.EnoughAmountForStorageDeposit())
	_, err = txb2.ProduceOutput(minted)
	require.NoError(t, err)

	// declare the tag (pure-conservation sentinel form) so tokenAmount is admissible.
	txb2.DeclareTokenConservation(tag)

	ts2 := carrierOID.Timestamp().AddSlots(1)
	if ts2.IsSlotBoundary() {
		ts2 = ts2.AddTicks(1)
	}
	txb2.SetTimestamp(ts2)
	txb2.ComputeInputCommitment()
	txb2.SignED25519(privKey)

	// The hidden consumed-side tokenAmount is not a top-level constraint, so it is
	// not counted: consumed sum 0, produced sum `counterfeit`, balance fails.
	err = u.AddTransaction(txb2.Bytes(), func(_ *transaction.Transaction, e error) error { return e })
	require.Error(t, err, "counterfeit tx2 must be rejected")
	require.NoError(t, util.MustErrorWith(err, "native token balance"),
		"expected a native-token balance rejection, got: %v", err)
}
