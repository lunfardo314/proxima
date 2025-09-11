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

func displayBalanceTotals(outs []*ledger.OutputWithID, target ledger.Accountable) {
	var sumOnChains, sumOutsideChains, sumDelegation uint64
	var numChains, numNonChains, numDelegation int

	delegations := make(map[base.ChainID]_delegation)

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
			if !ledger.EqualAccountables(dOut.Master(), target) {
				// for delegation locks only count those which are owned by the target
				continue
			}
			numDelegation++
			sumDelegation += o.Output.TokenBalance()
			glb.Assertf(ok, "extractChainID")
			delegations[dOut.ChainID] = _delegation{
				amount:     o.Output.TokenBalance(),
				inflation:  o.Output.TokenBalance() - dOut.OriginAmount,
				sinceSlot:  dOut.OriginSlot,
				lastActive: o.ID.Slot(),
			}
		}
	}
	glb.Infof("Total amounts controlled on:")
	glb.Infof("    %d non-chain outputs:                    %s", numNonChains, util.Th(sumOutsideChains))
	glb.Infof("    %d chain outputs (including delegation): %s", numChains, util.Th(sumOnChains))
	glb.Infof("    %d delegation outputs:                   %s", numDelegation, util.Th(sumDelegation))
	glb.Infof("-----------------\nTOTAL controlled on %d outputs: %s", numChains+numNonChains, util.Th(sumOnChains+sumOutsideChains))
	if len(delegations) == 0 {
		glb.Infof("\nNO DELEGATIONS")
		return
	}
	glb.Infof("\nDELEGATIONS:")
	ids := util.KeysSorted(delegations, func(k1, k2 base.ChainID) bool {
		return delegations[k1].sinceSlot < delegations[k2].sinceSlot
	})

	totalDelegated := uint64(0)
	for _, id := range ids {
		d := delegations[id]
		glb.Infof("     %s   %20s (+%s)", id.String(), util.Th(d.amount), util.Th(d.inflation))
		totalDelegated += d.amount
	}
	glb.Infof("----------------\nTOTAL DELEGATED AMOUNT: %s", util.Th(totalDelegated))
}
