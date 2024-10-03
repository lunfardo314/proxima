package node_cmd

import (
	"bufio"
	"os"
	"strconv"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	txb "github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

func initInflateChainCmd() *cobra.Command {
	inflateChainCmd := &cobra.Command{
		Use:   "inflate_chain [<period>] [<fee>] [<amount>]",
		Short: `inflates the <amount> tokens with a chain with the given chain transaction <period> and <fee>. If amount not provided all funds will be used`,
		Args:  cobra.MaximumNArgs(3),
		Run:   runInflateChainCmd,
	}
	glb.AddFlagTraceTx(inflateChainCmd)
	inflateChainCmd.InitDefaultHelpCmd()

	return inflateChainCmd
}

func InflateChain(chainTransitionPeriod uint64, tagAlongFee uint64, chainId ledger.ChainID) {

	walletData := glb.GetWalletData()

	tagAlongSequ := GetTagAlongSequencerID()

	stopPressed := false

	// Goroutine to listen for key press
	go func() {
		bufio.NewReader(os.Stdin).ReadBytes('\n') // Wait for Enter key
		stopPressed = true
		glb.Infof("'Enter' pressed. stopping...")
	}()

	for {
		glb.Infof("Waiting for %d sec...", chainTransitionPeriod*uint64(ledger.SlotDuration().Seconds()))
		glb.Infof("Press 'Enter' to stop the loop...")
		c := uint64(0)
		for {
			time.Sleep(1 * time.Second)
			c += 1
			if c >= chainTransitionPeriod*uint64(ledger.SlotDuration().Seconds()) || stopPressed {
				break
			}
		}
		if stopPressed {
			break
		}
		chainOutput, _, err := glb.GetClient().GetChainOutput(chainId)
		if err != nil {
			return
		}

		ts := ledger.TimeNow().AddTicks(ledger.TransactionPace())
		if ts.IsSlotBoundary() {
			time.Sleep(time.Duration(ledger.TransactionPace()) * ledger.TickDuration())
			ts = ledger.TimeNow().AddTicks(ledger.TransactionPace())
		}
		calcInflation := ledger.L().CalcChainInflationAmount(chainOutput.Timestamp(), ts, chainOutput.Output.Amount(), 0)
		profitability := int(int(calcInflation) - int(tagAlongFee))
		glb.Infof("Expected earning: %s", util.Th(profitability))
		if profitability < 0 {
			glb.Infof("profitability < 0. Skipping this round")
			continue
		}

		// create origin branch transaction at the next slot after genesis time slot
		txBytes, _, err := txb.MakeChainSuccTransaction(&txb.MakeChainSuccTransactionParams{
			ChainInput:           chainOutput,
			Timestamp:            ts,
			EnforceProfitability: true,
			TargetFee:            tagAlongFee,
			Target:               ledger.ChainLockFromChainID(*tagAlongSequ),
			PrivateKey:           walletData.PrivateKey,
		})
		if err != nil {
			return
		}
		inps := make([]*ledger.OutputWithID, 1)
		inps[0] = &chainOutput.OutputWithID
		txCtx, err := transaction.TxContextFromTransferableBytes(txBytes, transaction.PickOutputFromListFunc(inps))
		glb.AssertNoError(err)

		glb.Infof("Inflating chain. Press 'Enter' to stop the loop...")
		err = glb.GetClient().SubmitTransaction(txBytes, false)
		glb.AssertNoError(err)
		if !glb.NoWait() {
			glb.ReportTxInclusion(*txCtx.TransactionID(), time.Second)
		}
		if stopPressed {
			break
		}
	}
}

func runInflateChainCmd(_ *cobra.Command, args []string) {
	//cmd.DebugFlags()
	glb.InitLedgerFromNode()

	tagAlongFee := getTagAlongFee()
	chainTransitionPeriod := uint64(2)
	onChainAmount := uint64(0)
	if len(args) > 0 {
		period, err := strconv.ParseUint(args[0], 10, 64)
		glb.AssertNoError(err)
		chainTransitionPeriod = period
	}
	if len(args) > 1 {
		fee, err := strconv.ParseUint(args[1], 10, 64)
		glb.AssertNoError(err)
		tagAlongFee = fee
	}
	if len(args) > 2 {
		amount, err := strconv.ParseUint(args[2], 10, 64)
		glb.AssertNoError(err)
		onChainAmount = amount
	}

	glb.Infof("starting chain inflation of %d [0:all tokens] with period %d [slots] and fee %d", onChainAmount, chainTransitionPeriod, tagAlongFee)

	txCtx, chainID, err := MakeChain(onChainAmount)
	glb.AssertNoError(err)
	glb.Infof("new chain ID is %s", chainID.String())
	if !glb.NoWait() {
		glb.ReportTxInclusion(*txCtx.TransactionID(), time.Second)
	}

	InflateChain(chainTransitionPeriod, tagAlongFee, chainID)

	txCtx, err = DeleteChain(&chainID)
	glb.AssertNoError(err)
	if !glb.NoWait() {
		glb.ReportTxInclusion(*txCtx.TransactionID(), time.Second)
	}
}
