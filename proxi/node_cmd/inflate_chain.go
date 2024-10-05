package node_cmd

import (
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	txb "github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/spf13/cobra"
)

func initInflateChainCmd() *cobra.Command {
	inflateChainCmd := &cobra.Command{
		Use:     "inflate_chain <chainID>",
		Aliases: []string{"inflate"},
		Short:   `creates inflation on the chain by transiting it every <period in slots>`,
		Args:    cobra.ExactArgs(1),
		Run:     runInflateChainCmd,
	}
	glb.AddFlagTraceTx(inflateChainCmd)
	inflateChainCmd.InitDefaultHelpCmd()

	inflateChainCmd.PersistentFlags().IntVarP(&periodInSlots, "slots", "s", 10, "period in slots")
	inflateChainCmd.PersistentFlags().BoolVarP(&jumpToPresent, "jump_first", "j", false, "jump to the presence if chain output is far in the past")

	return inflateChainCmd
}

var (
	periodInSlots int
	jumpToPresent bool
)

func runInflateChainCmd(_ *cobra.Command, args []string) {
	glb.InitLedgerFromNode()

	chainID, err := ledger.ChainIDFromHexString(args[0])
	glb.AssertNoError(err)
	inflateChain(ledger.Slot(periodInSlots), chainID)
}

func inflateChain(chainTransitionPeriodSlots ledger.Slot, chainId ledger.ChainID) {
	walletData := glb.GetWalletData()
	tagAlongSeq := GetTagAlongSequencerID()
	tagAlongFee := getTagAlongFee()

	glb.Assertf(chainTransitionPeriodSlots <= ledger.Slot(ledger.L().ID.ChainInflationOpportunitySlots),
		"transition period in slots should not be bigger than inflation opportunity window")

	chainOutput, _, err := glb.GetClient().GetChainOutput(chainId)
	glb.AssertNoError(err)
	glb.Assertf(!chainOutput.ID.IsSequencerTransaction(), "must be non-sequencer output")

	estimated := ledger.L().CalcChainInflationAmount(ledger.NewLedgerTime(0, 1), ledger.NewLedgerTime(chainTransitionPeriodSlots, 1), chainOutput.Output.Amount(), 0)
	msg := lines.New().
		Add("will be inflating chain %s every %d slots", chainId.StringShort(), chainTransitionPeriodSlots).
		Add("Initial chain balance is %s, Tag-along fee to %s is %d", util.Th(chainOutput.Output.Amount()), tagAlongSeq.StringShort(), tagAlongFee).
		Add("Estimated net earnings per loop will be %s", util.Th(int64(estimated)-int64(tagAlongFee)))
	if jumpToPresent {
		msg.Add("forced jump to presence with 0 inflation, if necessary")
	}
	msg.Add("Proceed?")

	glb.YesNoPrompt(msg.String(), true)

	tsIN := chainOutput.Timestamp()
	tsOut := tsIN.AddSlots(chainTransitionPeriodSlots)

	ignoreProfitability := false
	if tsOut.Before(ledger.TimeNow()) && jumpToPresent {
		tsOut = ledger.TimeNow()
		ignoreProfitability = true
	}
	if tsOut.IsSlotBoundary() {
		tsOut = tsOut.AddTicks(1)
	}

	for {
		glb.Assertf(!tsOut.IsSlotBoundary(), "can't be on slot boundary")

		// create origin branch transaction at the next slot after genesis time slot
		txBytes, inflation, _, err := txb.MakeChainSuccessorTransaction(&txb.MakeChainSuccTransactionParams{
			ChainInput:           chainOutput,
			Timestamp:            tsOut,
			WithdrawAmount:       tagAlongFee,
			WithdrawTarget:       tagAlongSeq.AsChainLock(),
			PrivateKey:           walletData.PrivateKey,
			EnforceProfitability: !ignoreProfitability,
		})
		glb.AssertNoError(err)
		ignoreProfitability = false

		txid, err := transaction.IDFromTransactionBytes(txBytes)
		glb.AssertNoError(err)
		sleepFor := time.Until(tsOut.Time())
		glb.Infof("--------------\nwill be submitting next chain transaction %s in %v", txid.String(), sleepFor)
		estimate := int64(0)
		if tagAlongFee < inflation {
			estimate = int64(inflation - tagAlongFee)
		}
		glb.Infof("net inflation earnings after fee will be %s", util.Th(estimate))

		if sleepFor > 0 {
			glb.Infof("waiting for approx. %v to post the transaction... (ctrl-C to interrupt)", sleepFor)
			time.Sleep(sleepFor)
		}
		glb.Infof("submitting the transaction %s", txid.String())

		err = glb.GetClient().SubmitTransaction(txBytes, false)
		glb.AssertNoError(err)

		glb.ReportTxInclusion(txid, time.Second)

		for i := 0; ; i++ {
			chainOutput, _, err = glb.GetClient().GetChainOutput(chainId)
			glb.AssertNoError(err)

			if chainOutput.ID.TransactionID() == txid {
				break
			}
			time.Sleep(time.Second)
			if i >= 10 {
				glb.Infof(">>>> warning: failed to reach finality")
				break
			}
		}
		tsOut = chainOutput.Timestamp().AddSlots(chainTransitionPeriodSlots)
		glb.Infof("amount on chain: %s", util.Th(chainOutput.Output.Amount()))
	}
}
