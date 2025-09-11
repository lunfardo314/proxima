package delegation

import (
	"os"
	"time"

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
	wallet := glb.GetWalletData()

	clnt := glb.GetClient()
	if len(args) >= 1 {
		delegationID, err := base.ChainIDFromHexString(args[0])
		glb.AssertNoError(err)
		out, _, lrbid, err := clnt.GetChainOutput(delegationID)
		glb.AssertNoError(err)
		dOut, ok := ledger.AsDelegationOutput(out.Output, out.ID)
		glb.Assertf(ok, "unable to retrieve delegation output with ID %s", out.ID.String())
		glb.PrintLRB(&lrbid)
		glb.Infof("%s", dOut.LinesHR("    ").String())

		nowslot := uint32(ledger.TimeNow().Slot)
		glb.Infof("current slot is %d", nowslot)
		if dOut.IsInFrozenSlot(nowslot) {
			unfreeze := dOut.UnfreezeSlot()
			glb.Infof("delegation is FROZEN in the current slot %d until slot %d", nowslot, unfreeze)
			unfreezeTs := base.NewLedgerTime(base.Slot(unfreeze), 0)
			unfreezeTime := ledger.ClockTime(unfreezeTs)
			left := time.Until(unfreezeTime)
			unfreezeTimeFmt := unfreezeTime.Format("2006-01-02 15:04:05")
			leftHours := left / time.Hour
			leftMinutes := (left % (60 * time.Minute)) / time.Minute
			glb.Infof("safe revocation window starts in slot %d (at %s, %d hours and %d minutes from now)",
				unfreeze, unfreezeTimeFmt, leftHours, leftMinutes)
		}
		return
	}

	dOuts, lrbid, err := glb.GetClient().GetDelegationOutputs(wallet.Account)
	glb.AssertNoError(err)
	glb.PrintLRB(lrbid)
	if len(dOuts) == 0 {
		glb.Infof("no delegation outputs controlled by %s has been found", wallet.Account.String())
		os.Exit(0)
	}

	glb.Infof("found %d delegation outputs controlled by %s:", len(dOuts), wallet.Account.String())
	for _, dOut := range dOuts {
		glb.Infof("   %s %s -> %s", dOut.ChainID.String(), util.Th(dOut.Output.TokenBalance()), dOut.Target.ChainID())
	}
}
