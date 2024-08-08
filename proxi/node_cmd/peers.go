package node_cmd

import (
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initPeersInfoCmd() *cobra.Command {
	getPeersInfoCmd := &cobra.Command{
		Use:   "peers",
		Short: `retrieves peers info from the node`,
		Args:  cobra.NoArgs,
		Run:   runPeersInfoCmd,
	}

	getPeersInfoCmd.InitDefaultHelpCmd()
	return getPeersInfoCmd
}

func runPeersInfoCmd(_ *cobra.Command, _ []string) {
	glb.InitLedgerFromNode()
	//
	peersInfo, err := glb.GetClient().GetPeersInfo()
	glb.AssertNoError(err)
	for p := range peersInfo.Peers {
		glb.Infof("        %s : %s", peersInfo.Peers[p].ID, peersInfo.Peers[p].MultiAddresses[0])
	}
}
