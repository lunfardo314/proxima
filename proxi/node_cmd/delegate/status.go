package delegate

import (
	"os"
	"time"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

func initDelegationStatusCmd() *cobra.Command {
	statusCmd := &cobra.Command{
		Use:   "status [<delegation ID>]",
		Short: `displays status of a specific delegation or all delegation controlled by the wallet`,
		Args:  cobra.MaximumNArgs(1),
		Run:   runDelegationStatusCmd,
	}
	statusCmd.InitDefaultHelpCmd()

	return statusCmd
}

func runDelegationStatusCmd(_ *cobra.Command, args []string) {
	walletAccount := glb.GetWalletAccount()
	lib := glb.GetTxLibrary()
	consts := glb.GetLedgerConstants()

	clnt := glb.GetClient()
	if len(args) >= 1 {
		delegationID, err := base.ChainIDFromHexString(args[0])
		glb.AssertNoError(err)
		out, lrbid, err := clnt.GetChainOutput(delegationID)
		glb.Assertf(err == nil, "cannot to retrieve delegation %s: %v", delegationID.String(), err)
		view, ok, err := lib.ParseDelegationOutput(out.Output.Output, out.ID)
		glb.AssertNoError(err)
		glb.Assertf(ok, "unable to retrieve delegation output with ID %s", out.ID.String())
		glb.PrintLRB(&lrbid)
		if glb.IsVerbose() {
			// Inline mini-dump — the full ledger.DelegationOutput.LinesHRFull
			// display is singleton-bound and deferred (see seq info / claude
			// wallet_eval_api Phase D follow-up). The wallet view's fields
			// cover the essentials.
			glb.Infof("    delegation %s", view.ChainID.String())
			glb.Infof("    target:           %s", view.Target.String())
			glb.Infof("    master:           %s", view.MasterID.String())
			glb.Infof("    origin slot:      %d", view.OriginSlot)
			glb.Infof("    epoch slots:      %d", consts.DelegationEpochSlots)
			glb.Infof("    max frozen:       %d", consts.DelegationMaxFrozenEpochs)
			glb.Infof("    last frozen epoch:%d", view.LastFrozenEpoch)
			glb.Infof("    balance:          %s", util.Th(out.Output.TokenBalance()))
		}

		nowslot := glb.GetLedgerTimeNow().Slot
		if view.IsInFrozenSlot(nowslot, consts) {
			unfreeze := view.UnfreezeSlot(consts)
			glb.Infof("delegation %s is FROZEN in the current slot %d until slot %d", delegationID.String(), nowslot, unfreeze)
			glb.Infof("frozen balance is %s", util.Th(out.Output.TokenBalance()))
			unfreezeTime := consts.ClockTime(base.T(unfreeze, 0))
			left := time.Until(unfreezeTime)
			unfreezeTimeFmt := unfreezeTime.Format("2006-01-02 15:04:05")
			leftHours := left / time.Hour
			leftMinutes := (left % (60 * time.Minute)) / time.Minute
			glb.Infof("safe revocation window starts in slot %d (at %s, %d hours and %d minutes from now)",
				unfreeze, unfreezeTimeFmt, leftHours, leftMinutes)
		} else if view.IsMarkedOnHold() {
			glb.Infof("delegation %s is on hold", delegationID.StringShort())
			glb.Infof("balance is %s", util.Th(out.Output.TokenBalance()))
		}
		return
	}

	res, err := glb.GetClient().GetOutputsForControllerID(walletAccount.ControllerID(), client.GetOutputsParams{
		LockType:   api.GetOutputsLockTypeDelegateMaster,
		Chained:    client.ChainedOnly(),
		MaxOutputs: api.GetOutputsIterationCap,
	})
	glb.AssertNoError(err)
	glb.PrintLRB(&res.LRBID)
	if len(res.Outputs) == 0 {
		glb.Infof("no delegation outputs controlled by %s has been found", walletAccount.String())
		os.Exit(0)
	}

	type displayRow struct {
		chainID base.ChainID
		balance uint64
		target  base.ChainID
	}
	rows := make([]displayRow, 0, len(res.Outputs))
	for _, o := range res.Outputs {
		view, ok, err := lib.ParseDelegationOutput(o.Output.Output, o.ID)
		if err != nil || !ok {
			continue
		}
		rows = append(rows, displayRow{
			chainID: view.ChainID,
			balance: o.Output.TokenBalance(),
			target:  view.Target,
		})
	}
	glb.Infof("found %d delegation outputs controlled by %s:", len(rows), walletAccount.String())
	for _, r := range rows {
		glb.Infof("   %s %s -> %s", r.chainID.String(), util.Th(r.balance), r.target.String())
	}
}
