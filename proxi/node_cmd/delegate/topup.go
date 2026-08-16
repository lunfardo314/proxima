package delegate

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

// `topup` picks a delegation to add tokens to and hands the actual work to
// `delegate chain --add`. The miner applies a rule unattended
// (claude/delegation_add_tokens.md §3); a person at a terminal has already
// decided, so this command reports the situation and does what it is told.

func initDelegationTopUpCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "topup <amount>",
		Short: `adds tokens from the wallet to an existing delegation and re-delegates it`,
		Args:  cobra.ExactArgs(1),
		Run:   runDelegationTopUpCmd,
	}
	cmd.PersistentFlags().String("delegation", "", "delegation to top up (default: the smallest one the master can consume)")
	cmd.InitDefaultHelpCmd()
	return cmd
}

// topUpCandidate is one of the wallet's delegations, classified for display.
type topUpCandidate struct {
	view    *txbuildercore.DelegationOutputView
	oid     base.OutputID
	balance uint64
}

func runDelegationTopUpCmd(cmd *cobra.Command, args []string) {
	amountInt, err := strconv.Atoi(args[0])
	glb.AssertNoError(err)
	glb.Assertf(amountInt > 0, "amount must be > 0")
	amount := uint64(amountInt)

	walletData := glb.GetWalletData()
	lib := glb.GetTxLibrary()
	consts := glb.GetLedgerConstants()
	clnt := glb.GetClient()
	slot := glb.GetLedgerTimeNow().Slot

	res, err := clnt.GetOutputsForControllerID(walletData.Account.ControllerID(), client.GetOutputsParams{
		LockType:   api.GetOutputsLockTypeDelegateMaster,
		Chained:    client.ChainedOnly(),
		MaxOutputs: api.GetOutputsIterationCap,
	})
	glb.AssertNoError(err)
	glb.PrintLRB(&res.LRBID)

	all := make([]*topUpCandidate, 0, len(res.Outputs))
	for _, o := range res.Outputs {
		view, ok, err := lib.ParseDelegationOutput(o.Output.Output, o.ID)
		if err != nil || !ok {
			continue
		}
		all = append(all, &topUpCandidate{view: view, oid: o.ID, balance: o.Output.TokenBalance()})
	}
	glb.Assertf(len(all) > 0, "no delegation controlled by %s", walletData.Account.String())

	open, frozen := splitByAccess(all, slot, consts)

	// An explicit --delegation is an instruction, not a suggestion: report why
	// it cannot be topped up rather than silently choosing another.
	if s, _ := cmd.Flags().GetString("delegation"); s != "" {
		id, err := base.ChainIDFromHexString(s)
		glb.AssertNoError(err)
		for _, c := range all {
			if c.view.ChainID == id {
				glb.Assertf(!c.view.IsInFrozenSlot(slot, consts),
					"delegation %s is frozen until slot %d (%d slots away); stop it first with `dlg askstop %s`",
					id.StringShort(), c.view.UnfreezeSlot(consts), c.view.UnfreezeSlot(consts)-slot, id.StringHex())
				delegateChainWithAdd(id, amount)
				return
			}
		}
		glb.Assertf(false, "delegation %s is not controlled by this wallet", id.StringShort())
	}

	if len(open) > 0 {
		// smallest first, so balances even out across delegations
		sort.Slice(open, func(i, j int) bool { return open[i].balance < open[j].balance })
		c := open[0]
		glb.Infof("topping up %s (%s, %s)", c.view.ChainID.StringShort(), util.Th(c.balance), accessLabel(c))
		delegateChainWithAdd(c.view.ChainID, amount)
		return
	}

	// Nothing is consumable. Show every delegation and what it would take.
	glb.Infof("none of your %d delegation(s) can be topped up right now:", len(all))
	sort.Slice(frozen, func(i, j int) bool {
		return frozen[i].view.UnfreezeSlot(consts) < frozen[j].view.UnfreezeSlot(consts)
	})
	for _, c := range frozen {
		un := c.view.UnfreezeSlot(consts)
		glb.Infof("   %s  %20s  frozen, window opens at slot %d (%d slots)",
			c.view.ChainID.StringShort(), util.Th(c.balance), un, un-slot)
	}

	nearest := frozen[0]
	un := nearest.view.UnfreezeSlot(consts)
	glb.Infof("")
	glb.Infof("soonest window: %s in %d slot(s)", nearest.view.ChainID.StringShort(), un-slot)
	if comp, err := askStopCompensation(clnt, nearest, un); err == nil {
		glb.Infof("stopping it now returns %s of advance that has not been earned;", util.Th(comp))
		glb.Infof("the next freeze pays a fresh advance over a full span, so this is not a fee.")
	}
	glb.Infof("")
	prompt := fmt.Sprintf("ask sequencer %s to stop %s now?",
		nearest.view.Target.StringShort(), nearest.view.ChainID.StringShort())
	if !glb.YesNoPrompt(prompt, false) {
		glb.Infof("nothing done. Wait for the window, or re-run with --delegation")
		return
	}
	// Hand off; the delegation appears on hold once the target processes the
	// request, and `topup` will find it on the next run.
	runRevokeDelegationCmd(nil, []string{nearest.view.ChainID.StringHex()})
	glb.Infof("")
	glb.Infof("re-run `dlg topup %s` once the delegation shows as on hold", args[0])
}

// splitByAccess separates delegations the master can consume now from those
// still inside a freeze.
func splitByAccess(all []*topUpCandidate, slot uint32, c *txbuildercore.Constants) (open, frozen []*topUpCandidate) {
	for _, d := range all {
		if d.view.IsInFrozenSlot(slot, c) {
			frozen = append(frozen, d)
		} else {
			open = append(open, d)
		}
	}
	return
}

func accessLabel(d *topUpCandidate) string {
	switch {
	case d.view.IsMarkedOnHold():
		return "on hold"
	case d.view.IsMarkedFrozen():
		return "inside its safe revocation window"
	default:
		return "not frozen"
	}
}

// askStopCompensation is what stopping the delegation now would return: the
// unearned part of the advance, at the share pinned when it was frozen.
// Mirrors _projectedCompensation in ensure.easyfl, anchored on the output's own
// slot so wallet and constraint agree.
func askStopCompensation(clnt *client.APIClient, d *topUpCandidate, unfreeze uint32) (uint64, error) {
	if unfreeze <= d.oid.Slot() {
		return 0, nil
	}
	uncut := evalChainInflationMultiStep(clnt, d.balance, d.oid.Slot(), unfreeze-d.oid.Slot())
	return uncut * uint64(d.view.AdvanceShare) / 1000, nil
}

// delegateChainWithAdd runs the `delegate chain` builder with --add set, so
// there is exactly one place that composes a delegation transition.
func delegateChainWithAdd(chainID base.ChainID, amount uint64) {
	addAmount = amount
	runDelegationSubmitCmd(nil, []string{chainID.StringHex()})
}
