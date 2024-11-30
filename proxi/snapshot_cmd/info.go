package snapshot_cmd

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

func initSnapshotInfoCmd() *cobra.Command {
	snapshotInfoCmd := &cobra.Command{
		Use:   "info [<snapshot file name>]",
		Short: "reads snapshot file and displays main info",
		Args:  cobra.MaximumNArgs(1),
		Run:   runSnapshotInfoCmd,
	}

	snapshotInfoCmd.InitDefaultHelpCmd()
	return snapshotInfoCmd
}

func runSnapshotInfoCmd(_ *cobra.Command, args []string) {
	var fname string
	var ok bool
	if len(args) == 0 {
		fname, ok = findLatestSnapshotFile()
		glb.Assertf(ok, "can't find snapshot file")
	} else {
		fname = args[0]
	}

	kvStream, err := multistate.OpenSnapshotFileStream(fname)
	glb.AssertNoError(err)
	defer kvStream.Close()

	glb.Infof("Verbosity level: %d", glb.VerbosityLevel())
	glb.Infof("snapshot file: %s", fname)
	glb.Infof("format version: %s", kvStream.Header.Version)
	glb.Infof("branch ID: %s", kvStream.BranchID.String())
	glb.Infof("root record:\n%s", kvStream.RootRecord.Lines("    ").String())
	glb.Infof("ledger id:\n%s", kvStream.LedgerID.Lines("    ").String())

	switch glb.VerbosityLevel() {
	case 1:
		counters := make(map[byte]int)
		total := 0
		for pair := range kvStream.InChan {
			counters[pair.Key[0]] = counters[pair.Key[0]] + 1
			total++
		}
		glb.Infof("Total %d records. By type:", total)
		for _, k := range util.KeysSorted(counters, func(k1, k2 byte) bool { return k1 < k2 }) {
			glb.Infof("    %s: %d", multistate.PartitionToString(k), counters[k])
		}

	case 2:
		counter := 0
		for pair := range kvStream.InChan {
			_outKVPair(pair.Key, pair.Value, counter, os.Stdout)
			counter++
		}
	}
}

func findLatestSnapshotFile() (string, bool) {
	lst, err := listSnapshotFiles()
	glb.AssertNoError(err)
	if len(lst) == 0 {
		return "", false
	}
	return lst[0], true
}

func _outKVPair(k, v []byte, counter int, out io.Writer) {
	glb.Assertf(len(k) > 0, "len(k)>0")

	_, _ = fmt.Fprintf(out, "rec #%d: %s %s, value len: %d\n",
		counter, multistate.PartitionToString(k[0]), hex.EncodeToString(k[1:]), len(v))
}

// listSnapshotFiles returns sorted snapshot files in time-descending order
func listSnapshotFiles() ([]string, error) {
	entries, err := os.ReadDir(".")
	if err != nil {
		return nil, err
	}

	var ok bool
	var fi os.FileInfo

	entries = util.PurgeSlice(entries, func(entry os.DirEntry) bool {
		fi, err = entry.Info()
		if err != nil || strings.HasPrefix(entry.Name(), multistate.TmpSnapshotFileNamePrefix) || fi.Mode()&os.ModeType != 0 {
			return false
		}
		ok, err = filepath.Match("*.snapshot", entry.Name())
		return err == nil && ok
	})
	if len(entries) == 0 {
		return nil, nil
	}

	sort.Slice(entries, func(i, j int) bool {
		fii, _ := entries[i].Info()
		fij, _ := entries[j].Info()
		return fii.ModTime().After(fij.ModTime())
	})
	ret := make([]string, 0, len(entries))
	for i := range entries {
		ret = append(ret, entries[i].Name())
	}
	return ret, nil
}
