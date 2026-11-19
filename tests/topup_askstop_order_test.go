package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/util/testutil/txbtest"
	"github.com/stretchr/testify/require"
)

// makeDelegationTo creates a plain unfrozen delegation of the master to the test's sequencer.
func (td *testWithUTXODBData) makeDelegationTo(t *testing.T, amount uint64) base.ChainID {
	origin, err := td.u.CreateChainOrigin(td.masterPrivateKey, td.seqOutput().ID.Timestamp().AddSlots(1), amount)
	require.NoError(t, err)

	txb := exhelp.New()
	_, err = txb.ConsumeOutput(origin.Output, origin.ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.PutConstraint(origin.Output.Amounts().Bytes(), ledger.ConstraintIndexAmounts)
		o.WithLock(ledger.NewDelegateLock(td.seqID, base.HolderID(td.masterAddr), 980))
		o.PutConstraint(ledger.NewChainConstraint(origin.ChainID, 0, origin.OriginSlot, 0, 0, 1, 0).Bytes(), ledger.ConstraintIndexChain)
		o.MustPushConstraint(ledger.DelegateLockState{}.Bytes())
	}))
	require.NoError(t, err)
	txb.SetTimestamp(origin.ID.Timestamp().AddSlots(1))
	txb.ComputeInputCommitment()
	txb.SignED25519(td.masterPrivateKey)
	txBytes, _, txStr, err := txbtest.BuildAndValidate(txb)
	require.NoError(t, err, txStr)
	require.NoError(t, td.u.AddTransaction(txBytes))
	return origin.ChainID
}

// A sequencer qualifying for fresh freezes by coverage alone (15,000 PROX of its own, 90,000 frozen)
// receives in one milestone a top-up of an unfrozen delegation, which freezes it afresh, and an
// askstop of its big frozen delegation, which takes that coverage away. Applied top-up first, the
// top-up is admitted on coverage the milestone then loses: the builder refuses the milestone with
// ErrCoverageBatchShort instead of producing one the ledger refuses. Applied askstop first, the order
// the proposer uses, the top-up sees the coverage as it ends up, is skipped, and the milestone is valid.
func TestTopUpAfterAskStop(t *testing.T) {
	for _, topUpFirst := range []bool{true, false} {
		name := "askstop first"
		if topUpFirst {
			name = "top-up first"
		}
		t.Run(name, func(t *testing.T) {
			saved := utxodbSeqInitBalance
			utxodbSeqInitBalance = 15_000 * base.PROX
			t.Cleanup(func() { utxodbSeqInitBalance = saved })

			td, ts := newTestWithUTXODBData(t, 1) // delegation 0: left unfrozen
			require.NoError(t, td.u.TokensFromFaucet(td.masterAddr, 200_000*base.PROX))
			big := td.makeDelegationTo(t, 90_000*base.PROX)

			// milestone 1 freezes the big delegation, admitted by coverage (15,000 + 90,000)
			ts = base.MaximumTime(ts, td.seqOutput().ID.Timestamp()).AddSlots(3)
			txb, err := txbuilder_seq.NewWithSequencerID(ts, td.seqID, td.targetPrivateKey, td.u.SugaredStateReader())
			require.NoError(t, err)
			require.NoError(t, txb.AddEndorsement(base.RandomTransactionID(true, 2, base.T(ts.Slot, 0))))
			for _, d := range td.freezableDelegations(ts) {
				if d.ChainID == big {
					_, valid, err := txb.FreezeDelegation(&d)
					require.NoError(t, err)
					require.True(t, valid)
				}
			}
			txBytes, _, txStr, err := txbtest.BuildAndValidate(txb)
			require.NoError(t, err, txStr)
			require.NoError(t, td.u.AddTransaction(txBytes))

			// a third party tops up the small delegation, the master asks to stop the big one
			ts = ts.AddSlots(1)
			thirdKey, _, thirdAddr := td.u.GenerateAddress(777)
			require.NoError(t, td.u.TokensFromFaucet(thirdAddr, 1_000*base.PROX))
			topUp := txbuilder_seq.NewTopUpDelegationReqOutput(td.seqID, thirdAddr, td.delegationIDs[0], 100*base.PROX)
			require.NoError(t, td.u.SendOutput(thirdKey, topUp, base.T(ts.Slot, 20)))
			bigOut, err := td.u.SugaredStateReader().GetDelegatedOutput(big)
			require.NoError(t, err)
			askStop := txbuilder_seq.NewAskStopDelegationReqOutput(td.seqID, td.masterAddr, big, 1*base.PROX, bigOut.AllowanceCeiling())
			require.NoError(t, td.u.SendOutput(td.masterPrivateKey, askStop, base.T(ts.Slot, 40)))

			// milestone 2 takes both requests in the chosen order
			ts = ts.AddSlots(1)
			txb, err = txbuilder_seq.NewWithSequencerID(ts, td.seqID, td.targetPrivateKey, td.u.SugaredStateReader())
			require.NoError(t, err)
			require.NoError(t, txb.AddEndorsement(base.RandomTransactionID(true, 2, base.T(ts.Slot, 0))))
			backlog := td.tagAlongBacklog()
			require.Len(t, backlog, 2)
			if txbuilder_seq.IsTopUpRequest(backlog[0]) != topUpFirst {
				backlog[0], backlog[1] = backlog[1], backlog[0]
			}
			require.Equal(t, topUpFirst, txbuilder_seq.IsTopUpRequest(backlog[0]))

			for _, o := range backlog {
				_, valid, err := txb.AddTagAlongInput(o)
				require.True(t, valid)
				if !topUpFirst && txbuilder_seq.IsTopUpRequest(o) {
					require.Error(t, err, "after the askstop the top-up's fresh freeze has no bound to stand on")
					continue
				}
				require.NoError(t, err)
			}
			_, _, txStr, err = txbtest.BuildAndValidate(txb)
			if topUpFirst {
				require.ErrorIs(t, err, txbuilder_seq.ErrCoverageBatchShort)
				return
			}
			require.NoError(t, err, txStr)
		})
	}
}
