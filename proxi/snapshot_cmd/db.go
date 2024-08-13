package snapshot_cmd

import (
	"encoding/json"
	"time"

	"github.com/lunfardo314/proxima/global"
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

func initSnapshotDBCmd() *cobra.Command {
	snapshotCmd := &cobra.Command{
		Use:   "db",
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

	latestReliableBranch, found := multistate.FindLatestReliableBranch(glb.StateStore(), global.FractionHealthyBranch)
	glb.Assertf(found, "the reliable branch has not been found: cannot proceed with snapshot")

	header := snapshotHeader{
		Description: "Proxima snapshot file",
		Version:     versionString,
	}

	start := time.Now()
	headerBin, err := json.Marshal(&header)
	glb.AssertNoError(err)

	fname := snapshotFileName(latestReliableBranch)
	glb.Infof("writing state snapshot to file %s", fname)

	target, err := common.CreateKVStreamFile(fname)
	glb.AssertNoError(err)

	// write header with version
	err = target.Write(nil, headerBin)
	glb.AssertNoError(err)

	// write root record
	err = target.Write(nil, latestReliableBranch.RootRecord.Bytes())
	glb.AssertNoError(err)

	// write ledger identity record
	err = target.Write(nil, multistate.LedgerIdentityBytesFromRoot(glb.StateStore(), latestReliableBranch.Root))
	glb.AssertNoError(err)

	err = multistate.WriteState(glb.StateStore(), target, latestReliableBranch.Root)
	glb.AssertNoError(err)

	_ = target.Close()
	nRecords, totalBytes := target.Stats()
	glb.Infof("wrote %d key/value pairs, total %d bytes in %v", nRecords, totalBytes, time.Since(start))
	glb.Infof("success")
}

func snapshotFileName(branch *multistate.BranchData) string {
	return util.Ref(branch.Stem.ID.TransactionID()).AsFileName() + ".snapshot"
}
