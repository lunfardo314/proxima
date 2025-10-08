package node_cmd

import (
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

func initBalanceCmd() *cobra.Command {
	getBalanceCmd := &cobra.Command{
		Use:     "balance",
		Aliases: []string{"bal"},
		Short:   `displays account totals`,
		Args:    cobra.NoArgs,
		Run:     runBalanceCmd,
	}
	glb.AddFlagTarget(getBalanceCmd)
	getBalanceCmd.InitDefaultHelpCmd()
	return getBalanceCmd
}

func runBalanceCmd(_ *cobra.Command, _ []string) {
	glb.InitLedgerFromNode()
	accountable := glb.MustGetTarget()

	clnt := glb.GetClient()
	outs, lrbid, err := clnt.GetAccountOutputs(accountable)
	glb.AssertNoError(err)
	glb.PrintLRB(lrbid)
	displayBalanceTotals(outs, accountable)
}

func displayBalanceTotals(outs []*ledger.OutputWithID, walletAccount ledger.Accountable) {
	var sumOnNonDelegationChains, sumOutsideChains, sumDelegation uint64
	var numNonChains int

	delegations := make([]ledger.DelegationOutput, 0)
	otherChains := make([]ledger.OutputWithChainID, 0)

	for _, o := range outs {
		if oChain, err := o.AsChainOutput(); err == nil {
			if dOut, ok := ledger.AsDelegationOutput(o.Output, o.ID); ok {
				if !ledger.EqualAccountables(dOut.Master(), walletAccount) {
					// for delegation locks only count those which are owned by the wallet
					continue
				}
				sumDelegation += o.Output.TokenBalance()
				delegations = append(delegations, dOut)
			} else {
				sumOnNonDelegationChains += o.Output.TokenBalance()
				otherChains = append(otherChains, *oChain)
			}
		} else {
			numNonChains++
			sumOutsideChains += o.Output.TokenBalance()
		}
	}
	currentSlot := ledger.TimeNow().Slot
	glb.Infof("Current slot is %d", currentSlot)
	glb.Infof("\nSUMMARY of controlled by %s:", walletAccount.String())
	glb.Infof("    on %2d non-chain outputs:            %s", numNonChains, util.Th(sumOutsideChains))
	glb.Infof("    on %2d delegation outputs:           %s", len(delegations), util.Th(sumDelegation))
	glb.Infof("    on %2d non-delegation chain outputs: %s", len(otherChains), util.Th(sumOnNonDelegationChains))
	glb.Infof("-----------------\nTOTAL controlled on %d outputs: %s",
		len(delegations)+len(otherChains)+numNonChains, util.Th(sumDelegation+sumOnNonDelegationChains+sumOutsideChains))
	if len(delegations) == 0 {
		glb.Infof("\nNO DELEGATIONS")
	} else {
		glb.Infof("\nDELEGATIONS (%d):\n\n%s\n", len(delegations), glb.LinesDelegationOutputs(delegations, currentSlot, "  ").String())
	}
	if len(otherChains) > 0 {
		glb.Infof("\nNON-DELEGATION CHAINS (%d):\n\n%s\n", len(otherChains), glb.LinesChainOutputs(otherChains, currentSlot, "  ").String())
	}
}
