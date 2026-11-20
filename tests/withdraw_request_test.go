package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/util/testutil/txbtest"
	"github.com/stretchr/testify/require"
)

// A withdrawal request asks the sequencer to pay an amount from its chain to a target. A withdrawal below
// the storage deposit of the output it would create can never be paid: the request is refused as invalid,
// so the sequencer drops it, and the milestone consumes nothing. Before, the command consumed the request
// and credited its fee before the withdrawal output failed, and the builder kept both while the attacher
// dropped the input from the past cone: the milestone took the fee without paying, and declared a coverage
// that left out the request's past cone, so every node refused it. A withdrawal above the deposit is paid.
func TestWithdrawRequest(t *testing.T) {
	for _, c := range []struct {
		name   string
		amount uint64
		paid   bool
	}{
		{"below the storage deposit", 5_019_444, false},
		{"above the storage deposit", 40_000_000, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			td, ts := newTestWithUTXODBData(t, 1)
			ts = base.MaximumTime(ts, td.seqOutput().ID.Timestamp()).AddSlots(2)
			req := txbuilder_seq.NewWithdrawRequestOutput(td.seqID, td.targetAddr, 100_000, c.amount, td.masterAddr)
			require.NoError(t, td.u.SendOutput(td.targetPrivateKey, req, base.T(ts.Slot, 20)))

			ts = ts.AddSlots(1)
			txb, err := txbuilder_seq.NewWithSequencerID(ts, td.seqID, td.targetPrivateKey, td.u.SugaredStateReader())
			require.NoError(t, err)
			require.NoError(t, txb.AddEndorsement(base.RandomTransactionID(true, 2, base.T(ts.Slot, 0))))
			backlog := td.tagAlongBacklog()
			require.Len(t, backlog, 1)
			_, valid, err := txb.AddTagAlongInput(backlog[0])
			if !c.paid {
				require.Error(t, err)
				require.False(t, valid, "a withdrawal that can never be paid is refused for good")
			} else {
				require.NoError(t, err)
			}

			txBytes, _, txStr, err := txbtest.BuildAndValidate(txb)
			require.NoError(t, err, txStr)
			require.NoError(t, td.u.AddTransaction(txBytes))
			rdr := td.u.SugaredStateReader()
			_, consumed := rdr.GetUTXO(backlog[0].ID)
			require.Equal(t, c.paid, !consumed, "the request is consumed exactly when the withdrawal is paid")
			outs, err := rdr.GetOutputsForAccount(td.masterAddr.ControllerID())
			require.NoError(t, err)
			paid := false
			for _, o := range outs {
				paid = paid || (o.Output.TokenBalance() == c.amount && o.Output.Lock().Name() == ledger.SigLockName)
			}
			require.Equal(t, c.paid, paid)
		})
	}
}
