package snapshot_cmd

import (
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initSnapshotCheckAllCmd() *cobra.Command {
	snapshotCheckAllCmd := &cobra.Command{
		Use:   "check_all [<snapshot file name>]",
		Short: "reads snapshot files in the current directory and checks for file if it can be used to restore state in the network",
		Args:  cobra.MaximumNArgs(1),
		Run:   runSnapshotCheckAllCmd,
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			glb.ReadInConfig()
		},
	}

	snapshotCheckAllCmd.InitDefaultHelpCmd()
	return snapshotCheckAllCmd
}

func runSnapshotCheckAllCmd(_ *cobra.Command, _ []string) {
	glb.InitLedgerFromNode()
	clnt := glb.GetClient()

	fnames, err := listSnapshotFiles()
	glb.AssertNoError(err)

	if len(fnames) == 0 {
		glb.Infof("no snapshot files have been found")
		return
	}

	glb.Infof("----- snapshot files -----")
	for i, fname := range fnames {
		ssData, err := readASnapshotFile(fname)
		if err != nil {
			glb.Infof("#%d %20s read error: %v", i, fname, err)
			continue
		}
		if ssData.ledgerID.Hash() != ledger.L().ID.Hash() {
			glb.Infof("#%d %20s: branchID: %s  ledger ID mismatch: CANNOT BE USED to start a node", i, fname)
			continue
		}

		_, included, err := clnt.CheckTransactionIDInLRB(ssData.branchID)
		if err != nil {
			glb.Infof("#%d %20s: %v", i, fname, err)
			continue
		}
		if included {
			glb.Infof("#%d %20s: branchID: %s, seqID: %s -- CAN BE USED to start a node", i, fname, ssData.branchID.StringShort(), ssData.rootRecord.SequencerID.StringShort())
		} else {
			glb.Infof("#%d %20s: branchID: %s, seqID: %s -- CANNOT BE USED to start a node", i, fname, ssData.branchID.StringShort(), ssData.rootRecord.SequencerID.StringShort())
		}
	}
}
