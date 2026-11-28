package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/stretchr/testify/require"
)

// IterateUTXOIDsForControllerInSlot returns exactly the account's index entries of one slot: the
// account key ends with the output ID, which begins with the slot. Two transactions in different slots
// each produce outputs indexed under one account; each slot's iteration sees only its own outputs, and
// together they are the whole account.
func TestIterateUTXOIDsForControllerInSlot(t *testing.T) {
	u, privKey, srcAddr := newTestEnv(t, 10_000*base.PROX)
	_, _, account := u.GenerateAddress(2)

	produce := func(n int, slotsAhead int) uint32 {
		outsData, err := u.StateReader().GetUTXOsForController(srcAddr.ControllerID())
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
		const amount = 40 * base.PROX
		for i := 0; i < n; i++ {
			_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
				o.WithAmounts(int64(amount)).WithLock(account)
			}))
			require.NoError(t, err)
		}
		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(int64(total - uint64(n)*amount)).WithLock(srcAddr)
		}))
		require.NoError(t, err)
		ts := maxTs.AddSlots(uint32(slotsAhead))
		txb.SetTimestamp(ts)
		txb.ComputeInputCommitment()
		txb.SignED25519(privKey)
		require.NoError(t, u.AddTransaction(txb.Bytes(), func(_ *transaction.Transaction, e error) error { return e }))
		return ts.Slot
	}
	slot1 := produce(3, 1)
	slot2 := produce(5, 2)
	require.NotEqual(t, slot1, slot2)

	rdr := u.StateReader()
	inSlot := func(slot uint32) (ret []base.OutputID) {
		require.NoError(t, rdr.IterateUTXOIDsForControllerInSlot(account.ControllerID(), slot, func(oid base.OutputID) bool {
			require.EqualValues(t, slot, oid.Slot())
			ret = append(ret, oid)
			return true
		}))
		return
	}
	require.Len(t, inSlot(slot1), 3)
	require.Len(t, inSlot(slot2), 5)
	require.Empty(t, inSlot(slot2+1))

	all, err := rdr.GetUTXOIDsForController(account.ControllerID())
	require.NoError(t, err)
	require.ElementsMatch(t, all, append(inSlot(slot1), inSlot(slot2)...))
}
