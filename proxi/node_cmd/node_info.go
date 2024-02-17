package node_cmd

import (
	"github.com/lunfardo314/proxima/proxi/glb"
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
	glb.InitLedgerFromNode()

	nodeInfo, err := glb.GetClient().GetNodeInfo()
	glb.AssertNoError(err)
	glb.Infof(nodeInfo.Lines("    ").String())
}
