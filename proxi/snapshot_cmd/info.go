package snapshot_cmd

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
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
	kvStream, err := multistate.OpenSnapshotFileStream(args[0])
	glb.AssertNoError(err)
	defer kvStream.Close()

	glb.Infof("snapshot file ok. Format version: %s", kvStream.Header.Version)
	glb.Infof("branch ID: %s", kvStream.BranchID.String())
	glb.Infof("root record:\n%s", kvStream.RootRecord.Lines("    ").String())
	glb.Infof("ledger id:\n%s", kvStream.LedgerID.Lines("    ").String())

	if !glb.IsVerbose() {
		return
	}

	counter := 0
	for pair := range kvStream.InChan {
		_outKVPair(pair.Key, pair.Value, counter, os.Stdout)
		counter++
	}
}

func _outKVPair(k, v []byte, counter int, out io.Writer) {
	common.Assert(len(k) > 0, "len(k)>0")

	_, _ = fmt.Fprintf(out, "rec #%d: %s %s, value len: %d\n",
		counter, multistate.PartitionToString(k[0]), hex.EncodeToString(k[1:]), len(v))
}
