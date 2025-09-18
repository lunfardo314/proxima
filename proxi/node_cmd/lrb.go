package node_cmd

import (
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initReliableBranchCmd() *cobra.Command {
	reliableBranchCmd := &cobra.Command{
		Use:     "lrb",
		Aliases: []string{"reliable_branch"},
		Short:   `retrieves latest reliable branch (lrb) info from the node`,
		Args:    cobra.NoArgs,
		Run:     runReliableBranchCmd,
	}

	reliableBranchCmd.InitDefaultHelpCmd()
	return reliableBranchCmd
}

func runReliableBranchCmd(_ *cobra.Command, _ []string) {
	glb.InitLedgerFromNode()

	snapshotID, err := glb.GetClient().GetSnapshotBranchID()
	glb.AssertNoError(err)
	glb.Infof("snapshot ID: %s (hex = %s)", snapshotID.String(), snapshotID.StringHex())

	rootRecord, branchID, err := glb.GetClient().GetLatestReliableBranch()
	glb.AssertNoError(err)

	nowis := ledger.TimeNow()
	glb.Infof("---\nlatest reliable branch (LRB) is %d slots back from now:", nowis.Slot-branchID.Slot())
	glb.Infof("   branch id: %s, hex: %s", branchID.String(), branchID.StringHex())
	if glb.IsVerbose() {
		glb.Infof("   root record (verbose):\n%s", rootRecord.LinesVerbose("     ").String())
	} else {
		glb.Infof("   root record:\n%s", rootRecord.Lines("     ").String())
	}
}
