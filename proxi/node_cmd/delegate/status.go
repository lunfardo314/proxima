package delegate

import (
	"os"
	"time"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
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
	glb.InitLedgerFromNode()
	walletAccount := glb.GetWalletAccount()

	clnt := glb.GetClient()
	if len(args) >= 1 {
		delegationID, err := base.ChainIDFromHexString(args[0])
		glb.AssertNoError(err)
		out, lrbid, err := clnt.GetChainOutput(delegationID)
		glb.Assertf(err == nil, "cannot to retrieve delegation %s: %v", delegationID.String(), err)
		dOut, ok := ledger.AsDelegationOutput(out.Output, out.ID)
		glb.Assertf(ok, "unable to retrieve delegation output with ID %s", out.ID.String())
		glb.PrintLRB(&lrbid)
		glb.Verbosef("%s", dOut.LinesHRFull("    ").String())

		nowslot := ledger.TimeNow().Slot
		if dOut.IsInFrozenSlot(nowslot) {
			unfreeze := dOut.UnfreezeSlot()
			glb.Infof("delegation %s is FROZEN in the current slot %d until slot %d", delegationID.String(), nowslot, unfreeze)
			glb.Infof("frozen balance is %s", util.Th(dOut.Output.TokenBalance()))
			unfreezeTs := base.T(unfreeze, 0)
			unfreezeTime := ledger.ClockTime(unfreezeTs)
			left := time.Until(unfreezeTime)
			unfreezeTimeFmt := unfreezeTime.Format("2006-01-02 15:04:05")
			leftHours := left / time.Hour
			leftMinutes := (left % (60 * time.Minute)) / time.Minute
			glb.Infof("safe revocation window starts in slot %d (at %s, %d hours and %d minutes from now)",
				unfreeze, unfreezeTimeFmt, leftHours, leftMinutes)
		} else if dOut.IsMarkedOnHold() {
			glb.Infof("delegation %s is REVOKED", delegationID.StringShort())
			glb.Infof("balance is %s", util.Th(dOut.Output.TokenBalance()))
		}
		return
	}

	res, err := glb.GetClient().GetOutputs(walletAccount.ControllerID(), client.GetOutputsParams{
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

	dOuts := make([]ledger.DelegationOutput, 0, len(res.Outputs))
	for _, o := range res.Outputs {
		dOut, ok := ledger.AsDelegationOutput(o.Output, o.ID)
		if !ok {
			continue
		}
		dOuts = append(dOuts, dOut)
	}
	glb.Infof("found %d delegation outputs controlled by %s:", len(dOuts), walletAccount.String())
	for _, dOut := range dOuts {
		targetID := dOut.Target
		glb.Infof("   %s %s -> %s", dOut.ChainID.String(), util.Th(dOut.Output.TokenBalance()), targetID.String())
	}
}
