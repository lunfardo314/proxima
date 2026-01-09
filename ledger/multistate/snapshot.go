package multistate

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/unitrie/common"
)

type (
	SnapshotHeader struct {
		Description string `json:"description"`
		Version     string `json:"version"`
	}

	SnapshotFileStream struct {
		Header          *SnapshotHeader
		LedgerIDData    []byte
		LedgerConstants *ledger.Constants
		BranchID        base.TransactionID
		RootRecord      RootRecord
		InChan          chan common.KVPairOrError
		Close           func()
	}

	SnapshotStats struct {
		NumUTXO          int
		NumTx            int
		NumOtherState    int
		NumChainID       int
		NumAccounts      int
		DurationTraverse time.Duration
	}
)

const (
	snapshotFormatVersionString = "ver 0"
	TmpSnapshotFileNamePrefix   = "__tmp__"
)

// writeState writes state with the root as a sequence of key/value pairs.
// Does not write ledger identity record
func writeState(state StateStoreReader, target common.KVStreamWriter, root common.VCommitment, ctx context.Context, out io.Writer) (*SnapshotStats, error) {
	rdr, err := NewReadable(state, root)
	if err != nil {
		return nil, fmt.Errorf("writeState: %w", err)
	}
	counter := 0
	stats := &SnapshotStats{}
	start := time.Now()
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

				switch k[0] {
				case TriePartitionLedgerState:
					if len(k[1:]) == base.TransactionIDLength {
						stats.NumTx++
					} else if len(k[1:]) == base.OutputIDLength {
						stats.NumUTXO++
					} else {
						stats.NumOtherState++
					}
				case TriePartitionAccounts:
					stats.NumAccounts++
				case TriePartitionChainID:
					stats.NumChainID++
				}
			}
		}
		return err == nil
	})
	if err != nil {
		return nil, err
	}
	stats.DurationTraverse = time.Since(start)
	return stats, nil
}

func _outKVPair(k, v []byte, counter int, out io.Writer) {
	util.Assertf(len(k) > 0, "len(k)>0")

	_, _ = fmt.Fprintf(out, "[SaveSnapshot] rec #%d: %s %s, value len: %d\n",
		counter, PartitionToString(k[0]), hex.EncodeToString(k[1:]), len(v))

}

func snapshotFileName(branchID base.TransactionID) string {
	return branchID.AsFileName() + ".snapshot"
}

// SaveSnapshot writes latest reliable state into snapshot. Returns snapshot file name
func SaveSnapshot(state StateStoreReader, branch *BranchData, ctx context.Context, dir string, out ...io.Writer) (string, *SnapshotStats, error) {
	makeErr := func(errStr string) (string, *SnapshotStats, error) {
		return "", nil, fmt.Errorf("SaveSnapshot: %s", errStr)
	}

	console := io.Discard
	if len(out) > 0 {
		console = out[0]
	}
	_, _ = fmt.Fprintf(console, "[SaveSnapshot] latest reliable branch: %s\n", branch.Stem.IDShort())

	fname := snapshotFileName(branch.Stem.ID.TransactionID())
	tmpfname := TmpSnapshotFileNamePrefix + fname

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

	file, err := os.Create(fpathtmp)
	if err != nil {
		return makeErr(err.Error())
	}

	outFileStream := common.BinaryStreamWriterFromFile(file)

	// write header with version
	err = outFileStream.Write(nil, headerBin)
	if err != nil {
		return makeErr(err.Error())
	}
	_, _ = fmt.Fprintf(console, "[SaveSnapshot] header: %s\n", string(headerBin))

	// write root record
	branchID := branch.Stem.ID.TransactionID()
	err = outFileStream.Write(branchID[:], branch.RootRecord.Bytes())
	if err != nil {
		return makeErr(err.Error())
	}
	_, _ = fmt.Fprintf(console, "[SaveSnapshot] root record:\n%s\n", branch.RootRecord.Lines("     ").String())

	// write ledger identity data as separate record
	ledgerIDBytes := LedgerIdentityBytesFromRoot(state, branch.Root)
	err = outFileStream.Write(nil, ledgerIDBytes)
	if err != nil {
		return makeErr(err.Error())
	}

	// parse ledger ID data to validate and to print params (not needed?)
	lib, err := ledger.ParseLibraryFromYAML(ledgerIDBytes, ledger.GetEmbeddedFunctionResolverUpgrade0)
	if err != nil {
		return makeErr(err.Error())
	}
	var constants *ledger.Constants
	err = util.CatchPanicOrError(func() error {
		constants = ledger.ConstantsFromLibrary(lib)
		return nil
	})
	if err != nil {
		return makeErr(err.Error())
	}
	_, _ = fmt.Fprintf(console, "[SaveSnapshot] ledger constants:\n%s\n", constants.Lines("     ").String())

	// write trie
	var stats *SnapshotStats
	stats, err = writeState(state, outFileStream, branch.Root, ctx, console)
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
	return fpath, stats, nil
}

// OpenSnapshotFileStream reads first 3 records in the snapshot file and returns
// channel for remaining key/value pairs
func OpenSnapshotFileStream(fname string) (*SnapshotFileStream, error) {
	file, err := os.Open(fname)
	if err != nil {
		return nil, err
	}
	iter := common.BinaryStreamIteratorFromFile(file)
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
		return nil, fmt.Errorf("OpenSnapshotFileStream: wrong second key/value pair 1")
	}
	if ret.BranchID, err = base.TransactionIDFromBytes(pair.Key); err != nil {
		cancel()
		return nil, fmt.Errorf("OpenSnapshotFileStream: wrong second key/value pair 2")
	}
	if ret.RootRecord, err = RootRecordFromBytes(pair.Value); err != nil {
		cancel()
		return nil, fmt.Errorf("OpenSnapshotFileStream: wrong second key/value pair 3")
	}
	// read ledger identity data
	pair = <-ret.InChan
	if pair.IsNil() || pair.Err != nil {
		return nil, fmt.Errorf("OpenSnapshotFileStream: wrong third key/value pair 1")
	}
	lib, err := ledger.ParseLibraryFromYAML(pair.Value, ledger.GetEmbeddedFunctionResolverUpgrade0)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("OpenSnapshotFileStream: wrong third key/value pair 2")
	}
	var constants *ledger.Constants
	err = util.CatchPanicOrError(func() error {
		constants = ledger.ConstantsFromLibrary(lib)
		return nil
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("OpenSnapshotFileStream: %v", err)
	}

	ret.LedgerConstants = constants
	ret.LedgerIDData = pair.Value
	return ret, nil
}

func (s *SnapshotStats) Lines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	ret.Add("Traversed state in %v", s.DurationTraverse)
	ret.Add("UTXOs:         %d", s.NumUTXO)
	ret.Add("Transactions:  %d", s.NumTx)
	ret.Add("Other state:   %d", s.NumOtherState)
	ret.Add("Chains:        %d", s.NumChainID)
	ret.Add("Accounts:      %d", s.NumAccounts)
	ret.Add("Total records: %d", s.NumUTXO+s.NumTx+s.NumOtherState+s.NumChainID+s.NumAccounts)
	return ret
}
