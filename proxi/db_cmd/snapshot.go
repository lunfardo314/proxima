package db_cmd

import (
	"encoding/json"
	"time"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
	"github.com/spf13/cobra"
)

type snapshotHeader struct {
	Description string `json:"description"`
	Version     string `json:"version"`
}

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

const versionString = "ver 0"

func runSnapshotCmd(_ *cobra.Command, _ []string) {
	glb.InitLedgerFromDB()

	fraction := global.Fraction23
	latestHealthySlot, exists := multistate.FindLatestHealthySlot(glb.StateStore(), fraction)
	glb.Assertf(exists, "healthy slot (%s) does not exist. Can't proceed", fraction.String())

	glb.Infof("latest healthy (%s) slot is %d\nNow is slot %d", fraction.String(), latestHealthySlot, ledger.TimeNow().Slot())

	roots := multistate.FetchRootRecords(glb.StateStore(), latestHealthySlot)
	root := util.Maximum(roots, func(r1, r2 multistate.RootRecord) bool {
		return r1.LedgerCoverage < r2.LedgerCoverage
	})

	branch := multistate.FetchBranchDataByRoot(glb.StateStore(), root)
	glb.Infof("using:\n     branch: %s\n     root:   %s", util.Ref(branch.Stem.ID.TransactionID()).String(), branch.Root.String())

	header := snapshotHeader{
		Description: "Proxima snapshot file",
		Version:     versionString,
	}

	start := time.Now()
	headerBin, err := json.Marshal(&header)
	glb.AssertNoError(err)

	fname := snapshotFileName(branch)
	glb.Infof("writing state snapshot to file %s", fname)

	target, err := common.CreateKVStreamFile(fname)
	glb.AssertNoError(err)

	// write header with version
	err = target.Write(nil, headerBin)
	glb.AssertNoError(err)

	// write root record
	err = target.Write(nil, root.Bytes())
	glb.AssertNoError(err)

	err = multistate.WriteSnapshot(glb.StateStore(), target, root.Root)
	glb.AssertNoError(err)

	_ = target.Close()
	nRecords, totalBytes := target.Stats()
	glb.Infof("wrote %d key/value pairs, total %d bytes in %v", nRecords, totalBytes, time.Since(start))
	glb.Infof("success")
}

func snapshotFileName(branch multistate.BranchData) string {
	return util.Ref(branch.Stem.ID.TransactionID()).AsFileName() + ".snapshot"
}
