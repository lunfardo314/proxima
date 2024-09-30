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
	"github.com/spf13/cobra"
)

func initInflateChainCmd() *cobra.Command {
	inflateChainCmd := &cobra.Command{
		Use:   "inflate_chain [<amount>]",
		Short: `inflates the <amount> tokens with a chain. If amount not provided all funds will be used`,
		Args:  cobra.MaximumNArgs(1),
		Run:   runInflateChainCmd,
	}
	glb.AddFlagTraceTx(inflateChainCmd)
	inflateChainCmd.InitDefaultHelpCmd()

	return inflateChainCmd
}

func InflateChain(chainId ledger.ChainID) {

	walletData := glb.GetWalletData()

	tagAlongFee := getTagAlongFee()
	tagAlongSequ := GetTagAlongSequencerID()

	stopPressed := false

	// Goroutine to listen for key press
	go func() {
		glb.Infof("Press 'Enter' to stop the loop...")
		bufio.NewReader(os.Stdin).ReadBytes('\n') // Wait for Enter key
		stopPressed = true
		glb.Infof("'Enter' pressed. stopping...")
	}()

	//pace := 30
	for {
		//time.Sleep(time.Duration(pace) * ledger.TickDuration())
		time.Sleep(ledger.SlotDuration())
		if stopPressed {
			break
		}
		//inflation := ledger.L().CalcChainInflationAmount(tsIn, tsOut, amount, delayed)
		chainOutput, _, err := glb.GetClient().GetChainOutput(chainId)
		if err != nil {
			return
		}

		ts := ledger.TimeNow() //ledger.MaximumTime(maxTimestamp(lastOuts).AddTicks(pace), ledger.TimeNow())
		calcInflation := ledger.L().CalcChainInflationAmount(chainOutput.Timestamp(), ts, chainOutput.Output.Amount(), 0)
		glb.Infof("Expected earning: %d", int(int(calcInflation)-int(tagAlongFee)))

		// create origin branch transaction at the next slot after genesis time slot
		txBytes, _, err := txb.MakeChainSuccTransaction(txb.MakeChainSuccTransactionParams{
			ChainInput:        chainOutput,
			Timestamp:         ts,
			MinimumFee:        tagAlongFee,
			TagAlongSequencer: *tagAlongSequ,
			PrivateKey:        walletData.PrivateKey,
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

	onChainAmount := uint64(0)
	if len(args) > 0 {
		amount, err := strconv.ParseUint(args[0], 10, 64)
		glb.AssertNoError(err)
		onChainAmount = amount
	}

	txCtx, chainID, err := MakeChain(onChainAmount)
	glb.AssertNoError(err)
	glb.Infof("new chain ID is %s", chainID.String())
	if !glb.NoWait() {
		glb.ReportTxInclusion(*txCtx.TransactionID(), time.Second)
	}

	InflateChain(chainID)

	txCtx, err = DeleteChain(&chainID)
	glb.AssertNoError(err)
	// if !glb.NoWait() {
	// 	glb.ReportTxInclusion(*txCtx.TransactionID(), time.Second)
	// }
}
