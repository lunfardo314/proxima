package txbuilder_seq

import (
	"crypto/ed25519"
	"fmt"
	"math/rand"
	"testing"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer/seqdata"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/stretchr/testify/require"
)

// initializes ledger.Library singleton for all tests and creates testing genesis private key

var genesisPrivateKey ed25519.PrivateKey

func init() {
	genesisPrivateKey = ledger.InitWithTestingLedgerIDData()
}

func TestBase(t *testing.T) {
	privKey := testutil.GetTestingPrivateKey()
	addr := ledger.AddressED25519FromPrivateKey(privKey)
	seqID := base.RandomChainID()
	bal := ledger.L().ID.MinimumAmountOnSequencer << 8

	sd := seqdata.New().
		SetName("test_seq").
		IncBranchHeight(2).
		IncChainHeight(4).
		SetMinimumFee(1)

	predTs := base.NewLedgerTime(1000, 50)
	predID := base.MustNewOutputID(base.RandomTransactionID(true, 2, predTs), 0)

	newPredChain := func(frozen ...int64) *ledger.OutputWithChainID {
		amounts := append(append(make([]int64, 0), int64(bal), 0), frozen...)

		predChain := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(amounts...).WithLock(addr)
			ccIdx := o.MustPushConstraint(ledger.NewChainConstraint(seqID, 0, 2, 1000, bal).Bytes())
			_ = o.MustPushConstraint(ledger.NewSequencerConstraint(ccIdx).Bytes())
			_ = o.MustPushConstraint(easyfl.InlineDataBytecode(sd.Bytes()))
		})

		pred, ok := ledger.AsOutputWithChainID(predChain, predID)
		require.True(t, ok)
		return &pred
	}

	newTxb := func(ts base.LedgerTime, frozen ...int64) *SeqTxBuilder {
		txb, err := New(ts, newPredChain(frozen...), nil, privKey, multistate.DummyStateReader)
		require.NoError(t, err)
		rndEndorsement := base.RandomTransactionID(true, 2, base.NewLedgerTime(ts.Slot, 0))
		err = txb.AddEndorsement(rndEndorsement)
		require.NoError(t, err)
		return txb
	}
	t.Run("+1 slot", func(t *testing.T) {
		ts := predTs.AddSlots(1)
		txb := newTxb(ts)
		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
	t.Run("+100 slots", func(t *testing.T) {
		ts := predTs.AddSlots(100)
		txb := newTxb(ts)
		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
	t.Run("+1000 slots", func(t *testing.T) {
		ts := predTs.AddSlots(1000)
		txb := newTxb(ts)
		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
	t.Run("+1 slot frozen 1 epoch", func(t *testing.T) {
		ts := predTs.AddSlots(1)
		txb := newTxb(ts, 11_000_000)
		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
	t.Run("+100 slots frozen 1 epoch", func(t *testing.T) {
		ts := predTs.AddSlots(100)
		txb := newTxb(ts, 11_000_000)
		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
	t.Run("+1000 slots frozen 1 epoch", func(t *testing.T) {
		ts := predTs.AddSlots(1000)
		txb := newTxb(ts, 11_000_000)
		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
	t.Run("+1 slot frozen 4 epochs", func(t *testing.T) {
		ts := predTs.AddSlots(1)
		txb := newTxb(ts, 11_000_000, 11_000_000, 11_000_000, 11_000_000)
		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
	t.Run("+100 slots frozen 4 epochs", func(t *testing.T) {
		ts := predTs.AddSlots(100)
		txb := newTxb(ts, 11_000_000, 11_000_000, 11_000_000, 11_000_000)
		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
	t.Run("+1000 slots frozen 4 epochs", func(t *testing.T) {
		ts := predTs.AddSlots(1000)
		txb := newTxb(ts, 11_000_000, 11_000_000, 11_000_000, 11_000_000)
		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
	t.Run("+2000 slots frozen 4 epochs", func(t *testing.T) {
		ts := predTs.AddSlots(2000)
		txb := newTxb(ts, 11_000_000, 11_000_000, 11_000_000, 11_000_000)
		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
	t.Run("+100 slot tag_along", func(t *testing.T) {
		ts := predTs.AddSlots(100)
		txb := newTxb(ts)

		tagAlongOut := ledger.OutputWithID{
			ID:     base.OutputID{},
			Output: NewWithdrawCommandOutput(seqID, privKey, 200, 1_000_000, addr),
		}
		err := txb.AddTagAlongInput(tagAlongOut)
		require.NoError(t, err)

		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
	t.Run("+1000 slot tag_along", func(t *testing.T) {
		ts := predTs.AddSlots(1000)
		txb := newTxb(ts, 1_000_000, 1_000_000, 1_000_000, 1_000_000)

		tagAlongOut := ledger.OutputWithID{
			ID:     base.OutputID{},
			Output: NewWithdrawCommandOutput(seqID, privKey, 200, 1_000_000, addr),
		}
		err := txb.AddTagAlongInput(tagAlongOut)
		require.NoError(t, err)

		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
	t.Run("+1000 slot withdraw", func(t *testing.T) {
		ts := predTs.AddSlots(1000)
		txb := newTxb(ts, 1_000_000, 1_000_000, 1_000_000, 1_000_000)

		tagAlongOut := ledger.OutputWithID{
			ID:     base.OutputID{},
			Output: NewWithdrawCommandOutput(seqID, privKey, 200, 10_000_000, addr),
		}
		err := txb.AddTagAlongInput(tagAlongOut)
		require.NoError(t, err)

		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
	t.Run("many rnd withdraw", func(t *testing.T) {
		ts := predTs.AddSlots(1000)
		txb := newTxb(ts, 1_000_000, 1_000_000, 1_000_000, 1_000_000)

		rndWithdraw := func(amount uint64, slot base.Slot) error {
			tagAlongOut := ledger.OutputWithID{
				ID:     base.MustNewOutputID(base.RandomTransactionID(false, 2, base.NewLedgerTime(slot, 50)), 1),
				Output: NewWithdrawCommandOutput(seqID, privKey, 200, amount, addr),
			}
			err := txb.AddTagAlongInput(tagAlongOut)
			return err
		}

		const howMany = 2 // 254
		for i := 0; i < howMany; i++ {
			rnd := rand.Intn(500)
			err := rndWithdraw(10_000_000-uint64(rnd), predTs.Slot-base.Slot(rnd))
			require.NoError(t, err)
		}

		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
	t.Run("change seq name", func(t *testing.T) {
		ts := predTs.AddSlots(1000)
		txb := newTxb(ts, 1_000_000, 1_000_000, 1_000_000, 1_000_000)

		predSeqData, err := ledger.ParseSequencerData(txb.chainInput.Output)
		require.NoError(t, err)

		tagAlongOut := ledger.OutputWithID{
			ID: base.OutputID{},
			Output: NewSeqDataCommandOutput(seqID, privKey, 200, predSeqData.Clone(func(sdUpdated *seqdata.SequencerData) {
				sdUpdated.SetName("newName").IncChainHeight()
			})),
		}
		err = txb.AddTagAlongInput(tagAlongOut)
		require.NoError(t, err)

		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
}

func delegationInit(master ledger.Accountable, seqID base.ChainID, maxSeqProfitMargin uint16, maxFreezeEpochs ...byte) ledger.DelegationOutput {
	dcons := ledger.DelegationConst()
	maxEpochs := byte(dcons.MaxFrozenEpochs)
	if len(maxFreezeEpochs) > 0 {
		maxEpochs = maxFreezeEpochs[0]
	}
	ret := ledger.MakeDelegationInitOutput(ledger.MakeDelegateInitOutputParams{
		Amount:             1_000_000_000,
		Master:             master,
		Target:             ledger.ChainLockFromChainID(seqID),
		MaxFreezeEpochs:    maxEpochs,
		MaxSeqProfitMargin: maxSeqProfitMargin,
		StartSlot:          0,
	})
	delegationInitOid := base.MustNewOutputID(base.RandomTransactionID(false, 2, base.NewLedgerTime(2, 50)), 1)

	dout, ok := ledger.AsDelegationOutput(ret, delegationInitOid)
	util.Assertf(ok, "AsDelegationOutput")
	return dout
}

func TestFreeze(t *testing.T) {
	privKey := testutil.GetTestingPrivateKey()
	addr := ledger.AddressED25519FromPrivateKey(privKey)
	seqID := base.RandomChainID()
	bal := ledger.L().ID.MinimumAmountOnSequencer << 8

	predTs := base.NewLedgerTime(1000, 50)
	predID := base.MustNewOutputID(base.RandomTransactionID(true, 2, predTs), 0)

	newPredChain := func(requiredSeqProfitMargin uint16, generous bool, frozen ...int64) *ledger.OutputWithChainID {
		amounts := append([]int64{int64(bal), 0}, frozen...)

		sd := seqdata.New().
			SetName("test_seq").
			IncBranchHeight(2).
			IncChainHeight(4).
			SetMinimumFee(1).
			SetSeqProfitMarginPromille(requiredSeqProfitMargin).
			SetGenerous(generous)

		predChain := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(amounts...).WithLock(addr)
			ccIdx := o.MustPushConstraint(ledger.NewChainConstraint(seqID, 0, 2, 1000, bal).Bytes())
			_ = o.MustPushConstraint(ledger.NewSequencerConstraint(ccIdx).Bytes())

			_ = o.MustPushConstraint(easyfl.InlineDataBytecode(sd.Bytes()))
		})

		pred, ok := ledger.AsOutputWithChainID(predChain, predID)
		util.Assertf(ok, "AsOutputWithChainID")
		return &pred
	}

	newTxb := func(ts base.LedgerTime, seqProfitMargin uint16, generous bool, frozen ...int64) *SeqTxBuilder {
		txb, err := New(ts, newPredChain(seqProfitMargin, generous, frozen...), nil, privKey, multistate.DummyStateReader)
		util.AssertNoError(err)
		rndEndorsement := base.RandomTransactionID(true, 2, base.NewLedgerTime(ts.Slot, 0))
		err = txb.AddEndorsement(rndEndorsement)
		util.AssertNoError(err)
		return txb
	}

	runTest := func(seqProfitMargin, maxProfitMargin uint16, generous bool, maxFreezeEpochs byte, prnTx bool) {
		name := fmt.Sprintf("seqProfit=%d, maxProfit=%d, generous=%v, maxFreezeEpochs=%d", seqProfitMargin, maxProfitMargin, generous, maxFreezeEpochs)
		t.Run(name, func(t *testing.T) {
			dIn := delegationInit(addr, seqID, maxProfitMargin, maxFreezeEpochs)
			//t.Logf("------------\n%s", dIn.LinesHR("    ").String())

			ts := base.MaximumTime(predTs.AddSlots(1), dIn.Timestamp().AddTicks(10))
			txb := newTxb(ts, seqProfitMargin, generous)

			succIdx, err := txb.FreezeDelegation(&dIn)
			require.NoError(t, err)

			txBytes, _, txString, errTx := txb.BytesWithValidation()
			if prnTx {
				if errTx != nil {
					t.Logf("------- ERROR: %v\n--------- failing tx --------\n%s", errTx, txString)
				} else {
					t.Logf("--------- valid tx --------\n%s", txString)
				}
			}
			require.NoError(t, errTx)

			tx, err := transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
			require.NoError(t, err)
			o := tx.MustProducedOutputAt(succIdx)
			oid := tx.OutputID(succIdx)
			dOut, ok := ledger.AsDelegationOutput(o, oid)
			require.True(t, ok)

			//t.Logf("\n%s", dOut.LinesSource("    ").String())
			_, _, frozenEpochs := dOut.FrozenEpochs(ts)
			inflationOneSlot := ledger.L().CalcChainInflationAmountOneSlot(ts.Slot, dIn.Output.TokenBalance())
			advance := dOut.Output.TokenBalance() - dIn.Output.TokenBalance() - inflationOneSlot
			t.Logf("advance = %s", util.Th(advance))
			dconst := ledger.DelegationConst()
			frozenSlots := dconst.FrozenSlotsFromFrozenEpochs(dOut.Target.ChainID(), uint32(dOut.ID.Slot()), byte(frozenEpochs))
			inflationContributionProjection := ledger.InflationProjection(dOut.Output.TokenBalance(), uint32(dOut.ID.Slot()), frozenSlots)
			profit := int64(inflationContributionProjection) - int64(advance)
			t.Logf("total profit earned by sequencer: %s (%.2f%%)", util.Th(profit), float64(profit*100)/float64(advance))
			t.Logf("total inflation projection earned by sequencer: %s", util.Th(inflationContributionProjection))
			t.Logf("profit by sequencer: %s (%.2f%%)", util.Th(profit), float64(profit*100)/float64(advance))

			require.NoError(t, errTx)
		})

	}
	runTest(0, 0, false, 4, false)
	runTest(0, 0, true, 4, false)
	runTest(0, 500, false, 4, false)
	runTest(0, 500, true, 4, false)
}
