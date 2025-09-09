package node_cmd

import (
	"fmt"
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

var (
	targetChainIDStr string
	maxFeezeEpochs   uint8
)

func initDelegateCmd() *cobra.Command {
	delegateCmd := &cobra.Command{
		Use:     "delegate <amount> [-q <target sequencer id hex encoded. Defaults to own sequencer>]",
		Aliases: util.List("send"),
		Short:   `delegates amount to target sequencer by creating delegation chain output`,
		Args:    cobra.ExactArgs(1),
		Run:     runDelegateCmd,
	}

	glb.AddFlagTarget(delegateCmd)

	delegateCmd.PersistentFlags().StringVarP(&targetChainIDStr, "seq", "q", "", "target sequencer id")
	err := viper.BindPFlag("seq", delegateCmd.PersistentFlags().Lookup("seq"))
	glb.AssertNoError(err)

	delegateCmd.PersistentFlags().Uint8VarP(&maxFeezeEpochs, "epochs", "e", 1, "max frozen epochs allowed by the delegator")
	err = viper.BindPFlag("epochs", delegateCmd.PersistentFlags().Lookup("epochs"))
	glb.AssertNoError(err)

	delegateCmd.InitDefaultHelpCmd()
	return delegateCmd
}

func runDelegateCmd(_ *cobra.Command, args []string) {
	glb.InitLedgerFromNode()
	walletData := glb.GetWalletData()

	glb.Infof("wallet account is: %s", walletData.Account.String())

	var err error
	var targetSeqID base.ChainID

	if targetChainIDStr == "" {
		if id := glb.GetOwnSequencerID(); id == nil {
			glb.Assertf(id != nil, "own sequencer not configured -> can't use as a default target sequencer")
		} else {
			targetSeqID = *id
			glb.Infof("using own sequencer as a default target sequencer: %s", targetSeqID.String())
		}
	} else {
		targetSeqID, err = base.ChainIDFromHexString(targetChainIDStr)
		glb.Assertf(err == nil, "failed parsing target chainID: %v", err)
	}

	seqOut, _, _, err := glb.GetClient().GetChainOutput(targetSeqID)
	glb.Assertf(err == nil, "can't find sequencer id %s: %v", targetSeqID.StringShort(), err)
	glb.Assertf(seqOut.ID.IsSequencerTransaction(), "chainID %s does not represent a sequencer", targetSeqID.StringShort())

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
	minimumAmount := ledger.L().MinimumInflatableAmount(uint32(ts.Slot) + 1000)
	glb.Assertf(amount >= minimumAmount, "amount is too small, must be at least %s", util.Th(minimumAmount))

	glb.Assertf(maxFeezeEpochs > 0 && maxFeezeEpochs <= byte(ledger.Const.MaxFrozenEpochs), "wrong value of max freeze epochs")

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
	outDelegation := ledger.MakeDelegationInitOutput(ledger.MakeDelegateInitOutputParams{
		Amount:             amount,
		Master:             walletData.Account,
		Target:             ledger.ChainLockFromChainID(targetSeqID),
		MaxFreezeEpochs:    maxFeezeEpochs,
		MaxSeqProfitMargin: 100,
		StartSlot:          ts.Slot,
	})
	delegationOutputIdx, _ := txb.ProduceOutput(outDelegation)

	outTagAlong := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(feeAmount)
		o.WithLock(ledger.ChainLockFromChainID(*tagAlongSeqID))
	})
	_, _ = txb.ProduceOutput(outTagAlong)

	totalAmountConsumed := txb.ConsumedAmount()
	totalAmountProduced, _ := txb.ProducedAmount()

	if totalAmountConsumed > totalAmountProduced {
		remainderOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(totalAmountConsumed - totalAmountProduced)
			o.WithLock(walletData.Account)
		})
		_, _ = txb.ProduceOutput(remainderOut)
	}

	totalAmountProduced, _ = txb.ProducedAmount()
	glb.Assertf(totalAmountConsumed == totalAmountProduced, "totalAmountConsumed==totalAmountProduced")

	txb.TransactionData.Timestamp = ts
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(walletData.PrivateKey)

	txBytes, txid, failedTx, err := txb.BytesWithValidation()
	glb.Assertf(err == nil, "transaction invalid: %v\n------------------\n%s", err, failedTx)

	prompt := fmt.Sprintf("delegate amount %s to sequencer %s (plus tag-along fee %s)?",
		util.Th(amount), targetSeqID.String(), util.Th(feeAmount))

	if !glb.YesNoPrompt(prompt, true) {
		glb.Infof("exit")
		os.Exit(0)
	}

	delegationOid, err := base.NewOutputID(txid, delegationOutputIdx)
	glb.AssertNoError(err)

	delegationID := base.MakeOriginChainID(delegationOid)
	glb.Infof("\ndelegation id: %s\n", delegationID.String())

	err = client.SubmitTransaction(txBytes)
	glb.AssertNoError(err)

	glb.TrackTxInclusion(txid, 2*time.Second)
}
