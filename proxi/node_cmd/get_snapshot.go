package node_cmd

import (
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initGetSnapshotCmd() *cobra.Command {
	getSnapshotCmd := &cobra.Command{
		Use:   "get_snapshot",
		Short: "downloads latest snapshot from the node",
		Args:  cobra.NoArgs,
		Run:   runGetSnapshotCmd,
	}
	getSnapshotCmd.Flags().StringP("output", "o", "", "output file path (default: use server-provided filename)")
	return getSnapshotCmd
}

func runGetSnapshotCmd(cmd *cobra.Command, _ []string) {
	outputFile, _ := cmd.Flags().GetString("output")

	clnt := glb.GetClient()
	glb.Infof("downloading snapshot from the node...")
	savedPath, err := clnt.DownloadSnapshot(outputFile)
	glb.AssertNoError(err)
	glb.Infof("snapshot saved to '%s'", savedPath)
}
