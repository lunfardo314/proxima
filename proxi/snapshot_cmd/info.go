package snapshot_cmd

import (
	"encoding/json"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
	"github.com/spf13/cobra"
)

func initSnapshotInfoCmd() *cobra.Command {
	snapshotInfoCmd := &cobra.Command{
		Use:   "info",
		Short: "reads snapshot file and displays main info",
		Args:  cobra.ExactArgs(1),
		Run:   runSnapshotInfoCmd,
	}

	snapshotInfoCmd.InitDefaultHelpCmd()
	return snapshotInfoCmd
}

func runSnapshotInfoCmd(_ *cobra.Command, args []string) {
	iter, err := common.OpenKVStreamFile(args[0])
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
			var rr multistate.RootRecord
			rr, err = multistate.RootRecordFromBytes(v)
			glb.AssertNoError(err)

			glb.Infof("%s", rr.StringShort())
		case 2:
			util.Assertf(len(k) == 0, "wrong second key/value pair")
			var id *ledger.IdentityData
			id, err = ledger.IdentityDataFromBytes(v)
			glb.AssertNoError(err)
			glb.Infof("Ledger identity:\n%s", id.String())
		default:
		}
		n++
		return true
	})
	glb.AssertNoError(err)

	glb.Infof("total %d records", n)
}
