package snapshot_cmd

import (
	"encoding/hex"
	"encoding/json"

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

func runRestoreCmd(_ *cobra.Command, args []string) {
	iter, err := common.OpenKVStreamFile(args[0])
	glb.AssertNoError(err)

	stateDb := badger_adaptor.MustCreateOrOpenBadgerDB(global.MultiStateDBName, badger.DefaultOptions(global.MultiStateDBName))
	stateStore := badger_adaptor.New(stateDb)
	defer func() { _ = stateStore.Close() }()

	var rootRecord multistate.RootRecord
	var header snapshotHeader
	var emptyRoot, lastRoot common.VCommitment
	var trieUpdatable *immutable.TrieUpdatable
	var batch common.KVBatchedWriter
	var inBatch int

	n := 0
	err = iter.Iterate(func(k []byte, v []byte) bool {
		switch n {
		case 0:
			util.Assertf(len(k) == 0, "wrong first key/value pair")
			err = json.Unmarshal(v, &header)
			glb.AssertNoError(err)

			glb.Infof("%s", string(v))
		case 1:
			util.Assertf(len(k) == 0, "wrong second key/value pair")
			rootRecord, err = multistate.RootRecordFromBytes(v)
			glb.AssertNoError(err)

			glb.Infof("%s", rootRecord.StringShort())
		case 2:
			util.Assertf(len(k) == 0, "wrong second key/value pair")
			var id *ledger.IdentityData
			id, err = ledger.IdentityDataFromBytes(v)
			glb.AssertNoError(err)
			glb.Infof("Ledger identity:\n%s", id.String())

			emptyRoot, err = multistate.CommitEmptyRootWithLedgerIdentity(*id, stateStore)
			glb.AssertNoError(err)

		default:
			const (
				cacheSize = 5000
				batchSize = 5000
			)
			if trieUpdatable == nil {
				trieUpdatable, err = immutable.NewTrieUpdatable(ledger.CommitmentModel, stateStore, emptyRoot, cacheSize)
				glb.AssertNoError(err)
			}
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

	// TODO not finished. Write root record and slot records
}
