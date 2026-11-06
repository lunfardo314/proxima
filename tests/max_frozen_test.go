package tests

import (
	"crypto/ed25519"
	"testing"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/sequencer/seqdata"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/lunfardo314/proxima/util/testutil/txbtest"
	"github.com/stretchr/testify/require"
)

// The max_frozen bound in the sequencer data (kb/seq_decommission.md): the
// sequencer freezes a delegation only while its frozen coverage stays at or
// under the bound. A refused freeze is not permanent (valid = true), so the
// delegation is frozen again once there is room, and it leaves the builder
// unchanged. Absent means unlimited, 0 means nothing is frozen.
func TestMaxFrozenBound(t *testing.T) {
	privKey := testutil.GetTestingPrivateKey()
	addr := ledger.SigLockFromED25519PrivateKey(privKey)
	seqID := base.RandomChainID()
	predTs := base.T(1000, 50)
	predID := base.MustNewOutputID(base.RandomTransactionID(true, 2, predTs), 0)

	// a sequencer holding alreadyFrozen in frozen-coverage cell 0, with the given seq data
	newTxb := func(ts base.LedgerTime, sd *seqdata.SequencerData, alreadyFrozen int64) *txbuilder_seq.SeqTxBuilder {
		predChain := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(int64(1_000_000_000)<<8, 0, 0, alreadyFrozen).WithLock(addr)
			o.PutConstraint(ledger.NewChainConstraint(seqID, 0, 1000, 0, 0, 1, 0).Bytes(), ledger.ConstraintIndexChain)
			_ = o.MustPushConstraint(ledger.NewSequencerConstraint(0).Bytes())
			_ = o.MustPushConstraint(easyfl.InlineDataBytecode(sd.Bytes()))
		})
		pred, ok := ledger.AsOutputWithChainID(predChain, predID)
		require.True(t, ok)
		txb, err := txbuilder_seq.New(txbuilder_seq.Params{
			Timestamp:     ts,
			Predecessor:   &pred,
			SignatureType: base.SignatureTypeED25519,
			PrivateKey:    privKey,
			PublicKey:     privKey.Public().(ed25519.PublicKey),
			StateReader:   multistate.DummyStateReader,
		})
		require.NoError(t, err)
		require.NoError(t, txb.AddEndorsement(base.RandomTransactionID(true, 2, base.T(ts.Slot, 0))))
		return txb
	}
	base0 := func() *seqdata.SequencerData {
		return seqdata.New().SetName("test").SetMinimumFee(1).SetSeqProfitMarginPromille(50)
	}

	dIn := delegationInit(base.HolderID(addr), seqID, 1000, 50)
	ts := base.MaximumTime(predTs.AddSlots(1), dIn.Timestamp().AddSlots(1))

	// unlimited: the freeze goes through and validates; what it adds to cell 0
	// is the amount the bound is measured against below
	txb := newTxb(ts, base0(), 0)
	succIdx, valid, err := txb.FreezeDelegation(&dIn)
	require.NoError(t, err)
	require.True(t, valid)
	_, _, txString, err := txbtest.BuildAndValidate(txb)
	require.NoError(t, err, txString)
	added := txb.ProducedOutputs[succIdx].Amounts().FrozenCoverageAt(0)
	require.Positive(t, added)

	const already = 1_000_000
	cases := []struct {
		name  string
		bound uint64
		fits  bool
	}{
		{"bound 0 freezes nothing", 0, false},
		{"one mote short", uint64(already + added - 1), false},
		{"exactly at the bound", uint64(already + added), true},
		{"above the bound already", already - 1, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			txb := newTxb(ts, base0().SetMaxFrozenCoverage(c.bound), already)
			require.Equal(t, c.bound == 0, txb.FreezesNothing())
			consumedBefore := len(txb.ConsumedOutputs)
			_, valid, err := txb.FreezeDelegation(&dIn)
			require.True(t, valid, "a freeze refused by the bound is not permanent")
			if !c.fits {
				require.ErrorContains(t, err, "max_frozen")
				require.Equal(t, consumedBefore, len(txb.ConsumedOutputs), "a refused freeze leaves the builder unchanged")
				return
			}
			require.NoError(t, err)
			_, _, txString, err := txbtest.BuildAndValidate(txb)
			require.NoError(t, err, txString)
		})
	}
}

// End to end over the utxodb harness: the controller sets max_frozen 0 with an
// ordinary set-params request, and from the next milestone on a top-up request,
// being a freeze, waits in the backlog instead of being taken. Other tag-along
// requests are not affected: the set-params request itself goes through.
func TestMaxFrozenZeroHoldsTopUp(t *testing.T) {
	td, ts := newTestWithUTXODBData(t, 1)
	d0 := td.delegation(t, 0)
	ts = base.MaximumTime(ts, d0.ID.Timestamp()).AddSlots(1)

	seqOut, err := td.u.SugaredStateReader().GetChainOutputWithID(td.seqID)
	require.NoError(t, err)
	sd, err := ledger.ParseSequencerData(seqOut.Output)
	require.NoError(t, err)
	req := txbuilder_seq.NewSeqDataCommandOutput(td.seqID, td.targetAddr, 1_000_000, sd.Clone().SetMaxFrozenCoverage(0))
	require.NoError(t, td.u.SendOutput(td.targetPrivateKey, req, ts))
	ts = ts.AddSlots(1)
	consumed, _, _ := td.seqStep(t, ts)
	require.Len(t, consumed, 1, "the set-params request is taken")

	seqOut, err = td.u.SugaredStateReader().GetChainOutputWithID(td.seqID)
	require.NoError(t, err)
	sd, err = ledger.ParseSequencerData(seqOut.Output)
	require.NoError(t, err)
	require.True(t, sd.FreezesNothing())

	topUp := td.postTopUp(t, 0, 3*txbuildercore.MinimumTopUpAmount, ts)
	ts = ts.AddSlots(1)
	_, refusedForNow, refusedForGood := td.seqStep(t, ts)
	require.Contains(t, refusedForNow, topUp, "a top-up waits for room, it is not refused for good")
	require.NotContains(t, refusedForGood, topUp)
}
