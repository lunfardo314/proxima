package snapshot_cmd

import (
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func initSnapshotCheckCmd() *cobra.Command {
	snapshotCheckCmd := &cobra.Command{
		Use:   "check [<snapshot file name>]",
		Short: "reads snapshot file and checks if branch is part opf the LRB on the node",
		Args:  cobra.MaximumNArgs(1),
		Run:   runSnapshotCheckCmd,
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			glb.ReadInConfig()
		},
	}

	snapshotCheckCmd.InitDefaultHelpCmd()
	return snapshotCheckCmd
}

func runSnapshotCheckCmd(_ *cobra.Command, args []string) {
	glb.InitLedgerFromNode()
	clnt := glb.GetClient()

	var fname string
	var ok bool
	if len(args) == 0 {
		fname, ok = findLatestSnapshotFile()
		glb.Assertf(ok, "can't find snapshot file")
	} else {
		fname = args[0]
	}

	glb.Infof("reading snapshot file %s", fname)
	ssData, err := readASnapshotFile(fname)
	glb.AssertNoError(err)

	glb.Infof("snapshot format version: %s", ssData.fmtVersion)
	glb.Infof("snapshot branch ID: %s", ssData.branchID.String())
	glb.Infof("snapshot root record:\n%s", ssData.rootRecord.Lines("    ").String())

	if ssData.ledgerID.Hash() != ledger.L().ID.Hash() {
		glb.Infof("ledger ID hash in snapshot file %s is not equal to the ledger ID hash on the node on '%s'.\nThe snapshot file CANNOT BE USED to start a node",
			fname, viper.GetString("api.endpoint"))
		return
	}

	lrbID, included, err := clnt.CheckTransactionIDInLRB(ssData.branchID)
	glb.AssertNoError(err)
	glb.Infof("\n-----------------------\nlatest reliable branch (LRB) is %s", lrbID.String())
	if included {
		glb.Infof("the snapshot:")
		glb.Infof("      - is INCLUDED in the current LRB of the network. It CAN BE USED to start a node")
		glb.Infof("      - is %d slots back from LRB and %d slots back from now", lrbID.Slot()-ssData.branchID.Slot(), ledger.TimeNow().Slot()-ssData.branchID.Slot())
	} else {
		glb.Infof("the snapshot is NOT INCLUDED in the current LRB of the network. It CANNOT BE USED to start a node")
	}
}

type _snapshotFileData struct {
	fmtVersion string
	branchID   ledger.TransactionID
	rootRecord multistate.RootRecord
	ledgerID   *ledger.IdentityData
}

func readASnapshotFile(fname string) (*_snapshotFileData, error) {
	kvStream, err := multistate.OpenSnapshotFileStream(fname)
	if err != nil {
		return nil, err
	}
	defer kvStream.Close()

	return &_snapshotFileData{
		fmtVersion: kvStream.Header.Version,
		branchID:   kvStream.BranchID,
		rootRecord: kvStream.RootRecord,
		ledgerID:   kvStream.LedgerID,
	}, nil
}
