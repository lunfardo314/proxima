package node_cmd

import (
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
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
	inflateChainCmd.InitDefaultHelpCmd()

	inflateChainCmd.PersistentFlags().BoolVarP(&jumpToPresent, "jump_first", "j", false, "jump to the presence if chain output is far in the past")

	return inflateChainCmd
}

var (
	jumpToPresent bool
)

func runInflateChainCmd(_ *cobra.Command, args []string) {
	glb.InitLedgerFromNode()

	chainID, err := base.ChainIDFromHexString(args[0])
	glb.AssertNoError(err)
	inflateChain(chainID)
}

func inflateChain(chainId base.ChainID) {
	walletData := glb.GetWalletData()
	tagAlongSeq := glb.GetTagAlongSequencerID()
	tagAlongFee := glb.GetTagAlongFee()

	chainOutput, _, _, err := glb.GetClient().GetChainOutput(chainId)
	glb.AssertNoError(err)
	glb.Assertf(!chainOutput.ID.IsSequencerTransaction(), "must be non-sequencer output")

	msg := lines.New().
		Add("Initial chain balance is %s, Tag-along fee to %s is %d", util.Th(chainOutput.Output.TokenBalance()), tagAlongSeq.StringShort(), tagAlongFee)
	if jumpToPresent {
		msg.Add("forced jump to presence with 0 inflation, if necessary")
	}
	msg.Add("Proceed?")

	glb.YesNoPrompt(msg.String(), true)

	tsIN := chainOutput.Timestamp()
	tsOut := tsIN.AddSlots(1)

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
			TagAlongFee:          tagAlongFee,
			TagAlongSequencer:    *tagAlongSeq,
			PrivateKey:           walletData.PrivateKey,
			EnforceProfitability: !ignoreProfitability,
		})
		glb.AssertNoError(err)
		ignoreProfitability = false

		txid, err := transaction.IDFromParsedTransactionBytes(txBytes)
		glb.AssertNoError(err)
		sleepFor := time.Until(ledger.ClockTime(tsOut))
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

		err = glb.GetClient().SubmitTransaction(txBytes)
		glb.AssertNoError(err)

		glb.TrackTxInclusion(txid, time.Second)

		for i := 0; ; i++ {
			chainOutput, _, _, err = glb.GetClient().GetChainOutput(chainId)
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
		tsOut = chainOutput.Timestamp().AddSlots(1)
		glb.Infof("amount on chain: %s", util.Th(chainOutput.Output.TokenBalance()))
	}
}
