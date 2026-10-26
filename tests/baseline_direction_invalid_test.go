package tests

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/stretchr/testify/require"
)

// TestMilestoneWithoutBaselineDirectionRejected checks that a sequencer transaction which gives
// no baseline direction is marked invalid by the attacher and the node keeps running.
func TestMilestoneWithoutBaselineDirectionRejected(t *testing.T) {
	// hand-built sequencer milestones can't declare the attacher-computed coverageDelta
	defer reinitTestLedgerNoCoverageMonotonicity()()

	testData := initWorkflowTest(t, 1)
	defer testData.stopAndWait()
	require.NoError(t, testData.wrk.EnsureLatestBranches())

	branches := multistate.FetchLatestBranches(testData.wrk.StateStore())
	require.EqualValues(t, 1, len(branches))
	chainOut := branches[0].SequencerOutput.MustAsChainOutput()

	// the transactions are stamped in a later slot than the branch holding the chain input
	require.Eventually(t, func() bool { return chainOut.ID.Slot()+1 < ledger.TimeNow().Slot },
		10*time.Second, 50*time.Millisecond)
	ts := ledger.TimeNow()
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}

	// expectRejected gossips the transaction and waits for its vertex to be marked bad
	expectRejected := func(txBytes []byte) {
		tx, err := transaction.Parse(txBytes)
		require.NoError(t, err)
		require.True(t, tx.IsSequencerTransaction())
		testData.wrk.TxBytesInFromPeerQueued(txBytes, nil, testData.wrk.SelfPeerID(), tx.ID())
		require.Eventually(t, func() bool {
			vid := attacher.AttachTxID(tx.ID(), testData.wrk)
			return vid.GetTxStatus() == vertex.Bad
		}, 10*time.Second, 20*time.Millisecond, "%s was not rejected", tx.IDShortString())
		require.False(t, testData.wrk.IsShuttingDown())
	}

	t.Run("cross-slot predecessor, no endorsement, no explicit baseline", func(t *testing.T) {
		txBytes, err := txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
			SeqName:       "testSeq",
			Timestamp:     ts,
			ChainInput:    chainOut,
			SignatureType: base.SignatureTypeED25519,
			PrivateKey:    genesisPrivateKey,
			PublicKey:     genesisPrivateKey.Public().(ed25519.PublicKey),
		})
		require.NoError(t, err)
		expectRejected(txBytes)
	})

	t.Run("endorsement of a non-sequencer transaction as the only direction", func(t *testing.T) {
		// both in one slot, as endorsements must be, and early enough in it to leave room for the pace
		var tsTransfer base.LedgerTime
		require.Eventually(t, func() bool {
			tsTransfer = ledger.TimeNow()
			return tsTransfer.Tick > 2 && tsTransfer.Tick < 100
		}, 5*time.Second, 10*time.Millisecond)
		rdr := testData.wrk.HeaviestStateForLatestTimeSlot()
		oDatas, err := rdr.GetUTXOsForController(testData.addr.ControllerID())
		require.NoError(t, err)
		in, err := oDatas[0].Parse()
		require.NoError(t, err)
		transferBytes, err := utxodb.MakeSimpleTransferTransaction(utxodb.NewTransferData(testData.privKey, testData.addr, tsTransfer).
			MustWithInputs(in).
			WithAmount(1_000_000_000).
			WithTargetLock(testData.addrAux))
		require.NoError(t, err)
		transfer, err := transaction.Parse(transferBytes)
		require.NoError(t, err)
		require.False(t, transfer.IsSequencerTransaction())
		require.EqualValues(t, tsTransfer.Slot, transfer.Timestamp().Slot)

		txBytes, err := txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
			SeqName:       "testSeq",
			Timestamp:     transfer.Timestamp().AddTicks(int(ledger.L(0).TransactionPaceSequencer)),
			ChainInput:    chainOut,
			Endorsements:  []base.TransactionID{transfer.ID()},
			SignatureType: base.SignatureTypeED25519,
			PrivateKey:    genesisPrivateKey,
			PublicKey:     genesisPrivateKey.Public().(ed25519.PublicKey),
		})
		require.NoError(t, err)
		expectRejected(txBytes)
	})

	// walletSeqTx is a wallet transaction carrying sequencer data that names its only output, built
	// with the given chain constraint, as the sequencer output
	walletSeqTx := func(chainConstraint []byte, ts base.LedgerTime) []byte {
		rdr := testData.wrk.HeaviestStateForLatestTimeSlot()
		oDatas, err := rdr.GetUTXOsForController(testData.addr.ControllerID())
		require.NoError(t, err)
		require.EqualValues(t, 1, len(oDatas))
		in, err := oDatas[0].Parse()
		require.NoError(t, err)

		txb := exhelp.New()
		_, _, err = txb.ConsumeOutputsNoUnlock(in)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)
		out := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(in.Output.TokenBalance()).WithLock(testData.addr)
			o.MustPushConstraint(chainConstraint)
			o.MustPushConstraint(ledger.NewSequencerConstraint(0).Bytes())
		})
		idx, err := txb.ProduceOutput(out)
		require.NoError(t, err)
		txb.SetSequencerData(idx, 0xff)
		txb.SetTimestamp(ts)
		txb.ComputeInputCommitment()
		txb.SignED25519(testData.privKey)
		return txb.Bytes()
	}

	t.Run("chain origin marked as sequencer transaction", func(t *testing.T) {
		expectRejected(walletSeqTx(ledger.NewChainOrigin(ts.Slot).Bytes(), ts))
	})

	t.Run("branch with a zero chain predecessor", func(t *testing.T) {
		// a branch whose chain predecessor input is the all-zero output ID
		_, priv, err := ed25519.GenerateKey(nil)
		require.NoError(t, err)
		slot := ledger.TimeNow().Slot
		txb := exhelp.New()
		_, err = txb.ConsumeOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(100_000_000).WithLock(ledger.SigLockFromED25519PrivateKey(priv))
		}), base.OutputID{})
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)
		seqIdx, err := txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(100_000_000).WithLock(ledger.SigLockFromED25519PrivateKey(priv))
			o.MustPushConstraint(ledger.NewChainConstraint(testData.bootstrapChainID, 0, 0, 0, 0, 1, 1).Bytes())
			o.MustPushConstraint(ledger.NewSequencerConstraint(0).Bytes())
		}))
		require.NoError(t, err)
		stemIdx, err := txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(0).WithLock(&ledger.StemLock{})
		}))
		require.NoError(t, err)
		txb.SetSequencerData(seqIdx, stemIdx)
		txb.SetTimestamp(base.T(slot, 0))
		txb.ComputeInputCommitment()
		txb.SignED25519(priv)
		expectRejected(txb.Bytes())
	})

	t.Run("chain predecessor index without an input", func(t *testing.T) {
		// spaced from the previous transaction of the same wallet, so the sender pace gate passes it
		ts2 := ts.AddTicks(4 * int(ledger.L(0).TransactionPace))
		cc := ledger.NewChainConstraint(testData.bootstrapChainID, 7, 0, 0, 0, 1, 0)
		expectRejected(walletSeqTx(cc.Bytes(), ts2))
	})
}
