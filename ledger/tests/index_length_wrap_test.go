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

// The account index key is [partition, byte(len(value)), value, outputID]: the
// length byte keeps only the low 8 bits, and validation does not cap the length
// of an index value. An index value of 256+L bytes beginning with an L-byte
// account therefore lands under that account's prefix, with a remainder longer
// than an output ID. These tests plant such entries through ordinary, fully
// validated transactions and check that every account reader skips them: before
// the fix the stem readers and IsKnownController asserted (a node crash) and the
// error-returning readers failed every query of the targeted account.

// produceWithExtraIndexValue commits a valid transfer from srcAddr to itself
// whose first output carries `extra` in its index-value tuple beside the holder
// ID sigLock requires. The rest of the balance goes back to srcAddr.
func produceWithExtraIndexValue(t *testing.T, u *utxodb.UTXODB, privKey ed25519.PrivateKey, srcAddr ledger.SigLock, extra []byte) {
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
	const plantedAmount = 500_000_000
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(plantedAmount).WithLock(srcAddr)
		o.PutConstraint(ledger.IndexValuesTupleBytes([][]byte{srcAddr.ControllerID(), extra}), ledger.ConstraintIndexIndexValues)
	}))
	require.NoError(t, err)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(total - plantedAmount)).WithLock(srcAddr)
	}))
	require.NoError(t, err)
	txb.SetTimestamp(maxTs.AddTicks(int(ledger.L(maxTs.Slot).TransactionPace)))
	txb.ComputeInputCommitment()
	txb.SignED25519(privKey)
	err = u.AddTransaction(txb.Bytes(), func(_ *transaction.Transaction, e error) error { return e })
	require.NoError(t, err, "the planting transaction is valid and commits")
}

// A 257-byte index value starting with 0x00 gets length byte 1 and lands under
// the stem account (StemAccountID = 0x00). The stem readers must still return
// the one genuine stem output and not crash.
func TestIndexLengthWrapUnderStem(t *testing.T) {
	u, privKey, srcAddr := newTestEnv(t, 1_000_000_000)
	stemSlot, stemBytes := u.StateReader().GetStem()
	stemOut := u.SugaredStateReader().GetStemOutput()

	extra := make([]byte, 256+len(ledger.StemAccountID))
	copy(extra, ledger.StemAccountID)
	produceWithExtraIndexValue(t, u, privKey, srcAddr, extra)

	var slot uint32
	var data []byte
	require.NotPanics(t, func() { slot, data = u.StateReader().GetStem() })
	require.EqualValues(t, stemSlot, slot)
	require.Equal(t, stemBytes, data)
	require.NotPanics(t, func() {
		require.EqualValues(t, stemOut.ID, u.SugaredStateReader().GetStemOutput().ID)
	})
}

// A value of 256+32 bytes starting with a holder ID lands under that holder's
// account. The victim's queries must return exactly its own outputs without
// error, and a fresh holder named only by such an entry must stay unknown,
// without IsKnownController (the unknown-sender gate at ingress) crashing.
func TestIndexLengthWrapUnderAccount(t *testing.T) {
	u, privKey, srcAddr := newTestEnv(t, 1_000_000_000)
	_, _, victimAddr := u.GenerateAddress(2)
	require.NoError(t, u.TokensFromFaucet(victimAddr, 10_000_000))
	_, _, freshAddr := u.GenerateAddress(3)

	victimBalance := u.Balance(victimAddr)
	victimIDs, err := u.StateReader().GetUTXOIDsForController(victimAddr.ControllerID())
	require.NoError(t, err)
	require.Len(t, victimIDs, 1)

	for _, target := range []ledger.SigLock{victimAddr, freshAddr} {
		extra := make([]byte, 256+len(target.ControllerID()))
		copy(extra, target.ControllerID())
		produceWithExtraIndexValue(t, u, privKey, srcAddr, extra)
	}

	ids, err := u.StateReader().GetUTXOIDsForController(victimAddr.ControllerID())
	require.NoError(t, err)
	require.Equal(t, victimIDs, ids)

	outs, err := u.StateReader().GetUTXOsForController(victimAddr.ControllerID())
	require.NoError(t, err)
	require.Len(t, outs, 1)
	require.EqualValues(t, victimIDs[0], outs[0].ID)
	require.EqualValues(t, victimBalance, u.Balance(victimAddr))

	require.NotPanics(t, func() {
		require.True(t, u.StateReader().IsKnownController(victimAddr.ControllerID()))
		require.False(t, u.StateReader().IsKnownController(freshAddr.ControllerID()))
	})

	// the planted output is the attacker's own and stays listed under its account
	// (the second planting spent the first): the last planted output plus the remainder
	srcOuts, err := u.StateReader().GetUTXOsForController(srcAddr.ControllerID())
	require.NoError(t, err)
	require.Len(t, srcOuts, 2)
}
