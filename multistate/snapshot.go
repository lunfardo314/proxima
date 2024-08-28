package multistate

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/unitrie/common"
)

// writeState writes state with the root as a sequence of key/value pairs.
// Does not write ledger identity record
func writeState(state global.StateStoreReader, target common.KVStreamWriter, root common.VCommitment, ctx context.Context, out io.Writer) error {
	rdr, err := NewReadable(state, root)
	if err != nil {
		return fmt.Errorf("writeState: %w", err)
	}
	counter := 0
	rdr.Iterator(nil).Iterate(func(k, v []byte) bool {
		select {
		case <-ctx.Done():
			err = fmt.Errorf("writeState: state writing has been interrupted")
		default:
			if len(k) > 0 {
				// skip ledger identity record
				err = target.Write(k, v)
				_outKVPair(k, v, counter, out)
				counter++
			}
		}
		return err == nil
	})
	return err
}

func _outKVPair(k, v []byte, counter int, out io.Writer) {
	common.Assert(len(k) > 0, "len(k)>0")

	_, _ = fmt.Fprintf(out, "[SaveSnapshot] rec #%d: %s %s, value len: %d\n",
		counter, PartitionToString(k[0]), hex.EncodeToString(k[1:]), len(v))

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
func SaveSnapshot(state global.StateStoreReader, ctx context.Context, dir string, out ...io.Writer) (*RootRecord, string, error) {
	makeErr := func(errStr string) (*RootRecord, string, error) {
		return nil, "", fmt.Errorf("SaveSnapshot: %s", errStr)
	}

	console := io.Discard
	if len(out) > 0 {
		console = out[0]
	}
	latestReliableBranch, found := FindLatestReliableBranch(state, global.FractionHealthyBranch)
	if !found {
		return makeErr("the reliable branch has not been found: cannot proceed with snapshot")
	}
	_, _ = fmt.Fprintf(console, "[SaveSnapshot] latest reliable branch: %s\n", latestReliableBranch.Stem.IDShort())

	fname := snapshotFileName(latestReliableBranch.Stem.ID.TransactionID())
	tmpfname := "__tmp__" + fname

	fpath := filepath.Join(dir, fname)
	fpathtmp := filepath.Join(dir, tmpfname)

	_, _ = fmt.Fprintf(console, "[SaveSnapshot] target file:  %s\n", fpath)
	_, _ = fmt.Fprintf(console, "[SaveSnapshot] tmp file:  %s\n", fpathtmp)

	header := SnapshotHeader{
		Description: "Proxima snapshot file",
		Version:     snapshotFormatVersionString,
	}

	headerBin, err := json.Marshal(&header)
	if err != nil {
		return makeErr(err.Error())
	}

	outFileStream, err := common.CreateKVStreamFile(filepath.Join(dir, fpathtmp))
	if err != nil {
		return makeErr(err.Error())
	}

	// write header with version
	err = outFileStream.Write(nil, headerBin)
	if err != nil {
		return makeErr(err.Error())
	}
	_, _ = fmt.Fprintf(console, "[SaveSnapshot] header: %s\n", string(headerBin))

	// write root record
	branchID := latestReliableBranch.Stem.ID.TransactionID()
	err = outFileStream.Write(branchID[:], latestReliableBranch.RootRecord.Bytes())
	if err != nil {
		return makeErr(err.Error())
	}
	_, _ = fmt.Fprintf(console, "[SaveSnapshot] root record:\n%s\n", latestReliableBranch.RootRecord.Lines("     ").String())

	// write ledger identity record
	ledgerIDBytes := LedgerIdentityBytesFromRoot(state, latestReliableBranch.Root)
	err = outFileStream.Write(nil, ledgerIDBytes)
	if err != nil {
		return makeErr(err.Error())
	}
	ledgerID, err := ledger.IdentityDataFromBytes(ledgerIDBytes)
	if err != nil {
		return makeErr(err.Error())
	}
	_, _ = fmt.Fprintf(console, "[SaveSnapshot] ledger ID:\n%s\n", ledgerID.Lines("     ").String())

	// write trie
	err = writeState(state, outFileStream, latestReliableBranch.Root, ctx, console)
	if err != nil {
		return makeErr(err.Error())
	}

	err = outFileStream.Close()
	if err != nil {
		return makeErr(err.Error())
	}

	err = os.Rename(fpathtmp, fpath)
	if err != nil {
		return makeErr(err.Error())
	}
	return &latestReliableBranch.RootRecord, fpath, nil
}

type SnapshotFileStream struct {
	Header     *SnapshotHeader
	LedgerID   *ledger.IdentityData
	BranchID   ledger.TransactionID
	RootRecord RootRecord
	InChan     chan common.KVPairOrError
	Close      func()
}

// OpenSnapshotFileStream reads first 3 records in the snapshot file and returns
// channel for remaining key/value pairs
func OpenSnapshotFileStream(fname string) (*SnapshotFileStream, error) {
	iter, err := common.OpenKVStreamFile(fname)
	if err != nil {
		return nil, err
	}
	ret := &SnapshotFileStream{}
	ctx, cancel := context.WithCancel(context.Background())
	ret.Close = cancel
	ret.InChan = common.KVStreamIteratorToChan(iter, ctx)

	// read header
	pair := <-ret.InChan
	if pair.IsNil() || pair.Err != nil {
		return nil, fmt.Errorf("OpenSnapshotFileStream: wrong first key/value pair 1")
	}
	if len(pair.Key) > 0 {
		cancel()
		return nil, fmt.Errorf("OpenSnapshotFileStream: wrong first key/value pair 2")
	}
	if err = json.Unmarshal(pair.Value, &ret.Header); err != nil {
		cancel()
		return nil, fmt.Errorf("OpenSnapshotFileStream: wrong first key/value pair 3")
	}
	// read root record
	pair = <-ret.InChan
	if pair.IsNil() || pair.Err != nil {
		return nil, fmt.Errorf("OpenSnapshotFileStream: wrong ssecond key/value pair 1")
	}
	if ret.BranchID, err = ledger.TransactionIDFromBytes(pair.Key); err != nil {
		cancel()
		return nil, fmt.Errorf("OpenSnapshotFileStream: wrong second key/value pair 2")
	}
	if ret.RootRecord, err = RootRecordFromBytes(pair.Value); err != nil {
		cancel()
		return nil, fmt.Errorf("OpenSnapshotFileStream: wrong second key/value pair 3")
	}
	// read ledger identity
	pair = <-ret.InChan
	if pair.IsNil() || pair.Err != nil {
		return nil, fmt.Errorf("OpenSnapshotFileStream: wrong third key/value pair 1")
	}
	ret.LedgerID, err = ledger.IdentityDataFromBytes(pair.Value)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("OpenSnapshotFileStream: wrong third key/value pair 2")
	}
	return ret, nil
}
