package node_cmd

import (
	"strconv"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	txb "github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initInflateChainCmd() *cobra.Command {
	inflateChainCmd := &cobra.Command{
		Use:   "inflate_chain <amount>",
		Short: `inflates the <amount> tokens with a chain`,
		Args:  cobra.ExactArgs(1),
		Run:   runInflateChainCmd,
	}
	glb.AddFlagTraceTx(inflateChainCmd)
	inflateChainCmd.InitDefaultHelpCmd()

	return inflateChainCmd
}

func InflateChain(chainId ledger.ChainID) {

	pace := 30
	//beginTime := time.Now()
	for {
		time.Sleep(time.Duration(pace) * ledger.TickDuration())
		//inflation := ledger.L().CalcChainInflationAmount(tsIn, tsOut, amount, delayed)
		chainOutput, _, err := glb.GetClient().GetChainOutput(chainId)
		if err != nil {
			return
		}

		// create origin branch transaction at the next slot after genesis time slot
		txBytes, err := txb.MakeSequencerTransaction(txb.MakeSequencerTransactionParams{
			ChainInput: &ledger.OutputWithChainID{
				OutputWithID: *chainOutput,
				ChainID:      chainId,
			},
			StemInput:         nil,
			Timestamp:         ledger.NewLedgerTime(genesisStem.Timestamp().Slot()+1, 0),
			MinimumFee:        0,
			AdditionalInputs:  nil,
			AdditionalOutputs: nil,
			Endorsements:      nil,
			PrivateKey:        originPrivateKey,
			PutInflation:      false,
		})
		if err != nil {
			return nil, err
		}
	}
}

func runInflateChainCmd(_ *cobra.Command, args []string) {
	//cmd.DebugFlags()
	glb.InitLedgerFromNode()

	onChainAmount, err := strconv.ParseUint(args[0], 10, 64)
	glb.AssertNoError(err)

	txCtx, chainID, err := MakeChain(onChainAmount)

	glb.AssertNoError(err)
	glb.Infof("new chain ID is %s", chainID.String())
	if !glb.NoWait() {
		glb.ReportTxInclusion(*txCtx.TransactionID(), time.Second)
	}
}
