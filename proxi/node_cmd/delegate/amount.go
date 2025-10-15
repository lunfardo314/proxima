package delegate

import (
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// TODO implement random delegation target option

var (
	targetChainIDStr string
	maxFreezeEpochs  uint8
)

func initDelegateAmountCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "amount <amount> [flags]",
		Short: `delegates amount to the target sequencer by creating delegation chain output`,
		Args:  cobra.ExactArgs(1),
		Run:   runDelegateAmountCmd,
	}

	glb.AddFlagTarget(cmd)

	cmd.PersistentFlags().StringVarP(&targetChainIDStr, "seq", "q", "", "target sequencer id")
	err := viper.BindPFlag("seq", cmd.PersistentFlags().Lookup("seq"))
	glb.AssertNoError(err)

	cmd.PersistentFlags().Uint8VarP(&maxFreezeEpochs, "epochs", "e", 8, "max frozen epochs allowed by the delegator")
	err = viper.BindPFlag("epochs", cmd.PersistentFlags().Lookup("epochs"))
	glb.AssertNoError(err)

	cmd.InitDefaultHelpCmd()
	return cmd
}

func runDelegateAmountCmd(_ *cobra.Command, args []string) {
	glb.InitLedgerFromNode()
	walletData := glb.GetWalletData()

	glb.Infof("wallet account is: %s", walletData.Account.String())

	var err error
	var targetSeqID base.ChainID

	if targetChainIDStr == "" {
		glb.Infof("selecing optimal/random target sequencer..")
		targetSeqID, err = chooseRandomSequencerForDelegation()
		glb.AssertNoError(fmt.Errorf("chooseRandomSequencerForDelegation: %v", err))
	} else {
		targetSeqID, err = base.ChainIDFromHexString(targetChainIDStr)
		glb.Assertf(err == nil, "failed parsing target chainID: %v", err)
	}

	seqOut, _, _, err := glb.GetClient().GetChainOutput(targetSeqID)
	glb.Assertf(err == nil, "can't find sequencer id %s: %v", targetSeqID.StringShort(), err)
	glb.Assertf(seqOut.Output.IsSequencerOutput(), "chainID %s does not represent a sequencer", targetSeqID.StringShort())

	var tagAlongSeqID *base.ChainID
	feeAmount := glb.GetTagAlongFee()
	glb.Assertf(feeAmount > 0, "tag-along fee is configured 0. Fee-less option not supported yet")
	tagAlongSeqID = glb.GetTagAlongSequencerID()
	glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")

	ts := ledger.TimeNow()
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(10)
	}
	amountInt, err := strconv.Atoi(args[0])
	glb.AssertNoError(err)
	amount := uint64(amountInt)
	minimumAmount := ledger.MinimumInflatableAmount(uint32(ts.Slot) + 1000)
	glb.Assertf(amount >= minimumAmount, "amount is too small, must be at least %s", util.Th(minimumAmount))

	glb.Assertf(maxFreezeEpochs > 0 && maxFreezeEpochs <= byte(ledger.Const.MaxFrozenEpochs), "wrong value of max freeze epochs")

	client := glb.GetClient()
	walletOutputs, lrbid, _, err := client.GetOutputsForAmount(walletData.Account, amount+feeAmount)
	glb.AssertNoError(err)
	glb.PrintLRB(lrbid)

	sumIn := uint64(0)
	walletOutputs = util.PurgeSlice(walletOutputs, func(o *ledger.OutputWithID) bool {
		if sumIn >= amount+feeAmount {
			return false
		}
		sumIn += o.Output.TokenBalance()
		return true
	})
	glb.Assertf(sumIn >= amount+feeAmount, "not enough tokens. Needed %s, got %s", util.Th(amount+feeAmount), util.Th(sumIn))

	txb := txbuilder.New()
	_, inTs, err := txb.ConsumeOutputsNoUnlock(walletOutputs...)
	glb.AssertNoError(err)

	ts = base.MaximumTime(inTs, ts)

	for i := range walletOutputs {
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			err = txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0)
			glb.AssertNoError(err)
		}
	}
	// tentative with maximum epochs, to check storage deposit
	outDelegation := ledger.MakeDelegationInitOutput(ledger.MakeDelegateInitOutputParams{
		Amount:             amount,
		Master:             walletData.Account,
		Target:             ledger.ChainLockFromChainID(targetSeqID),
		MaxFreezeEpochs:    byte(ledger.Const.MaxFrozenEpochs),
		MaxSeqProfitMargin: 100,
		StartSlot:          ts.Slot,
	})
	glb.AssertNoError(outDelegation.EnoughAmountForStorageDeposit())

	outDelegation = ledger.MakeDelegationInitOutput(ledger.MakeDelegateInitOutputParams{
		Amount:             amount,
		Master:             walletData.Account,
		Target:             ledger.ChainLockFromChainID(targetSeqID),
		MaxFreezeEpochs:    maxFreezeEpochs,
		MaxSeqProfitMargin: 100,
		StartSlot:          ts.Slot,
	})

	delegationOutputIdx, err := txb.ProduceOutput(outDelegation)
	glb.AssertNoError(err)

	outTagAlong := ledger.NewTagAlongOutput(feeAmount, *tagAlongSeqID, walletData.Account)
	_, err = txb.ProduceOutput(outTagAlong)
	glb.AssertNoError(err)

	totalAmountConsumed := txb.ConsumedAmount()
	totalAmountProduced, _ := txb.ProducedAmount()

	if totalAmountConsumed > totalAmountProduced {
		remainderOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(totalAmountConsumed - totalAmountProduced)
			o.WithLock(walletData.Account)
		})
		if _, err = txb.ProduceOutput(remainderOut); err != nil {
			err = fmt.Errorf("making remainder output: %v", err)
		}
		glb.AssertNoError(err)
	}

	totalAmountProduced, _ = txb.ProducedAmount()
	glb.Assertf(totalAmountConsumed == totalAmountProduced, "totalAmountConsumed==totalAmountProduced")

	txb.TransactionData.Timestamp = ts
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(walletData.PrivateKey)

	txBytes, txid, failedTx, err := txb.BytesWithValidation()
	glb.Assertf(err == nil, "error: %v\n---------- failing tx --------\n%s", err, failedTx)

	prompt := fmt.Sprintf("delegate amount %s to sequencer %s (plus tag-along fee %s)?",
		util.Th(amount), targetSeqID.String(), util.Th(feeAmount))

	if !glb.YesNoPrompt(prompt, true) {
		glb.Infof("exit")
		os.Exit(0)
	}

	delegationOid, err := base.NewOutputID(txid, delegationOutputIdx)
	glb.AssertNoError(err)

	delegationID := base.MakeOriginChainID(delegationOid)
	glb.Infof("\ndelegation ID is %s", delegationID.String())
	err = client.SubmitTransaction(txBytes)
	glb.AssertNoError(err)

	glb.TrackTxInclusion(txid, 2*time.Second)
}

// select randomly inverse proportionally coverage
// using random roulette wheel selection
func chooseRandomSequencerForDelegation() (base.ChainID, error) {
	outs, _, err := glb.GetClient().GetAllSequencerOutputs()
	glb.AssertNoError(err)

	if len(outs) == 0 {
		return base.ChainID{}, fmt.Errorf("no sequencer outputs")
	}
	// select randomly inverse proportionally coverage

	maxCov := uint64(0)
	for _, out := range outs {
		cov := out.Output.TokenBalance() + uint64(out.Output.FrozenCoverage(0))
		if maxCov < cov {
			maxCov = cov
		}
	}
	m := make(map[base.ChainID]uint64)
	currentSlot := ledger.SlotNow()
	for seqID, out := range outs {
		if out.ID.Slot()+6 >= currentSlot {
			// skip inactive sequencers
			m[seqID] = maxCov - (out.Output.TokenBalance() + uint64(out.Output.FrozenCoverage(0)))
		}
	}
	rnd := uint64(rand.Intn(int(maxCov)))
	sum := uint64(0)
	for seqID, x := range m {
		if rnd < sum {
			return seqID, nil
		}
		sum += x
	}
	panic("inconsistency in chooseRandomSequencerForDelegation")
}
