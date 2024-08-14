package snapshot_cmd

import (
	"encoding/hex"

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
		Use:   "restore",
		Short: "creates multi-state db from snapshot",
		Args:  cobra.ExactArgs(1),
		Run:   runRestoreCmd,
	}

	restoreCmd.InitDefaultHelpCmd()
	return restoreCmd
}

const (
	cacheSize = 5000
	batchSize = 10000
)

func runRestoreCmd(_ *cobra.Command, args []string) {
	header, id, branchID, rootRecord, kvStream, err := multistate.OpenSnapshotFileStream(args[0])
	glb.AssertNoError(err)
	glb.Infof("snapshot file ok. Format version: %s", header.Version)
	glb.Infof("root record: %s", rootRecord.StringShort())
	glb.Infof("ledger id:\n%s", id.String())
	defer func() { _ = kvStream.Close() }()

	stateDb := badger_adaptor.MustCreateOrOpenBadgerDB(global.MultiStateDBName, badger.DefaultOptions(global.MultiStateDBName))
	stateStore := badger_adaptor.New(stateDb)
	defer func() { _ = stateStore.Close() }()

	emptyRoot, err := multistate.CommitEmptyRootWithLedgerIdentity(*id, stateStore)
	glb.AssertNoError(err)

	trieUpdatable, err := immutable.NewTrieUpdatable(ledger.CommitmentModel, stateStore, emptyRoot, cacheSize)
	glb.AssertNoError(err)

	var batch common.KVBatchedWriter
	var inBatch int
	var lastRoot common.VCommitment

	n := 0
	err = kvStream.Iterate(func(k []byte, v []byte) bool {
		if util.IsNil(batch) {
			batch = stateStore.BatchedWriter()
		}

		already := trieUpdatable.Update(k, v)
		glb.Assertf(!already, "repeating key %s", hex.EncodeToString(k))
		inBatch++

		if inBatch == batchSize {
			lastRoot = trieUpdatable.Commit(batch)
			err = batch.Commit()
			util.AssertNoError(err)
			inBatch = 0
			batch = nil
		}
		n++
		return true
	})
	glb.AssertNoError(err)
	if !util.IsNil(batch) {
		lastRoot = trieUpdatable.Commit(batch)
		err = batch.Commit()
		util.AssertNoError(err)
	}

	glb.Assertf(ledger.CommitmentModel.EqualCommitments(lastRoot, rootRecord.Root), ""+
		"inconsistency: final root is not equal to the root record")

	batch = stateStore.BatchedWriter()
	multistate.WriteLatestSlotRecord(batch, branchID.Slot())
	multistate.WriteEarliestSlotRecord(batch, branchID.Slot())
	multistate.WriteRootRecord(batch, *branchID, *rootRecord)
	err = batch.Commit()
	glb.AssertNoError(err)
}
