package node_cmd

import (
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
	consts := glb.GetLedgerConstants()

	earliestBranchIDs, err := glb.GetClient().GetEarliestBranchIDs()
	glb.AssertNoError(err)
	glb.Infof("earliest retained branches (floor, heaviest first):")
	for _, id := range earliestBranchIDs {
		glb.Infof("   %s (hex = %s)", id.String(), id.StringHex())
	}

	rootRecord, branchID, err := glb.GetClient().GetLatestReliableBranch()
	glb.AssertNoError(err)

	nowis := glb.GetLedgerTimeNow()
	glb.Infof("---\nlatest reliable branch (LRB) is %d slots back from now:", nowis.Slot-branchID.Slot())
	glb.Infof("   branch id: %s, hex: %s", branchID.String(), branchID.StringHex())
	if glb.IsVerbose() {
		glb.Infof("   root record (verbose):\n%s", rootRecord.LinesVerbose(consts.HealthyCoverageNumerator, consts.HealthyCoverageDenominator, "     ").String())
	} else {
		glb.Infof("   root record:\n%s", rootRecord.Lines(consts.HealthyCoverageNumerator, consts.HealthyCoverageDenominator, "     ").String())
	}
}
