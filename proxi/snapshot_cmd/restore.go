package snapshot_cmd

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/dgraph-io/badger/v4"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/adaptors/badger_adaptor"
	"github.com/lunfardo314/unitrie/common"
	"github.com/lunfardo314/unitrie/immutable"
	"github.com/spf13/cobra"
)

func initRestoreCmd() *cobra.Command {
	restoreCmd := &cobra.Command{
		Use:   "restore <snapshot file>",
		Short: "creates multi-state db from snapshot",
		Args:  cobra.ExactArgs(1),
		Run:   runRestoreCmd,
	}

	restoreCmd.InitDefaultHelpCmd()
	return restoreCmd
}

const (
	trieCacheSize = 100_000
	batchSize     = 100_000
)

func runRestoreCmd(_ *cobra.Command, args []string) {
	kvStream, err := multistate.OpenSnapshotFileStream(args[0])
	glb.AssertNoError(err)
	defer kvStream.Close()

	glb.Infof("snapshot file ok. Format version: %s", kvStream.Header.Version)
	glb.Infof("root record: %s", kvStream.RootRecord.Lines("    ").String())
	glb.Infof("ledger id:\n%s", kvStream.LedgerID.Lines("    ").String())

	stateDb := badger_adaptor.MustCreateOrOpenBadgerDB(global.MultiStateDBName, badger.DefaultOptions(global.MultiStateDBName))
	stateStore := badger_adaptor.New(stateDb)
	defer func() { _ = stateStore.Close() }()

	emptyRoot, err := multistate.CommitEmptyRootWithLedgerIdentity(*kvStream.LedgerID, stateStore)
	glb.AssertNoError(err)

	trieUpdatable, err := immutable.NewTrieUpdatable(ledger.CommitmentModel, stateStore, emptyRoot, trieCacheSize)
	glb.AssertNoError(err)

	var batch common.KVBatchedWriter
	var inBatch int
	var lastRoot common.VCommitment

	console := io.Discard
	if glb.IsVerbose() {
		console = os.Stdout
	}
	counter := 0
	for pair := range kvStream.InChan {
		if util.IsNil(batch) {
			batch = stateStore.BatchedWriter()
		}
		already := trieUpdatable.Update(pair.Key, pair.Value)
		glb.Assertf(!already, "repeating key %s", hex.EncodeToString(pair.Key))
		inBatch++

		_outKVPair(pair.Key, pair.Value, counter, console)
		counter++

		if inBatch == batchSize {
			lastRoot = trieUpdatable.Commit(batch)
			err = batch.Commit()
			util.AssertNoError(err)
			inBatch = 0
			batch = nil
			_, _ = fmt.Fprintf(console, "--- commit ---\n")
		}
	}
	if !util.IsNil(batch) {
		lastRoot = trieUpdatable.Commit(batch)
		err = batch.Commit()
		util.AssertNoError(err)
		_, _ = fmt.Fprintf(console, "--- commit ---\n")
	}
	// write meta-records
	batch = stateStore.BatchedWriter()
	multistate.WriteLatestSlotRecord(batch, kvStream.BranchID.Slot())
	multistate.WriteEarliestSlotRecord(batch, kvStream.BranchID.Slot())
	multistate.WriteRootRecord(batch, kvStream.BranchID, kvStream.RootRecord)

	err = batch.Commit()
	glb.AssertNoError(err)

	glb.Assertf(ledger.CommitmentModel.EqualCommitments(lastRoot, kvStream.RootRecord.Root),
		"inconsistency: final root %s is not equal to the root in the root record %s",
		lastRoot.String(), kvStream.RootRecord.Root.String())
}
