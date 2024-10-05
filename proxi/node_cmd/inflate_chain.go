package node_cmd

import (
	"bufio"
	"os"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	txb "github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
)

func InflateChain(chainTransitionPeriodSlots uint64, tagAlongFee uint64, chainId ledger.ChainID) {

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
		glb.Infof("Waiting for %d sec...", chainTransitionPeriodSlots*uint64(ledger.SlotDuration().Seconds()))
		glb.Infof("Press 'Enter' to stop the loop...")
		c := uint64(0)
		for {
			time.Sleep(1 * time.Second)
			c += 1
			if c >= chainTransitionPeriodSlots*uint64(ledger.SlotDuration().Seconds()) || stopPressed {
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
			// no need to wait, transaction will wait in the input queue
			//time.Sleep(time.Duration(ledger.TransactionPace()) * ledger.TickDuration())
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
		txBytes, _, err := txb.MakeChainSuccessorTransaction(&txb.MakeChainSuccTransactionParams{
			ChainInput:           chainOutput,
			Timestamp:            ts,
			EnforceProfitability: true,
			WithdrawAmount:       tagAlongFee,
			WithdrawTarget:       ledger.ChainLockFromChainID(*tagAlongSequ),
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
