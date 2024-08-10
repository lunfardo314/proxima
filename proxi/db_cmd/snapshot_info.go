package db_cmd

import (
	"encoding/json"

	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
	"github.com/spf13/cobra"
)

func initSnapshotInfoCmd() *cobra.Command {
	snapshotInfoCmd := &cobra.Command{
		Use:   "snapshot_info",
		Short: "writes state snapshot to file",
		Args:  cobra.ExactArgs(1),
		Run:   runSnapshotInfoCmd,
	}

	snapshotInfoCmd.InitDefaultHelpCmd()
	return snapshotInfoCmd
}

func runSnapshotInfoCmd(_ *cobra.Command, args []string) {

	ReadSnapshotInfo(args[0])
}

func ReadSnapshotInfo(fname string) {
	iter, err := common.OpenKVStreamFile(fname)
	glb.AssertNoError(err)

	n := 0
	err = iter.Iterate(func(k []byte, v []byte) bool {
		switch n {
		case 0:
			util.Assertf(len(k) == 0, "wrong first key/value pair")
			var header snapshotHeader
			err = json.Unmarshal(v, &header)
			glb.AssertNoError(err)

			glb.Infof("%s", string(v))
		case 1:
			util.Assertf(len(k) == 0, "wrong second key/value pair")
			root, err := multistate.RootRecordFromBytes(v)
			glb.AssertNoError(err)

			glb.Infof("%s", root.StringShort())
		default:
		}
		n++
		return true
	})
	glb.AssertNoError(err)

	glb.Infof("total %d records", n)
}
