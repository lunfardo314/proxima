package node_cmd

import (
	"strconv"
	"time"

	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initInflateTokensCmd() *cobra.Command {
	inflateChainCmd := &cobra.Command{
		Use:     "inflate_tokens [<period>] [<fee>] [<amount>]",
		Aliases: []string{"inflate"},
		Short:   `inflates the <amount> tokens on a newly created chain with the given chain transaction <period in slots> and <tag-along fee>. If amount not provided all funds in the account will be used`,
		Args:    cobra.MaximumNArgs(3),
		Run:     runInflateTokensCmd,
	}
	glb.AddFlagTraceTx(inflateChainCmd)
	inflateChainCmd.InitDefaultHelpCmd()

	return inflateChainCmd
}

func runInflateTokensCmd(_ *cobra.Command, args []string) {
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
