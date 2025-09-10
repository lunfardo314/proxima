package delegation

import (
	"os"

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
		Args:  cobra.MinimumNArgs(1),
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
