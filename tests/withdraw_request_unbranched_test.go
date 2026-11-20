package tests

import (
	"testing"
	"time"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/stretchr/testify/require"
)

// End to end on a running v3 sequencer: a withdrawal request (code 1) from its controller whose producing
// wallet transaction, and that transaction's own funding, are in no branch yet. A payable withdrawal is
// included in a reliable branch and paid, so the milestones consuming it declared the coverage of the
// request's past cone. One below the storage deposit is refused and the sequencer keeps branching; before,
// every milestone consuming it was refused by the nodes for its coverage delta.
func TestWithdrawRequestFromUnbranchedTx(t *testing.T) {
	for _, c := range []struct {
		name   string
		amount uint64
		paid   bool
	}{
		{"payable", 40_000_000, true},
		{"below the storage deposit", 5_019_444, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			td := initWorkflowTest(t, 1)
			seq, err := newTestSequencer(td.wrk, td.bootstrapChainID, genesisPrivateKey)
			require.NoError(t, err)
			seq.Start()
			defer func() { td.stop(); td.waitStop() }()

			deadline := time.Now().Add(8 * ledger.SlotDuration())
			var lrb *multistate.BranchData
			for {
				lrb = td.wrk.Branches().FindLatestReliableBranch()
				if lrb != nil && lrb.Stem.ID.Slot() > td.distributionBranchTxID.Slot() {
					break
				}
				require.True(t, time.Now().Before(deadline), "the sequencer did not branch")
				time.Sleep(ledger.TickDuration())
			}
			rdr := multistate.MustNewSugaredReadableState(td.wrk.StateStore(), lrb.Root, 0)
			outs, err := rdr.GetOutputsForAccount(td.addr.ControllerID())
			require.NoError(t, err)
			require.NotEmpty(t, outs)
			in := outs[0]

			controller := ledger.SigLockFromED25519PrivateKey(genesisPrivateKey)
			const funding = 99_600_000
			amount := c.amount
			ts := ledger.TimeNow()
			if ts.IsSlotBoundary() {
				ts = ts.AddTicks(1)
			}
			// A: the wallet funds the controller with 99.6 PROX
			a := exhelp.New()
			_, _, err = a.ConsumeOutputsUnlock(in)
			require.NoError(t, err)
			_, err = a.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
				o.WithTokenBalance(funding).WithLock(controller)
			}))
			require.NoError(t, err)
			_, err = a.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
				o.WithTokenBalance(in.Output.TokenBalance() - funding).WithLock(td.addr)
			}))
			require.NoError(t, err)
			a.FinaliseAndSign(ts, td.privKey)
			txA, err := td.wrk.TxBytesInForTests(a.Bytes())
			require.NoError(t, err)

			// B: the controller spends A's output at once into a withdrawal request and change
			fundOut := ledger.OutputWithID{ID: base.MustNewOutputID(txA, 0), Output: ledger.NewOutput(func(o *ledger.OutputBuilder) {
				o.WithTokenBalance(funding).WithLock(controller)
			})}
			target := td.addrAux
			b := exhelp.New()
			_, _, err = b.ConsumeOutputsUnlock(&fundOut)
			require.NoError(t, err)
			_, err = b.ProduceOutput(txbuilder_seq.NewWithdrawRequestOutput(td.bootstrapChainID, controller, tagAlongFee, amount, target))
			require.NoError(t, err)
			_, err = b.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
				o.WithTokenBalance(funding - tagAlongFee).WithLock(controller)
			}))
			require.NoError(t, err)
			b.FinaliseAndSign(ts.AddTicks(int(ledger.L(ts.Slot).TransactionPace)), genesisPrivateKey)
			txB, err := td.wrk.TxBytesInForTests(b.Bytes())
			require.NoError(t, err)

			deadline = time.Now().Add(20 * ledger.SlotDuration())
			included, paid := false, false
			for time.Now().Before(deadline) && !(included && paid) {
				if lrb = td.wrk.Branches().FindLatestReliableBranch(); lrb != nil {
					rdr = multistate.MustNewSugaredReadableState(td.wrk.StateStore(), lrb.Root, 0)
					included = rdr.KnowsCommittedTransaction(txB)
					if targetOuts, err := rdr.GetOutputsForAccount(target.ControllerID()); err == nil {
						for _, o := range targetOuts {
							paid = paid || o.Output.TokenBalance() == amount
						}
					}
				}
				time.Sleep(ledger.TickDuration())
			}
			if c.paid {
				require.True(t, included, "the withdrawal request is included in a reliable branch")
				require.True(t, paid, "the withdrawal is paid to its target")
				return
			}
			require.False(t, included || paid, "a withdrawal below the storage deposit is never paid")
			require.Greater(t, lrb.Stem.ID.Slot(), txB.Slot()+3, "the sequencer keeps branching")
		})
	}
}
