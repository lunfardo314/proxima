package vertex

// Vertex.ValidateConstraints() rejects an invalid transaction with an error, never a panic. The
// transaction carries a token-balance cell >= 2^63 (negative as int64): full validation rejects it,
// and the error message renders the transaction, which must not panic on such amounts, since the
// attacher does not recover from a panic.

import (
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/stretchr/testify/require"
)

func TestValidateConstraintsNegativeTokenCellNoPanic(t *testing.T) {
	const amount = 1_000_000_000
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	priv, _, addr := u.GenerateAddress(1)
	require.NoError(t, u.TokensFromFaucet(addr, amount))
	_, _, dst := u.GenerateAddress(2)

	outsData, err := u.StateReader().GetUTXOsForController(addr.ControllerID())
	require.NoError(t, err)
	outs, err := ledger.ParseAndSortOutputData(outsData, nil)
	require.NoError(t, err)

	// bypassing the honest builder: output A with token cell uint64(-1), output B such that
	// the int64 sum balances; an attacker sets the bytes directly
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
	const neg = int64(-1)
	txb.TxBuilder.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) { o.WithAmounts(neg).WithLock(dst) }).Bytes())
	txb.TxBuilder.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) { o.WithAmounts(int64(total) - neg).WithLock(addr) }).Bytes())
	txb.SetTimestamp(maxTs.AddTicks(int(ledger.L(maxTs.Slot).TransactionPace)))
	txb.ComputeInputCommitment()
	txb.SignED25519(priv)

	// partial validation accepts it; the input queue and AttachTransaction refuse it before it
	// reaches this point (CheckMalformed), so the rendering here is the last line of defence
	tx, err := transaction.ParseWithPartialValidation(txb.Bytes())
	require.NoError(t, err)

	// vertex with resolved inputs, the way the attacher validates it
	v := NewVertex(tx)
	for i := range outs {
		oid := tx.MustInputAt(byte(i))
		require.Equal(t, outs[i].ID, oid, "inputs in the same order")
		vt := newVirtualTx()
		vt.mustAddOutput(oid.Index(), outs[i].Output)
		v.Inputs[i] = vt.wrapWithID(oid.TransactionID())
	}

	require.NotPanics(t, func() { err = v.ValidateConstraints() },
		"ValidateConstraints must not panic")
	require.Error(t, err, "the transaction must be rejected")
}

// An output with an unparsable lock element: OutputFromBytes accepts it, Output.Lock() asserts.
// Ingress rejects such a lock today, so the test skips itself; it guards the rendering in case
// that changes.
func TestValidateConstraintsUnparsableLockNoPanic(t *testing.T) {
	const amount = 1_000_000_000
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	priv, _, addr := u.GenerateAddress(3)
	require.NoError(t, u.TokensFromFaucet(addr, amount))
	_, _, dst := u.GenerateAddress(4)

	outsData, err := u.StateReader().GetUTXOsForController(addr.ControllerID())
	require.NoError(t, err)
	outs, err := ledger.ParseAndSortOutputData(outsData, nil)
	require.NoError(t, err)

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
	half := int64(total / 2)
	txb.TxBuilder.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(half).WithLock(dst)
		o.PutConstraint([]byte{0x30, 0x30}, ledger.ConstraintIndexLock) // unparsable lock
	}).Bytes())
	txb.TxBuilder.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) { o.WithAmounts(int64(total) - half).WithLock(addr) }).Bytes())
	txb.SetTimestamp(maxTs.AddTicks(int(ledger.L(maxTs.Slot).TransactionPace)))
	txb.ComputeInputCommitment()
	txb.SignED25519(priv)

	tx, err := transaction.ParseWithPartialValidation(txb.Bytes())
	if err != nil {
		t.Skipf("ingress rejects it (not reachable this way): %v", err)
	}
	v := NewVertex(tx)
	for i := range outs {
		oid := tx.MustInputAt(byte(i))
		vt := newVirtualTx()
		vt.mustAddOutput(oid.Index(), outs[i].Output)
		v.Inputs[i] = vt.wrapWithID(oid.TransactionID())
	}
	require.NotPanics(t, func() { err = v.ValidateConstraints() },
		"ValidateConstraints must not panic on an unparsable lock")
	require.Error(t, err)
}
