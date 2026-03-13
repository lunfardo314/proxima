package node_cmd

import (
	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initUpgradesCmd() *cobra.Command {
	upgradesCmd := &cobra.Command{
		Use:   "upgrades",
		Short: "displays ledger upgrades (in effect and pending) from the node",
		Args:  cobra.NoArgs,
		Run:   runNodeUpgradesCmd,
	}

	upgradesCmd.InitDefaultHelpCmd()
	return upgradesCmd
}

func runNodeUpgradesCmd(_ *cobra.Command, _ []string) {
	clnt := glb.GetClient()

	// Get current slot from sync info
	syncInfo, err := clnt.GetSyncInfo()
	glb.AssertNoError(err)
	currentSlot := syncInfo.CurrentSlot

	// Walk the upgrade chain from latest back to genesis
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

	glb.Infof("Ledger Upgrades (current slot: %d)", currentSlot)
	glb.Infof("==========================================")

	// Display in chronological order (genesis first)
	for i := len(upgrades) - 1; i >= 0; i-- {
		u := upgrades[i]
		status := "IN EFFECT"
		if u.UpgradeSlot > currentSlot {
			status = "PENDING"
		}
		glb.Infof("   slot %8d: %s  %s", u.UpgradeSlot, u.LibraryHash, status)
	}
}
