package node_cmd

import (
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
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

	outs, lrbid, err := glb.GetClient().GetAccountOutputs(accountable)
	glb.AssertNoError(err)
	glb.PrintLRB(lrbid)
	displayBalanceTotals(outs, accountable)
}

type _delegation struct {
	amount     uint64
	inflation  uint64
	sinceSlot  base.Slot
	lastActive base.Slot
}

func displayBalanceTotals(outs []*ledger.OutputWithID, walletAccount ledger.Accountable) {
	var sumOnChains, sumOutsideChains, sumDelegation uint64
	var numChains, numNonChains, numDelegation int

	delegations := make([]ledger.DelegationOutput, 0)

	for _, o := range outs {
		_, ccIdx := o.Output.ChainConstraint()
		if ccIdx != 0xff {
			numChains++
			sumOnChains += o.Output.TokenBalance()

		} else {
			numNonChains++
			sumOutsideChains += o.Output.TokenBalance()
		}
		if dOut, ok := ledger.AsDelegationOutput(o.Output, o.ID); ok {
			if !ledger.EqualAccountables(dOut.Master(), walletAccount) {
				// for delegation locks only count those which are owned by the wallet
				continue
			}
			numDelegation++
			sumDelegation += o.Output.TokenBalance()
			delegations = append(delegations, dOut)
		}
	}
	glb.Infof("SUMMARY: total amounts controlled by %s:", walletAccount.String())
	glb.Infof("    %d on non-chain outputs:                    %s", numNonChains, util.Th(sumOutsideChains))
	glb.Infof("    %d on chain outputs (including delegation): %s", numChains, util.Th(sumOnChains))
	glb.Infof("    %d on delegation outputs:                   %s", numDelegation, util.Th(sumDelegation))
	glb.Infof("-----------------\nTOTAL controlled on %d outputs: %s", numChains+numNonChains, util.Th(sumOnChains+sumOutsideChains))
	if len(delegations) == 0 {
		glb.Infof("\nNO DELEGATIONS")
		return
	}
	currentSlot := uint32(ledger.TimeNow().Slot)

	glb.Infof("\nDELEGATIONS (current slot is %d):\n\n%s\n", currentSlot, glb.LinesDelegationOutputs(delegations, currentSlot, 0, "     ").String())
}
