package tests

import (
	"crypto/ed25519"
	"fmt"
	"testing"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/sequencer/seqdata"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/stretchr/testify/require"
)

// initializes ledger.Library singleton for all tests and creates testing genesis private key

//var genesisPrivateKey ed25519.PrivateKey

//func init() {
//	genesisPrivateKey = ledger.InitWithTestingLedgerIDData()
//}

func TestBase(t *testing.T) {
	privKey := testutil.GetTestingPrivateKey()
	addr := ledger.AddressED25519FromPrivateKey(privKey)
	seqID := base.RandomChainID()
	bal := uint64(1_000_000_000_000)

	sd := seqdata.New().
		SetName("test_seq").
		IncBranchHeight(2).
		IncChainHeight(4).
		SetMinimumFee(1)

	predTs := base.T(1000, 50)
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

	newTxb := func(ts base.LedgerTime, frozen ...int64) *txbuilder_seq.SeqTxBuilder {
		txb, err := txbuilder_seq.New(ts, newPredChain(frozen...), nil, privKey, multistate.DummyStateReader)
		require.NoError(t, err)
		rndEndorsement := base.RandomTransactionID(true, 2, base.T(ts.Slot, 0))
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
	t.Run("+100 slot simple tag-along", func(t *testing.T) {
		ts := predTs.AddSlots(99)
		tagAlongOut := ledger.OutputWithID{
			ID:     base.RandomOutputID(ts),
			Output: ledger.NewTagAlongOutput(200, seqID, ledger.AddressED25519FromPrivateKey(privKey)),
			//txbuilder_seq.NewWithdrawRequestOutput(seqID, ledger.AddressED25519FromPrivateKey(privKey), 200, 1_000_000, addr),
		}
		ts = ts.AddSlots(1)
		txb := newTxb(ts)
		_, _, err := txb.AddTagAlongInput(tagAlongOut)
		require.NoError(t, err)

		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
	t.Run("+1000 slot simple tag_along", func(t *testing.T) {
		ts := predTs.AddSlots(999)
		tagAlongOut := ledger.OutputWithID{
			ID:     base.RandomOutputID(ts),
			Output: ledger.NewTagAlongOutput(200, seqID, ledger.AddressED25519FromPrivateKey(privKey)),
		}

		ts = ts.AddSlots(1)
		txb := newTxb(ts, 1_000_000, 1_000_000, 1_000_000, 1_000_000)

		_, _, err := txb.AddTagAlongInput(tagAlongOut)
		require.NoError(t, err)

		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
	t.Run("simple tag_along fail window", func(t *testing.T) {
		ts := predTs.AddSlots(1000)
		tagAlongOut := ledger.OutputWithID{
			ID:     base.RandomOutputID(ts),
			Output: ledger.NewTagAlongOutput(200, seqID, ledger.AddressED25519FromPrivateKey(privKey)),
		}
		txb := newTxb(ts.AddSlots(ledger.L(0).TagAlongSlots), 1_000_000, 1_000_000, 1_000_000, 1_000_000)
		_, _, err := txb.AddTagAlongInput(tagAlongOut)
		require.NoError(t, util.MustErrorWith(err, "missed tag-along window"))

		txb = newTxb(ts.AddSlots(ledger.L(0).TagAlongSlots-1), 1_000_000, 1_000_000, 1_000_000, 1_000_000)
		_, _, err = txb.AddTagAlongInput(tagAlongOut)
		require.NoError(t, err)

		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})

	t.Run("+1000 slot withdraw", func(t *testing.T) {
		ts := predTs.AddSlots(999)
		tagAlongOut := ledger.OutputWithID{
			ID:     base.RandomOutputID(ts),
			Output: txbuilder_seq.NewWithdrawRequestOutput(seqID, ledger.AddressED25519FromPrivateKey(privKey), 200, 40_000_000, addr),
		}
		txb := newTxb(ts.AddSlots(1), 1_000_000, 1_000_000, 1_000_000, 1_000_000)
		_, _, err := txb.AddTagAlongInput(tagAlongOut)
		require.NoError(t, err)

		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
	t.Run("+1000 slot withdraw fail", func(t *testing.T) {
		ts := predTs.AddSlots(999)
		tagAlongOut := ledger.OutputWithID{
			ID:     base.RandomOutputID(ts),
			Output: txbuilder_seq.NewWithdrawRequestOutput(seqID, ledger.AddressED25519FromPrivateKey(privKey), 200, 40_000_000, addr),
		}
		txb := newTxb(ts.AddSlots(ledger.L(0).TagAlongSlots), 1_000_000, 1_000_000, 1_000_000, 1_000_000)
		_, _, err := txb.AddTagAlongInput(tagAlongOut)
		require.NoError(t, util.MustErrorWith(err, "missed tag-along window"))

		txb = newTxb(ts.AddSlots(ledger.L(0).TagAlongSlots-5), 1_000_000, 1_000_000, 1_000_000, 1_000_000)
		_, _, err = txb.AddTagAlongInput(tagAlongOut)
		require.NoError(t, err)

		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
	t.Run("many withdraw", func(t *testing.T) {
		ts := predTs.AddSlots(1000)
		txb := newTxb(ts.AddSlots(ledger.L(0).TagAlongSlots-1), 1_000_000, 1_000_000, 1_000_000, 1_000_000)

		sender := ledger.AddressED25519FromPrivateKey(privKey)
		rndWithdraw := func(amount uint64, slot uint32) error {
			tagAlongOut := ledger.OutputWithID{
				ID:     base.RandomOutputID(base.T(slot, 50)),
				Output: txbuilder_seq.NewWithdrawRequestOutput(seqID, sender, 200, 40_000_000, addr),
			}
			_, _, err := txb.AddTagAlongInput(tagAlongOut)
			return err
		}

		const howMany = 29 // 254
		for i := 0; i < howMany; i++ {
			err := rndWithdraw(10_000_000, ts.Slot+uint32(i))
			require.NoError(t, err)
		}

		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
	t.Run("change seq name", func(t *testing.T) {
		ts := predTs.AddSlots(1000)
		txb := newTxb(ts.AddSlots(3), 1_000_000, 1_000_000, 1_000_000, 1_000_000)
		predSeqData, err := ledger.ParseSequencerData(txb.ChainInput().Output)
		require.NoError(t, err)

		tagAlongOut := ledger.OutputWithID{
			ID: base.RandomOutputID(base.T(ts.Slot, 50)),
			Output: txbuilder_seq.NewSeqDataCommandOutput(seqID, ledger.AddressED25519FromPrivateKey(privKey), 200, predSeqData.Clone(func(sdUpdated *seqdata.SequencerData) {
				sdUpdated.SetName("newName").IncChainHeight()
			})),
		}

		_, _, err = txb.AddTagAlongInput(tagAlongOut)
		require.NoError(t, err)

		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
}

func delegationInit(master ledger.Accountable, seqID base.ChainID, startSlot uint32, maxSeqProfitMargin uint16, maxFreezeEpochs ...byte) ledger.DelegationOutput {
	maxEpochs := byte(ledger.L(0).MaxFrozenEpochs)
	if len(maxFreezeEpochs) > 0 {
		maxEpochs = maxFreezeEpochs[0]
	}
	ret := ledger.MakeDelegationInitOutput(ledger.MakeDelegateInitOutputParams{
		Amount:             1_000_000_000,
		Master:             master,
		Target:             ledger.ChainLockFromChainID(seqID),
		MaxFreezeEpochs:    maxEpochs,
		MaxSeqProfitMargin: maxSeqProfitMargin,
		StartSlot:          startSlot,
	})
	delegationInitOid := base.MustNewOutputID(base.RandomTransactionID(false, 2, base.T(startSlot, 50)), 1)

	dout, ok := ledger.AsDelegationOutput(ret, delegationInitOid)
	util.Assertf(ok, "AsDelegationOutput")
	return dout
}

func TestFreezeOneStep(t *testing.T) {
	privKey := testutil.GetTestingPrivateKey()
	addr := ledger.AddressED25519FromPrivateKey(privKey)
	seqID := base.RandomChainID()
	seqInitBalance := ledger.L(0).MinimumAmountOnSequencer << 8

	predTs := base.T(1000, 50)
	predID := base.MustNewOutputID(base.RandomTransactionID(true, 2, predTs), 0)

	newPredChain := func(requiredSeqProfitMargin uint16, greedy bool, frozen ...int64) *ledger.OutputWithChainID {
		amounts := append([]int64{int64(seqInitBalance), 0}, frozen...)

		sd := seqdata.New().
			SetName("test_seq").
			IncBranchHeight(2).
			IncChainHeight(4).
			SetMinimumFee(1).
			SetSeqProfitMarginPromille(requiredSeqProfitMargin).
			SetGreedy(greedy)

		predChain := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(amounts...).WithLock(addr)
			ccIdx := o.MustPushConstraint(ledger.NewChainConstraint(seqID, 0, 2, 1000, seqInitBalance).Bytes())
			_ = o.MustPushConstraint(ledger.NewSequencerConstraint(ccIdx).Bytes())

			_ = o.MustPushConstraint(easyfl.InlineDataBytecode(sd.Bytes()))
		})

		pred, ok := ledger.AsOutputWithChainID(predChain, predID)
		util.Assertf(ok, "AsOutputWithChainID")
		return &pred
	}

	newTxb := func(ts base.LedgerTime, seqProfitMargin uint16, greedy bool, frozen ...int64) *txbuilder_seq.SeqTxBuilder {
		txb, err := txbuilder_seq.New(ts, newPredChain(seqProfitMargin, greedy, frozen...), nil, privKey, multistate.DummyStateReader)
		util.AssertNoError(err)
		rndEndorsement := base.RandomTransactionID(true, 2, base.T(ts.Slot, 0))
		err = txb.AddEndorsement(rndEndorsement)
		util.AssertNoError(err)
		return txb
	}

	runTest := func(startSlot uint32, seqProfitMargin, inflationShareByDelegator uint16, greedy bool, maxFreezeEpochs byte, prnTx bool) (errTest error) {
		name := fmt.Sprintf("seqProfit=%d inflationShare=%d greedy=%v maxFreezeEpochs=%d", seqProfitMargin, inflationShareByDelegator, greedy, maxFreezeEpochs)
		t.Run(name, func(t *testing.T) {
			dIn := delegationInit(addr, seqID, startSlot, inflationShareByDelegator, maxFreezeEpochs)
			//t.Logf("------------\n%s", dIn.LinesHR("    ").String())

			ts := base.MaximumTime(predTs.AddSlots(1), dIn.Timestamp().AddSlots(1))
			txb := newTxb(ts, seqProfitMargin, greedy)

			succIdx, _, err := txb.FreezeDelegation(&dIn)
			if err != nil {
				errTest = err
				return
			}

			txBytes, _, txString, errTx := txb.BytesWithValidation()
			if prnTx {
				if errTx != nil {
					t.Logf("------- ERROR: %v\n--------- failing tx --------\n%s", errTx, txString)
				} else {
					t.Logf("--------- valid tx --------\n%s", txString)
				}
			}
			if errTx != nil {
				errTest = err
				return
			}

			tx, err := transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
			if err != nil {
				errTest = err
				return
			}
			o := tx.MustProducedOutputAt(succIdx)
			oid := tx.OutputID(succIdx)
			dOut, ok := ledger.AsDelegationOutput(o, oid)
			require.True(t, ok)

			// TODO something is fishy. Seq profit projections slightly inconsistent (too good) compared to the produced output

			// calculate profitability of freezing
			inflationOneSlotDelegator := ledger.ChainInflationOneSlot(dIn.Output.TokenBalance(), dIn.ID.Slot())
			advance := dOut.Output.TokenBalance() - dIn.Output.TokenBalance() - inflationOneSlotDelegator

			t.Logf("delegation init ID: %s", dIn.ID.String())
			_, _, frozenSlots := dOut.FrozenSlots()
			t.Logf("frozen slots: %d", frozenSlots)
			t.Logf("advance     : %s", util.Th(advance))
			frozenAmount := dIn.Output.TokenBalance() + inflationOneSlotDelegator
			t.Logf("frozen amount: %s", util.Th(frozenAmount))

			inflationOneSlotSeq := ledger.ChainInflationOneSlot(seqInitBalance, predTs.Slot)
			seqInflatableBalanceDoNothing := seqInitBalance + inflationOneSlotSeq
			seqInflationDoNothing := ledger.ChainInflation(seqInflatableBalanceDoNothing, ts.Slot, frozenSlots)
			seqInflatableBalanceFreeze := seqInitBalance + inflationOneSlotSeq - advance + frozenAmount
			seqInflationFreeze := ledger.ChainInflation(seqInflatableBalanceFreeze, ts.Slot, frozenSlots)
			repaymentSeq := int64(seqInflationFreeze) - int64(seqInflationDoNothing)
			roiSeq := repaymentSeq - int64(advance)
			roiSeqPercent := 100 * (float64(roiSeq) / float64(advance))

			t.Logf("inflatable balance do nothing: %s, inflation: %s", util.Th(seqInflatableBalanceDoNothing), util.Th(seqInflationDoNothing))
			t.Logf("    inflatable balance freeze: %s, inflation: %s", util.Th(seqInflatableBalanceFreeze), util.Th(seqInflationFreeze))
			t.Logf("Repayment: %s", util.Th(repaymentSeq))
			t.Logf("Projected profit by sequencer: %s (%.2f%% of advance)", util.Th(roiSeq), roiSeqPercent)
		})
		return
	}
	var err error
	err = runTest(0, 0, 1000, true, 4, false)
	require.NoError(t, err)
	err = runTest(0, 0, 1000, false, 4, false)
	require.NoError(t, err)
	err = runTest(0, 0, 500, true, 4, false)
	require.NoError(t, err)
	err = runTest(0, 0, 500, false, 4, false)
	require.NoError(t, err)
	err = runTest(0, 10, 980, true, 4, false)
	require.NoError(t, err)
	err = runTest(0, 10, 980, false, 4, false)
	require.NoError(t, err)
	err = runTest(0, 10, 990, false, 4, false)
	require.NoError(t, err)
	err = runTest(0, 5, 995, false, 4, false)
	require.NoError(t, err)
	err = runTest(1000000, 0, 995, false, 4, false)
	require.NoError(t, err)
	err = runTest(1000000, 0, 995, true, 4, false)
	require.NoError(t, err)
	err = runTest(1000000, 0, 0, false, 4, false)
	require.NoError(t, err)
	err = runTest(1000000, 0, 0, true, 4, false)
	require.NoError(t, err)
	err = runTest(0, 10, 990, false, 4, false)
	require.NoError(t, err)
	err = runTest(0, 10, 991, false, 4, false)
	require.NoError(t, util.MustErrorWith(err, "advance required by delegator is loss-making for the sequencer"))
	err = runTest(100_000_000, 10, 990, false, 4, false)
	require.NoError(t, err)
	err = runTest(100_000_000, 10, 991, false, 4, false)
	require.NoError(t, util.MustErrorWith(err, "advance required by delegator is loss-making for the sequencer"))
	err = runTest(100_000_000, 2, 998, false, 4, false)
	require.NoError(t, err)
	err = runTest(100_000_000, 2, 999, false, 4, false)
	require.NoError(t, util.MustErrorWith(err, "advance required by delegator is loss-making for the sequencer"))
	err = runTest(100_000_000, 1, 999, false, 4, false)
	require.NoError(t, err)
	err = runTest(100_000_000, 0, 1000, false, 4, false)
	require.NoError(t, err)
}

type testFreezeMultipleStepsParams struct {
	howManySteps              int
	numDelegations            int
	startSlot                 uint32
	seqProfitMargin           uint16
	inflationShareByDelegator uint16
	greedy                    bool
	maxFreezeEpochs           byte
	prnTx                     bool
	prnEpochStats             bool
	prnSlotStats              bool
}

type _epochStats struct {
	epoch           uint32
	firstSlot       uint32
	nSeqSteps       int
	nFreezes        int
	amountsSeqStart ledger.Amounts
	maxTxBytes      int
}

func TestFreezeMultipleSteps(t *testing.T) {
	privKey := testutil.GetTestingPrivateKey()
	addr := ledger.AddressED25519FromPrivateKey(privKey)
	seqID := base.RandomChainID()
	seqInitBalance := ledger.L(0).MinimumAmountOnSequencer << 8

	predTs := base.T(1000, 50)
	predID := base.MustNewOutputID(base.RandomTransactionID(true, 2, predTs), 0)

	newPredChain := func(requiredSeqProfitMargin uint16, greedy bool) *ledger.OutputWithChainID {
		sd := seqdata.New().
			SetName("test_seq").
			IncBranchHeight(2).
			IncChainHeight(4).
			SetMinimumFee(1).
			SetSeqProfitMarginPromille(requiredSeqProfitMargin).
			SetGreedy(greedy)

		predChain := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(int64(seqInitBalance)).WithLock(addr)
			ccIdx := o.MustPushConstraint(ledger.NewChainConstraint(seqID, 0, 2, 1000, seqInitBalance).Bytes())
			_ = o.MustPushConstraint(ledger.NewSequencerConstraint(ccIdx).Bytes())
			_ = o.MustPushConstraint(easyfl.InlineDataBytecode(sd.Bytes()))
		})

		pred, ok := ledger.AsOutputWithChainID(predChain, predID)
		util.Assertf(ok, "AsOutputWithChainID")
		return &pred
	}

	newTxb := func(predChain *ledger.OutputWithChainID, ts base.LedgerTime, seqProfitMargin uint16, greedy bool) *txbuilder_seq.SeqTxBuilder {
		txb, err := txbuilder_seq.New(ts, predChain, nil, privKey, multistate.DummyStateReader)
		util.AssertNoError(err)
		rndEndorsement := base.RandomTransactionID(true, 2, base.T(ts.Slot, 0))
		err = txb.AddEndorsement(rndEndorsement)
		util.AssertNoError(err)
		return txb
	}

	var delegations []ledger.DelegationOutput

	runTest := func(par testFreezeMultipleStepsParams) (errTest error) {
		delegations = make([]ledger.DelegationOutput, par.numDelegations)
		for i := range delegations {
			maxFreeze := byte(i) % par.maxFreezeEpochs
			delegations[i] = delegationInit(addr, seqID, par.startSlot+uint32(i), par.inflationShareByDelegator, maxFreeze)
		}
		seqOut := newPredChain(par.seqProfitMargin, par.greedy)
		var ok bool

		ts := base.T(par.startSlot+uint32(par.numDelegations), 50)
		var txSlot uint32
		var epochStats *_epochStats

		for step := 0; step < par.howManySteps; step++ {
			ts = ts.AddSlots(1)
			txSlot = ts.Slot
			epoch := ledger.L(0).EpochFromSlotDirect(seqID, txSlot)
			if epochStats == nil || epochStats.epoch != epoch {
				if epochStats != nil && par.prnEpochStats {
					a := seqOut.Output.TokenBalance()
					b := epochStats.amountsSeqStart.TokenBalance()
					var earnings uint64
					if a > b {
						earnings = a - b
					}
					t.Logf("#%3d (%3d)  start slot: %5d freezes: %3d max txBytes: %5d earned: %s   %s",
						epochStats.epoch, epochStats.nSeqSteps, epochStats.firstSlot, epochStats.nFreezes, epochStats.maxTxBytes, util.Th(earnings), epochStats.amountsSeqStart.String())
				}
				epochStats = &_epochStats{
					epoch:           epoch,
					amountsSeqStart: seqOut.Output.Amounts(),
					firstSlot:       txSlot,
				}
			}
			epochStats.nSeqSteps++

			txb := newTxb(seqOut, ts, par.seqProfitMargin, par.greedy)

			unlockableIndices := make([]int, 0)
			nInWindow := 0
			for i := range delegations {
				if delegations[i].IsUnlockableByTargetForFreezing(ts.Slot) {
					unlockableIndices = append(unlockableIndices, i)
				}
				if delegations[i].IsInSafeRevocationWindow(ts.Slot) {
					nInWindow++
				}
			}

			for _, j := range unlockableIndices {
				_, _, err := txb.FreezeDelegation(&delegations[j])
				if err != nil {
					errTest = err
					return
				}
				epochStats.nFreezes++
			}
			txBytes, txid, txString, errTx := txb.BytesWithValidation()

			if epochStats.maxTxBytes < len(txBytes) {
				epochStats.maxTxBytes = len(txBytes)
			}
			if errTx != nil {
				t.Logf("------- ERROR: %v\n--------- failing tx --------\n%s", errTx, txString)
			}
			if errTx != nil {
				errTest = errTx
				return
			}

			tx, err := transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
			if err != nil {
				errTest = err
				return
			}
			for i, j := range unlockableIndices {
				delegations[j], ok = ledger.AsDelegationOutput(tx.MustProducedOutputAt(byte(i)), tx.OutputID(byte(i)))
				require.True(t, ok)
			}

			seqOutIdx := byte(len(unlockableIndices))
			seqOutTmp, ok := ledger.AsOutputWithChainID(tx.MustProducedOutputAt(seqOutIdx), tx.OutputID(seqOutIdx))
			require.True(t, ok)
			seqOut = &seqOutTmp
			if par.prnSlotStats {
				t.Logf("%4d -- %s freeze: %v, safed: %d amounts: %s", step, txid.StringShort(), len(unlockableIndices) > 0, nInWindow, seqOut.Output.Amounts().String())
				for _, j := range unlockableIndices {
					t.Logf("            freeze %d, amounts: %s", j, delegations[j].Output.Amounts().String())
					if par.prnTx {
						t.Logf("\n--------- freeze tx --------\n%s", txString)
					}
				}
			}
		}
		return
	}
	var err error
	par := testFreezeMultipleStepsParams{
		howManySteps:              2000,
		numDelegations:            254,
		startSlot:                 10000,
		seqProfitMargin:           20,
		inflationShareByDelegator: 980,
		greedy:                    false,
		maxFreezeEpochs:           4,
		prnTx:                     false,
		prnSlotStats:              false,
		prnEpochStats:             true,
	}
	err = runTest(par)
	require.NoError(t, err)

}

type (
	testWithUTXODBData struct {
		*testing.T
		masterPrivateKey ed25519.PrivateKey
		masterAddr       ledger.AddressED25519
		targetPrivateKey ed25519.PrivateKey
		targetAddr       ledger.AddressED25519
		u                *utxodb.UTXODB
		seqID            base.ChainID
		delegationIDs    []base.ChainID
		revokeRequests   set.Set[base.ChainID]
	}
)

func newTestWithUTXODBData(t *testing.T, nDelegations int) (*testWithUTXODBData, base.LedgerTime) {
	u := utxodb.NewUTXODB(genesisPrivateKey)
	seqInitBalance := ledger.L(0).MinimumAmountOnSequencer << 8
	pk, _, addrs := u.GenerateAddressesWithFaucetAmount(314, 2, seqInitBalance*2)
	ret := &testWithUTXODBData{
		T:                t,
		u:                u,
		masterPrivateKey: pk[0],
		masterAddr:       addrs[0],
		targetPrivateKey: pk[1],
		targetAddr:       addrs[1],
		delegationIDs:    make([]base.ChainID, nDelegations),
		revokeRequests:   set.New[base.ChainID](),
	}
	initTs := base.T(1000, 50)

	// sequencer chain origin
	seqChainOrig, err := ret.u.CreateChainOrigin(ret.targetPrivateKey, initTs, seqInitBalance)
	require.NoError(t, err)
	ret.seqID = seqChainOrig.ChainID
	t.Logf("seqID: %s", ret.seqID.String())
	ts := seqChainOrig.ID.Timestamp().AddSlots(1)
	txbSeq, err := txbuilder_seq.New(ts, seqChainOrig, nil, ret.targetPrivateKey, nil)
	require.NoError(t, err)
	rndTxid := base.RandomTransactionID(true, 2, base.T(ts.Slot, 0))
	err = txbSeq.AddEndorsement(rndTxid)
	require.NoError(t, err)
	txBytes, _, _, err := txbSeq.BytesWithValidation()
	require.NoError(t, err)
	err = ret.u.AddTransaction(txBytes)
	require.NoError(t, err)

	const delegatedAmount = 1_000_000_000

	for i := range ret.delegationIDs {
		out, err := ret.u.CreateChainOrigin(ret.masterPrivateKey, initTs.AddSlots(uint32(i)), delegatedAmount)
		require.NoError(t, err)

		ret.delegationIDs[i] = out.ChainID
		//t.Logf("delegation %d: %s", i, out.ChainID.String())
	}

	txb := txbuilder.New()
	var ts1 base.LedgerTime
	for i := range ret.delegationIDs {
		out := ret.delegationChain(i)
		ts1 = base.MaximumTime(ts1, out.ID.Timestamp())

		_, err = txb.ConsumeOutput(out.Output, out.ID)
		require.NoError(t, err)
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			err = txb.PutUnlockReference(byte(i), 1, 0)
			require.NoError(t, err)
		}
		txb.PutUnlockParams(byte(i), 2, ledger.NewChainUnlockParams(byte(i), 2))

		maxFreezeEpochs := byte(uint32(i)%ledger.L(0).MaxFrozenEpochs + 1)

		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(out.Output.Amounts()...)
			delegateLock := ledger.NewDelegateLock(ledger.ChainLockFromChainID(ret.seqID), ret.masterAddr, maxFreezeEpochs, 980)
			o.WithLock(delegateLock)
			o.MustPushConstraint(ledger.NewChainConstraint(out.ChainID, byte(i), 2, out.OriginSlot, out.OriginAmount).Bytes())
			o.MustPushConstraint(ledger.DelegateLockState{}.Bytes())
		}))
		require.NoError(t, err)
	}
	txb.TransactionData.Timestamp = ts1.AddSlots(1)
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(ret.masterPrivateKey)

	var txString string
	txBytes, _, txString, err = txb.BytesWithValidation()
	if err != nil {
		t.Logf("--------- failing tx --------------\n%s", txString)
	}
	require.NoError(t, err)

	err = u.AddTransaction(txBytes)
	require.NoError(t, err)

	return ret, ts
}

func (td *testWithUTXODBData) seqOutput() *ledger.OutputWithChainID {
	ret, err := td.u.SugaredStateReader().GetChainOutputWithChainID(td.seqID)
	util.AssertNoError(err)
	return &ret
}

func (td *testWithUTXODBData) delegationChain(i int) *ledger.OutputWithChainID {
	ret, err := td.u.SugaredStateReader().GetChainOutputWithChainID(td.delegationIDs[i])
	util.AssertNoError(err)
	return &ret
}

func (td *testWithUTXODBData) freezableDelegations(ts base.LedgerTime) []ledger.DelegationOutput {
	ret, err := td.u.SugaredStateReader().GetDelegationsForSequencer(td.seqID, func(o *ledger.DelegationOutput) bool {
		return o.IsUnlockableByTargetForFreezing(ts.Slot)
	})
	util.AssertNoError(err)
	return ret
}

// epoch -> which (delegatio idx, relative slot) to be revokeRequests

var _revokeSchedule = map[uint32][]struct{ d, s uint32 }{
	4:  {{1, 40}},
	5:  {{5, 10}, {3, 35}},
	6:  {{3, 10}},
	8:  {{2, 5}},
	10: {{4, 50}, {6, 60}},
	12: {{1, 40}},
}

// postRevokeRequestsInEpoch creates revocation requests
func (td *testWithUTXODBData) postRevokeRequestsInEpoch(slot uint32) int {
	epoch := ledger.L(0).EpochFromSlotDirect(td.seqID, slot)
	lst, ok := _revokeSchedule[epoch]
	if !ok {
		return 0
	}
	firstSlot, _ := ledger.L(0).EpochLimits(td.seqID, epoch)
	nrSlotInEpoch := slot - firstSlot + 1
	nRequests := 0
	for i := range lst {
		if lst[i].s != nrSlotInEpoch {
			continue
		}
		nRequests++
		did := td.delegationIDs[lst[i].d]

		askStopRequestOutput := txbuilder_seq.NewAskStopDelegationReqOutput(td.seqID, td.masterAddr, did, 500)
		err := td.u.SendOutput(td.masterPrivateKey, askStopRequestOutput, base.T(slot, 50))
		if err != nil {
			println()
		}
		util.AssertNoError(err)
		td.revokeRequests.Insert(did)
		td.Logf("post revoke request for %s epoch %d, slot %d, slot in epoch: %d", did.StringShort(), epoch, slot, nrSlotInEpoch)
	}
	return nRequests
}

func (td *testWithUTXODBData) tagAlongBacklog() []ledger.OutputWithID {
	ret, err := td.u.SugaredStateReader().GetTagAlongBacklogForSequencer(td.seqID)
	util.AssertNoError(err)
	return ret
}

func TestWithUTXODB(t *testing.T) {
	const (
		numDelegations = 10
		howManySteps   = 8030
	)
	td, ts := newTestWithUTXODBData(t, numDelegations)
	//t.Logf("----- seq init:\n%s", td.seqOutput().LinesHR("   ").String())

	//var txSize int
	var stats *_epochStats
	revokeRequests := 0

	blacklist := set.New[base.OutputID]()

	for i := 0; i < howManySteps; i++ {
		rdr := td.u.SugaredStateReader()
		seqOut, err := rdr.GetChainOutputWithID(td.seqID)
		require.NoError(t, err)

		ts = ts.AddSlots(1)
		txSlot := ts.Slot

		epoch := ledger.L(0).EpochFromSlotDirect(td.seqID, ts.Slot)
		if stats == nil || epoch != stats.epoch {
			if stats != nil {
				t.Logf("%4d (%5d + %3d slots), freezes: %3d   maxTx: %5d    %s",
					stats.epoch, stats.firstSlot, stats.nSeqSteps, stats.nFreezes, stats.maxTxBytes, stats.amountsSeqStart.String())
			}
			stats = &_epochStats{
				epoch:           epoch,
				firstSlot:       txSlot,
				amountsSeqStart: seqOut.Output.Amounts(),
			}
		}
		stats.nSeqSteps++
		freezable := td.freezableDelegations(ts)

		txb, err := txbuilder_seq.NewWithSequencerID(ts, td.seqID, td.targetPrivateKey, rdr)
		require.NoError(t, err)
		err = txb.AddEndorsement(base.RandomTransactionID(true, 2, base.T(ts.Slot, 0)))
		require.NoError(t, err)

		// non-deterministic
		for _, dIn := range freezable {
			_, _, err = txb.FreezeDelegation(&dIn)
			require.NoError(t, err)
			stats.nFreezes++
		}

		tagAlongBacklog := td.tagAlongBacklog()
		for _, o := range tagAlongBacklog {
			if blacklist.Contains(o.ID) {
				continue
			}
			_, valid, err := txb.AddTagAlongInput(o)
			if !valid || err != nil {
				if !valid {
					t.Logf("   %s PERMANENTLY cannot add tag-along, reason = '%v'", o.ID.StringShort(), err)
					blacklist.Insert(o.ID)
				} else {
					t.Logf("   %s TEMPORARY cannot add tag-along, reason = '%v'", o.ID.StringShort(), err)
				}
			} else {
				t.Logf("   %s tag-along output has been added", o.ID.StringShort())
			}
		}
		txBytes, _, txString, err := txb.BytesWithValidation()
		if err != nil {
			t.Logf("--------- failing tx --------------\n%s", txString)
		}
		require.NoError(t, err)
		err = td.u.AddTransaction(txBytes)
		if stats.maxTxBytes < len(txBytes) {
			stats.maxTxBytes = len(txBytes)
		}

		revokeRequests += td.postRevokeRequestsInEpoch(txSlot)
	}
	numTotalDelegations := 0
	numRevoked := 0
	numSafeRevocation := 0
	ts = ts.AddSlots(1)
	t.Logf("-------------%s -----------", ts.String())
	td.u.SugaredStateReader().IterateDelegatedOutputs(td.seqID, func(o *ledger.DelegationOutput) bool {
		numTotalDelegations++
		if o.IsMarkedOnHold() {
			numRevoked++
		}
		if o.IsInSafeRevocationWindow(ts.Slot) {
			numSafeRevocation++
		}
		t.Logf("   %s  %s  revokeRequests: %v,  safe: %v,  marked frozen: %v,  feezable: %v",
			o.ChainID.StringShort(), o.ID.StringShort(), o.IsMarkedOnHold(),
			o.IsInSafeRevocationWindow(ts.Slot), o.IsMarkedFrozen(), o.IsUnlockableByTargetForFreezing(ts.Slot))

		if o.IsMarkedOnHold() {
			require.True(t, td.revokeRequests.Contains(o.ChainID))
		}
		return true
	})
	t.Logf(`---------------------------
     total delegations :     %d
     revoked:                %d
     revoke requests:        %d
     safe revocation window: %d`, numTotalDelegations, numRevoked, revokeRequests, numSafeRevocation)

	//require.EqualValues(t, numRevoked, revokeRequests)
	//require.EqualValues(t, numRevoked, len(td.revokeRequests))
}
