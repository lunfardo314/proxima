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
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/ledger/utxodb"
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

func delegationInit(master ledger.Accountable, seqID base.ChainID, startSlot uint32, maxSeqProfitMargin uint16, maxFreezeEpochs ...byte) ledger.DelegationOutput {
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
		StartSlot:          base.Slot(startSlot),
	})
	delegationInitOid := base.MustNewOutputID(base.RandomTransactionID(false, 2, base.NewLedgerTime(base.Slot(startSlot), 50)), 1)

	dout, ok := ledger.AsDelegationOutput(ret, delegationInitOid)
	util.Assertf(ok, "AsDelegationOutput")
	return dout
}

func TestFreezeOneStep(t *testing.T) {
	privKey := testutil.GetTestingPrivateKey()
	addr := ledger.AddressED25519FromPrivateKey(privKey)
	seqID := base.RandomChainID()
	seqInitBalance := ledger.L().ID.MinimumAmountOnSequencer << 8

	predTs := base.NewLedgerTime(1000, 50)
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

	newTxb := func(ts base.LedgerTime, seqProfitMargin uint16, greedy bool, frozen ...int64) *SeqTxBuilder {
		txb, err := New(ts, newPredChain(seqProfitMargin, greedy, frozen...), nil, privKey, multistate.DummyStateReader)
		util.AssertNoError(err)
		rndEndorsement := base.RandomTransactionID(true, 2, base.NewLedgerTime(ts.Slot, 0))
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

			succIdx, err := txb.FreezeDelegation(&dIn)
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
			inflationOneSlotDelegator := ledger.L().ChainInflationOneSlot(dIn.Output.TokenBalance(), uint32(dIn.ID.Slot()))
			advance := dOut.Output.TokenBalance() - dIn.Output.TokenBalance() - inflationOneSlotDelegator

			t.Logf("delegation init ID: %s", dIn.ID.String())
			_, _, frozenSlots := dOut.FrozenSlots()
			t.Logf("frozen slots: %d", frozenSlots)
			t.Logf("advance     : %s", util.Th(advance))
			frozenAmount := dIn.Output.TokenBalance() + inflationOneSlotDelegator
			t.Logf("frozen amount: %s", util.Th(frozenAmount))

			inflationOneSlotSeq := ledger.L().ChainInflationOneSlot(seqInitBalance, uint32(predTs.Slot))
			seqInflatableBalanceDoNothing := seqInitBalance + inflationOneSlotSeq
			seqInflationDoNothing := ledger.L().ChainInflation(seqInflatableBalanceDoNothing, uint32(ts.Slot), frozenSlots)
			seqInflatableBalanceFreeze := seqInitBalance + inflationOneSlotSeq - advance + frozenAmount
			seqInflationFreeze := ledger.L().ChainInflation(seqInflatableBalanceFreeze, uint32(ts.Slot), frozenSlots)
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
	seqInitBalance := ledger.L().ID.MinimumAmountOnSequencer << 8

	predTs := base.NewLedgerTime(1000, 50)
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

	newTxb := func(predChain *ledger.OutputWithChainID, ts base.LedgerTime, seqProfitMargin uint16, greedy bool) *SeqTxBuilder {
		txb, err := New(ts, predChain, nil, privKey, multistate.DummyStateReader)
		util.AssertNoError(err)
		rndEndorsement := base.RandomTransactionID(true, 2, base.NewLedgerTime(ts.Slot, 0))
		err = txb.AddEndorsement(rndEndorsement)
		util.AssertNoError(err)
		return txb
	}

	dconst := ledger.DelegationConst()
	var delegations []ledger.DelegationOutput

	runTest := func(par testFreezeMultipleStepsParams) (errTest error) {
		delegations = make([]ledger.DelegationOutput, par.numDelegations)
		for i := range delegations {
			maxFreeze := byte(i) % par.maxFreezeEpochs
			delegations[i] = delegationInit(addr, seqID, par.startSlot+uint32(i), par.inflationShareByDelegator, maxFreeze)
		}
		seqOut := newPredChain(par.seqProfitMargin, par.greedy)
		var ok bool

		ts := base.NewLedgerTime(base.Slot(par.startSlot)+base.Slot(par.numDelegations), 50)
		var txSlot uint32
		var epochStats *_epochStats

		for step := 0; step < par.howManySteps; step++ {
			ts = ts.AddSlots(1)
			txSlot = uint32(ts.Slot)
			epoch := dconst.EpochFromSlotDirect(seqID, txSlot)
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
				if delegations[i].IsUnlockableByTargetForFreezing(uint32(ts.Slot)) {
					unlockableIndices = append(unlockableIndices, i)
				}
				if delegations[i].IsInSafeRevocationWindow(uint32(ts.Slot)) {
					nInWindow++
				}
			}

			for _, j := range unlockableIndices {
				_, err := txb.FreezeDelegation(&delegations[j])
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
		privKey       ed25519.PrivateKey
		addr          ledger.AddressED25519
		u             *utxodb.UTXODB
		seqID         base.ChainID
		delegationIDs []base.ChainID
	}
)

func newTestWithUTXODBData(t *testing.T, nDelegations int) (*testWithUTXODBData, base.LedgerTime) {
	u := utxodb.NewUTXODB(genesisPrivateKey)
	seqInitBalance := ledger.L().ID.MinimumAmountOnSequencer << 8
	pk, _, addrs := u.GenerateAddressesWithFaucetAmount(314, 1, seqInitBalance*2)
	ret := &testWithUTXODBData{
		T:             t,
		u:             u,
		privKey:       pk[0],
		addr:          addrs[0],
		delegationIDs: make([]base.ChainID, nDelegations),
	}
	initTs := base.NewLedgerTime(1000, 50)

	// sequencer chain origin
	seqChainOrig, err := ret.u.CreateChainOrigin(ret.privKey, initTs, seqInitBalance)
	require.NoError(t, err)
	ret.seqID = seqChainOrig.ChainID
	t.Logf("seqID: %s", ret.seqID.String())
	ts := seqChainOrig.ID.Timestamp().AddSlots(1)
	txbSeq, err := New(ts, seqChainOrig, nil, ret.privKey, nil)
	require.NoError(t, err)
	rndTxid := base.RandomTransactionID(true, 2, base.NewLedgerTime(ts.Slot, 0))
	err = txbSeq.AddEndorsement(rndTxid)
	require.NoError(t, err)
	txBytes, _, _, err := txbSeq.BytesWithValidation()
	require.NoError(t, err)
	err = ret.u.AddTransaction(txBytes)
	require.NoError(t, err)

	const delegatedAmount = 1_000_000_000

	for i := range ret.delegationIDs {
		out, err := ret.u.CreateChainOrigin(ret.privKey, initTs.AddSlots(base.Slot(i)), delegatedAmount)
		require.NoError(t, err)

		ret.delegationIDs[i] = out.ChainID
		t.Logf("delegation %d: %s", i, out.ChainID.String())
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

		maxFreezeEpochs := byte(uint32(i)%ledger.DelegationConst().MaxFrozenEpochs + 1)

		_, _ = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(out.Output.Amounts()...)
			delegateLock := ledger.NewDelegateLock(ledger.ChainLockFromChainID(ret.seqID), ret.addr, maxFreezeEpochs, 980)
			o.WithLock(delegateLock)
			o.MustPushConstraint(ledger.NewChainConstraint(out.ChainID, byte(i), 2, out.OriginSlot, out.OriginAmount).Bytes())
			o.MustPushConstraint(ledger.DelegateLockState{}.Bytes())
		}))
	}
	txb.TransactionData.Timestamp = ts1.AddSlots(1)
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(ret.privKey)

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

func (td *testWithUTXODBData) freezableDelegations(ts base.LedgerTime, rdr multistate.SugaredStateReader) []ledger.DelegationOutput {
	ret := make([]ledger.DelegationOutput, 0)
	for _, did := range td.delegationIDs {
		o, err := rdr.GetChainOutputWithChainID(did)
		require.NoError(td, err)
		dOut, ok := ledger.AsDelegationOutput(o.Output, o.ID)
		require.True(td, ok)
		if dOut.IsUnlockableByTargetForFreezing(uint32(ts.Slot)) {
			ret = append(ret, dOut)
		}
	}
	return ret
}

func TestWithUTXODB(t *testing.T) {
	const (
		numDelegations = 10
		howManySteps   = 4000
	)
	td, ts := newTestWithUTXODBData(t, numDelegations)
	//t.Logf("----- seq init:\n%s", td.seqOutput().LinesHR("   ").String())

	var txSize int

	for i := 0; i < howManySteps; i++ {
		rdr := td.u.SugaredStateReader()
		seqOut, err := rdr.GetChainOutputWithID(td.seqID)
		require.NoError(t, err)

		ts = ts.AddSlots(1)
		freezable := td.freezableDelegations(ts, rdr)
		freeze := ""
		if len(freezable) > 0 {
			freeze = fmt.Sprintf("   <- freeze %d", len(freezable))
		}
		t.Logf("%4d %s seq balance: %s, txSize: %d txTs: %s %s",
			i, seqOut.ID.StringShort(), util.Th(seqOut.Output.TokenBalance()), txSize, ts.String(), freeze)

		txb, err := NewWithSequencerID(ts, td.seqID, td.privKey, rdr)
		require.NoError(t, err)
		err = txb.AddEndorsement(base.RandomTransactionID(true, 2, base.NewLedgerTime(ts.Slot, 0)))
		require.NoError(t, err)

		for _, dIn := range freezable {
			_, err = txb.FreezeDelegation(&dIn)
			require.NoError(t, err)

			t.Logf("    freeze %s (%s) -- %s", dIn.ID.StringShort(), util.Th(dIn.Output.TokenBalance()), dIn.ChainID.StringShort())
		}

		txBytes, _, txString, err := txb.BytesWithValidation()
		if err != nil {
			t.Logf("--------- failing tx --------------\n%s", txString)
		}
		require.NoError(t, err)
		err = td.u.AddTransaction(txBytes)
		txSize = len(txBytes)
	}

}
