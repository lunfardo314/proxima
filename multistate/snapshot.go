package multistate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/unitrie/common"
)

// WriteState writes state with the root as a sequence of key/value pairs.
// Does not write ledger identity record
func WriteState(state global.StateStoreReader, target common.KVStreamWriter, root common.VCommitment, ctx context.Context) error {
	rdr, err := NewReadable(state, root)
	if err != nil {
		return fmt.Errorf("WriteState: %w", err)
	}
	rdr.Iterator(nil).Iterate(func(k, v []byte) bool {
		select {
		case <-ctx.Done():
			err = fmt.Errorf("WriteState: state writing interrupted")
		default:
			if len(k) > 0 {
				// skip ledger identity record
				err = target.Write(k, v)
			}
		}
		return err == nil
	})
	return err
}

type SnapshotHeader struct {
	Description string `json:"description"`
	Version     string `json:"version"`
}

const snapshotFormatVersionString = "ver 0"

func snapshotFileName(branchID ledger.TransactionID) string {
	return branchID.AsFileName() + ".snapshot"
}

// SaveSnapshot writes latest reliable state into snapshot. Returns snapshot file name
func SaveSnapshot(state global.StateStoreReader, ctx context.Context) (*RootRecord, string, error) {
	makeErr := func(errStr string) (*RootRecord, string, error) {
		return nil, "", fmt.Errorf("SaveSnapshot: %s", errStr)
	}

	latestReliableBranch, found := FindLatestReliableBranch(state, global.FractionHealthyBranch)
	if !found {
		return makeErr("the reliable branch has not been found: cannot proceed with snapshot")
	}

	header := SnapshotHeader{
		Description: "Proxima snapshot file",
		Version:     snapshotFormatVersionString,
	}

	headerBin, err := json.Marshal(&header)
	if err != nil {
		return makeErr(err.Error())
	}

	fname := snapshotFileName(latestReliableBranch.Stem.ID.TransactionID())
	tmpfname := "__tmp__" + fname

	outFileStream, err := common.CreateKVStreamFile(tmpfname)
	if err != nil {
		return makeErr(err.Error())
	}

	// write header with version
	err = outFileStream.Write(nil, headerBin)
	if err != nil {
		return makeErr(err.Error())
	}

	// write root record
	branchID := latestReliableBranch.Stem.ID.TransactionID()
	err = outFileStream.Write(branchID[:], latestReliableBranch.RootRecord.Bytes())
	if err != nil {
		return makeErr(err.Error())
	}

	// write ledger identity record
	err = outFileStream.Write(nil, LedgerIdentityBytesFromRoot(state, latestReliableBranch.Root))
	if err != nil {
		return makeErr(err.Error())
	}

	// write trie
	err = WriteState(state, outFileStream, latestReliableBranch.Root, ctx)
	if err != nil {
		return makeErr(err.Error())
	}

	err = outFileStream.Close()
	if err != nil {
		return makeErr(err.Error())
	}

	err = os.Rename(tmpfname, fname)
	if err != nil {
		return makeErr(err.Error())
	}
	return &latestReliableBranch.RootRecord, fname, nil
}

// OpenSnapshotFileStream reads first 3 records in the snapshot file and returns
// iterator of remaining key/value pairs
func OpenSnapshotFileStream(fname string) (
	*SnapshotHeader,
	*ledger.IdentityData,
	*ledger.TransactionID,
	*RootRecord,
	*common.BinaryStreamFileIterator,
	error) {
	iter, err := common.OpenKVStreamFile(fname)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	var header SnapshotHeader
	var id *ledger.IdentityData
	var rootRecord RootRecord
	var branchID ledger.TransactionID

	n := 0
	err1 := iter.Iterate(func(k []byte, v []byte) bool {
		switch n {
		case 0:
			if len(k) != 0 {
				err = fmt.Errorf("wrong first key/value pair")
				return false
			}
			if err = json.Unmarshal(v, &header); err != nil {
				return false
			}
		case 1:
			if branchID, err = ledger.TransactionIDFromBytes(k); err != nil {
				return false
			}
			if rootRecord, err = RootRecordFromBytes(v); err != nil {
				return false
			}
		case 2:
			if len(k) != 0 {
				err = fmt.Errorf("wrong second key/value pair")
				return false
			}
			if id, err = ledger.IdentityDataFromBytes(v); err != nil {
				return false
			}
			// stop reading
			return false
		}
		n++
		return true
	})
	if err1 != nil {
		return nil, nil, nil, nil, nil, err1
	}
	if n != 2 {
		return nil, nil, nil, nil, nil, fmt.Errorf("wrong snapshot file format")
	}
	return &header, id, &branchID, &rootRecord, iter, nil
}
