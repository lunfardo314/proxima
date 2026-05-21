package node_cmd

import (
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

func initSyncInfoCmd() *cobra.Command {
	getSyncInfoCmd := &cobra.Command{
		Use:   "sync",
		Short: `retrieves sync info from the node`,
		Args:  cobra.NoArgs,
		Run:   runSyncInfoCmd,
	}

	getSyncInfoCmd.InitDefaultHelpCmd()
	return getSyncInfoCmd
}

func runSyncInfoCmd(_ *cobra.Command, _ []string) {
	syncInfo, err := glb.GetClient().GetSyncInfo()
	glb.AssertNoError(err)
	glb.Infof("  node synced:  %v", syncInfo.Synced)
	glb.Infof("  current slot: %v", syncInfo.CurrentSlot)
	glb.Infof("  LRB slot:     %v", syncInfo.LrbSlot)
	glb.Infof("  ledger coverage:     %s", util.Th(syncInfo.LedgerCoverage))
}
