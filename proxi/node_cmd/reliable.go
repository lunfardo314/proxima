package node_cmd

import (
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initReliableBranchCmd() *cobra.Command {
	reliableBranchCmd := &cobra.Command{
		Use:   "reliable_branch",
		Short: `retrieves latest reliable branch info from the node`,
		Args:  cobra.NoArgs,
		Run:   runReliableBranchCmd,
	}

	reliableBranchCmd.InitDefaultHelpCmd()
	return reliableBranchCmd
}

func runReliableBranchCmd(_ *cobra.Command, _ []string) {
	glb.InitLedgerFromNode()
	//
	rootRecord, branchID, err := glb.GetClient().GetLatestReliableBranch()
	glb.AssertNoError(err)

	nowis := ledger.TimeNow()
	glb.Infof("---\nlatest reliable branch is %d slots back from now:", nowis.Slot()-branchID.Slot())
	glb.Infof("   branch ID: %s", branchID.String())
	glb.Infof("   root record:\n%s", rootRecord.Lines("     ").String())
}
