package node_cmd

import (
	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/spf13/cobra"
)

func initNodeInfoCmd() *cobra.Command {
	getNodeInfoCmd := &cobra.Command{
		Use:   "info",
		Short: `retrieves node info from the node`,
		Args:  cobra.NoArgs,
		Run:   runNodeInfoCmd,
	}

	getNodeInfoCmd.InitDefaultHelpCmd()
	return getNodeInfoCmd
}

func runNodeInfoCmd(_ *cobra.Command, _ []string) {
	nodeInfo, err := glb.GetClient().GetNodeInfo()
	glb.AssertNoError(err)
	glb.Infof("\nNode:\n%s", nodeInfo.Lines("    ").String())

	rootRecord, branchID, err := glb.GetClient().GetLatestReliableBranch()
	glb.AssertNoError(err)

	consts := glb.GetLedgerConstants()
	ln := lines.New("    ")
	ln.Add("branch id: %s", branchID.String()).
		Add("root record:").
		Append(rootRecord.Lines(consts.HealthyCoverageNumerator, consts.HealthyCoverageDenominator, "    "))
	glb.Infof("\nLatest reliable branch (LRB):\n%s", ln.String())

	// Display ledger upgrades
	clnt := glb.GetClient()
	syncInfo, err := clnt.GetSyncInfo()
	glb.AssertNoError(err)
	currentSlot := syncInfo.CurrentSlot

	var upgrades []api.LedgerDefinition
	resp, err := clnt.GetLedgerDefinition(nil)
	glb.AssertNoError(err)
	upgrades = append(upgrades, *resp)

	for resp.UpgradeSlot > 0 {
		prevSlot := resp.PrevUpgradeSlot
		resp, err = clnt.GetLedgerDefinition(&prevSlot)
		glb.AssertNoError(err)
		upgrades = append(upgrades, *resp)
	}

	glb.Infof("\nLedger upgrades (current slot: %d):", currentSlot)
	for i := len(upgrades) - 1; i >= 0; i-- {
		u := upgrades[i]
		status := "IN EFFECT"
		if u.UpgradeSlot > currentSlot {
			status = "PENDING"
		}
		glb.Infof("    slot %8d: %s  %s", u.UpgradeSlot, u.LibraryHash, status)
	}
}
