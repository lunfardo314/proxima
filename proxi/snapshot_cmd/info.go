package snapshot_cmd

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

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
		Args:  cobra.MaximumNArgs(1),
		Run:   runSnapshotInfoCmd,
	}

	snapshotInfoCmd.InitDefaultHelpCmd()
	return snapshotInfoCmd
}

func runSnapshotInfoCmd(_ *cobra.Command, args []string) {
	var fname string
	if len(args) == 0 {
		entries, err := os.ReadDir(".")
		glb.AssertNoError(err)

		entries = util.PurgeSlice(entries, func(entry os.DirEntry) bool {
			ok, err := filepath.Match("*.snapshot", entry.Name())
			if err == nil {
				_, err = entry.Info()
			}
			return err == nil && ok
		})

		glb.Assertf(len(entries) > 0, "no snapshot files found")
		sort.Slice(entries, func(i, j int) bool {
			fii, _ := entries[i].Info()
			fij, _ := entries[j].Info()
			return fii.ModTime().After(fij.ModTime())
		})
		fname = entries[0].Name()
	} else {
		fname = args[0]
	}

	kvStream, err := multistate.OpenSnapshotFileStream(fname)
	glb.AssertNoError(err)
	defer kvStream.Close()

	glb.Infof("snapshot file: %s", fname)
	glb.Infof("format version: %s", kvStream.Header.Version)
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
