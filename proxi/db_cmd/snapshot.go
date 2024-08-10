package db_cmd

import (
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

func initSnapshotCmd() *cobra.Command {
	snapshotCmd := &cobra.Command{
		Use:   "snapshot",
		Short: "writes state snapshot to file",
		Args:  cobra.NoArgs,
		Run:   runSnapshotCmd,
	}

	snapshotCmd.InitDefaultHelpCmd()
	return snapshotCmd
}

type dummyStreamWriter struct{}

func (d dummyStreamWriter) Write(key, value []byte) error {
	return nil
}

func (d dummyStreamWriter) Stats() (int, int) {
	return 0, 0
}

func runSnapshotCmd(_ *cobra.Command, _ []string) {
	glb.InitLedgerFromDB()

	fraction := global.Fraction23
	latestHealthySlot, exists := multistate.FindLatestHealthySlot(glb.StateStore(), fraction)
	glb.Assertf(exists, "healthy slot (%s) does not exist. Can't proceed", fraction.String())

	glb.Infof("latest healthy (%s) slot is %d\nNow is slot %s", fraction.String(), latestHealthySlot, ledger.TimeNow().Slot())

	roots := multistate.FetchRootRecords(glb.StateStore(), latestHealthySlot)
	root := util.Maximum(roots, func(r1, r2 multistate.RootRecord) bool {
		return r1.LedgerCoverage < r2.LedgerCoverage
	})

	branch := multistate.FetchBranchDataByRoot(glb.StateStore(), root)
	glb.Infof("using:     branch %s\n root:     %s", util.Ref(branch.Stem.ID.TransactionID()).String(), branch.Root.String())

	writer := dummyStreamWriter{}

	err := multistate.WriteSnapshot(glb.StateStore(), writer, root.Root)
	glb.AssertNoError(err)
}
