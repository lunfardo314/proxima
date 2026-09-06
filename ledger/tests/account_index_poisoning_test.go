package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/stretchr/testify/require"
)

// TestAccountIndexPoisoningFilteredOnRead is the read-side regression for the
// account-index poisoning finding (audit FSTATE-1, general case).
//
// The slot-1 index-value tuple is free-form data any producer can fill with
// arbitrary values, including another account's controller ID. Validation only
// forbids reserved values (StemAccountID) and duplicates, so an attacker CAN
// produce an output it controls that also names a victim's controller ID as an
// extra index value. Such an output is then indexed under the victim's account.
//
// The account-index readers must therefore verify ownership: an output belongs
// to an account only if the account is among the index values the output's LOCK
// vouches for, not merely present in the raw tuple. This test plants a poison
// output and asserts the victim does not see it and is not made "known" by it
// (the latter feeds the unknown-sender spam gate), while the real owner does.
func TestAccountIndexPoisoningFilteredOnRead(t *testing.T) {
	const initAmount = 1_000_000_000
	u, privKey, srcAddr := newTestEnv(t, initAmount)

	// victim: a distinct sig-lock account that owns nothing.
	_, _, victimAddr := u.GenerateAddress(2)
	require.EqualValues(t, 0, u.Balance(victimAddr))
	require.False(t, u.StateReader().IsKnownController(victimAddr.ControllerID()),
		"victim must be unknown before the poison")

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

	const poisonAmount = 100_000_000
	// output genuinely locked to the attacker (srcAddr), but its index-value
	// tuple also names the victim's controller — so it is indexed under the
	// victim's account despite the victim not controlling it.
	poison := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(poisonAmount)).WithLock(srcAddr)
		o.PutConstraint(
			ledger.IndexValuesTupleBytes([][]byte{srcAddr.ControllerID(), victimAddr.ControllerID()}),
			ledger.ConstraintIndexIndexValues,
		)
	})
	_, err = txb.ProduceOutput(poison)
	require.NoError(t, err)

	remainder := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(total - poisonAmount)).WithLock(srcAddr)
	})
	_, err = txb.ProduceOutput(remainder)
	require.NoError(t, err)

	lib := ledger.L(maxTs.Slot)
	txb.SetTimestamp(maxTs.AddTicks(int(lib.TransactionPace)))
	txb.ComputeInputCommitment()
	txb.SignED25519(privKey)

	// The poison output is a valid transaction (two distinct, non-reserved index
	// values are permitted); the defence is on the read side, not validation.
	require.NoError(t, u.AddTransaction(txb.Bytes(), func(_ *transaction.Transaction, e error) error { return e }),
		"poison tx is structurally valid and must be accepted")

	// The victim must NOT see the poison output and must NOT be made known by it.
	victimOuts, err := u.StateReader().GetUTXOsForController(victimAddr.ControllerID())
	require.NoError(t, err)
	require.Len(t, victimOuts, 0, "index-poisoned output must be filtered from the victim's account")
	require.EqualValues(t, 0, u.Balance(victimAddr), "poison must not inflate the victim's balance")
	require.False(t, u.StateReader().IsKnownController(victimAddr.ControllerID()),
		"poison must not make the victim a known controller (spam-gate bypass)")

	// The real owner still sees it.
	tx, err := transaction.Parse(txb.Bytes())
	require.NoError(t, err)
	poisonOID := base.MustNewOutputID(tx.ID(), 0) // first produced output

	ownerOuts, err := u.StateReader().GetUTXOsForController(srcAddr.ControllerID())
	require.NoError(t, err)
	foundPoison := false
	for _, od := range ownerOuts {
		if od.ID == poisonOID {
			foundPoison = true
		}
	}
	require.True(t, foundPoison, "the real owner must still see its own output")
}
