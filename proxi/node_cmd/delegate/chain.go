package delegate

import (
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func initDelegationSubmitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "chain <chain ID> [flags]",
		Short: `delegates existing chain to the target sequencer`,
		Args:  cobra.ExactArgs(1),
		Run:   runDelegationSubmitCmd,
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

func runDelegationSubmitCmd(_ *cobra.Command, args []string) {
	glb.InitLedgerFromNode()
	walletData := glb.GetWalletData()

	glb.Infof("wallet account is: %s", walletData.Account.String())

	var err error
	var targetSeqID base.ChainID

	chainID, err := base.ChainIDFromHexString(args[0])
	glb.AssertNoError(err)

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
	client := glb.GetClient()
	oIn, _, _, err := client.GetChainOutput(chainID)
	glb.AssertNoError(err)

	dOut, isDelegation := ledger.AsDelegationOutput(oIn.Output, oIn.ID)
	glb.Assertf(!isDelegation || dOut.IsUnlockableByMaster(ts.Slot), "chain is delegation output NOT unlockable by master")

	inflation := ledger.ChainInflationOneSlot(oIn.Output.TokenBalance(), oIn.ID.Slot())

	// tentatively checking maximum storage deposit
	oOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(oIn.Output.TokenBalance()+inflation-glb.GetTagAlongFee()), int64(inflation))
		lock := ledger.NewDelegateLock(ledger.ChainLockFromChainID(targetSeqID), walletData.Account, byte(ledger.Const.MaxFrozenEpochs), 100)
		o.WithLock(lock)
		cc := ledger.NewChainConstraint(chainID, 0, 2, oIn.OriginSlot, oIn.OriginAmount)
		o.MustPushConstraint(cc.Bytes())
		o.MustPushConstraint(ledger.DelegateLockState{}.Bytes())
	})
	glb.AssertNoError(oOut.EnoughAmountForStorageDeposit())

	oOut = ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(oIn.Output.TokenBalance()+inflation-glb.GetTagAlongFee()), int64(inflation))
		lock := ledger.NewDelegateLock(ledger.ChainLockFromChainID(targetSeqID), walletData.Account, maxFreezeEpochs, 100)
		o.WithLock(lock)
		cc := ledger.NewChainConstraint(chainID, 0, 2, oIn.OriginSlot, oIn.OriginAmount)
		o.MustPushConstraint(cc.Bytes())
		o.MustPushConstraint(ledger.DelegateLockState{}.Bytes())
	})

	txb := txbuilder.New()
	predIdx, err := txb.ConsumeOutput(oIn.Output, oIn.ID)
	glb.AssertNoError(err)
	glb.Assertf(predIdx == 0, "predIdx==0")
	txb.PutSignatureUnlock(0, 0, ledger.DelegationUnlockedByMaster)
	txb.PutUnlockParams(0, 2, ledger.NewChainUnlockParams(0, 2))

	succIdx, err := txb.ProduceOutput(oOut)
	glb.AssertNoError(err)
	glb.Assertf(succIdx == 0, "succIdx==0")

	taOut := ledger.NewTagAlongOutput(glb.GetTagAlongFee(), *glb.GetTagAlongSequencerID(), walletData.Account)
	tagAlongIdx, err := txb.ProduceOutput(taOut)
	glb.AssertNoError(err)
	glb.Assertf(tagAlongIdx == 1, "tagAlongIdx==1")

	txb.TransactionData.Timestamp = ts
	txb.TransactionData.InputCommitment = ledger.HashOutputs(oIn.Output)
	txb.SignED25519(walletData.PrivateKey)

	txBytes, txid, txString, err := txb.BytesWithValidation()
	if err != nil {
		glb.Infof("\nFAILED to produce transaction: '%v'\n-------------------\n%s", err, txString)
		return
	}
	glb.Verbosef("\n-------- tx OK (len = %d) -----------\n%s", len(txBytes), txString)

	prompt := fmt.Sprintf("delegate %s to sequencer %s?", chainID.StringShort(), targetSeqID.String())
	if !glb.YesNoPrompt(prompt, true) {
		return
	}
	err = client.SubmitTransaction(txBytes)
	glb.AssertNoError(err)

	glb.TrackTxInclusion(txid, 2*time.Second)
}
