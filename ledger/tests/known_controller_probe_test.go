package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/stretchr/testify/require"
)

// IsKnownController reads a bounded number of account index entries, whatever their number: entries
// produced by others under an account are skipped, and it stops after MaxKnownControllerProbe of them.
// An output the caller names as a candidate is checked directly.
//
// Another account produces more than MaxKnownControllerProbe outputs naming the victim in their index
// values, and in the same transaction, at the last index, one output to the victim: an account index key
// ends with the output ID, so the victim's own output sorts after all the others.
func TestKnownControllerBoundedProbe(t *testing.T) {
	u, privKey, srcAddr := newTestEnv(t, 100_000*base.PROX)
	_, _, victim := u.GenerateAddress(2)
	_, _, fresh := u.GenerateAddress(3)

	const numNaming = multistate.MaxKnownControllerProbe + 36
	outsData, err := u.StateReader().GetUTXOsForController(srcAddr.ControllerID())
	require.NoError(t, err)
	outs, err := ledger.ParseAndSortOutputData(outsData, nil)
	require.NoError(t, err)
	txb := exhelp.New()
	total, maxTs, err := txb.ConsumeOutputsNoUnlock(outs...)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)
	const amount = 40 * base.PROX
	for i := 0; i < numNaming; i++ {
		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(int64(amount)).WithLock(srcAddr)
			o.PutConstraint(ledger.IndexValuesTupleBytes([][]byte{srcAddr.ControllerID(), victim.ControllerID()}), ledger.ConstraintIndexIndexValues)
		}))
		require.NoError(t, err)
	}
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(total - (numNaming+1)*amount)).WithLock(srcAddr)
	}))
	require.NoError(t, err)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(amount)).WithLock(victim)
	}))
	require.NoError(t, err)
	txb.SetTimestamp(maxTs.AddTicks(int(ledger.L(maxTs.Slot).TransactionPace)))
	txb.ComputeInputCommitment()
	txb.SignED25519(privKey)
	require.NoError(t, u.AddTransaction(txb.Bytes(), func(_ *transaction.Transaction, e error) error { return e }))

	rdr := u.StateReader()
	ids, err := rdr.GetUTXOIDsForController(victim.ControllerID())
	require.NoError(t, err)
	require.Len(t, ids, numNaming+1, "every naming output and the victim's own are indexed under the victim")
	own, err := rdr.GetUTXOsForController(victim.ControllerID())
	require.NoError(t, err)
	require.Len(t, own, 1)
	require.Equal(t, own[0].ID, ids[len(ids)-1], "the victim's own output is the last entry")

	require.False(t, rdr.IsKnownController(victim.ControllerID()),
		"the probe stops after MaxKnownControllerProbe entries of others, before the victim's own output")
	require.True(t, rdr.IsKnownController(victim.ControllerID(), own[0].ID), "an owned candidate makes the victim known")
	require.False(t, rdr.IsKnownController(victim.ControllerID(), ids[0]), "a candidate owned by another does not")
	require.False(t, rdr.IsKnownController(fresh.ControllerID(), own[0].ID), "nor does a candidate owned by the victim for another account")
	require.True(t, rdr.IsKnownController(srcAddr.ControllerID()), "an account whose own outputs come first is known without candidates")
}
