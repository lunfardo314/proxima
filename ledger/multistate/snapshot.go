package multistate

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/lunfardo314/proxima/global"
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
		Header           *SnapshotHeader
		UpgradeLibraries []UpgradeLibraryEntry
		BranchID         base.TransactionID
		RootRecord       RootRecord
		InChan           chan common.KVPairOrError
		Close            func()
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
	snapshotFormatVersionString = "ver 1"
	TmpSnapshotFileNamePrefix   = "__tmp__"
)

// UpgradeLibraryEntry represents a single upgrade library in a snapshot
type UpgradeLibraryEntry struct {
	Slot        uint32
	LibraryYAML []byte
}

// writeState writes state with the root as a sequence of key/value pairs.
// Does not write ledger identity record
func writeState(state global.StoreReader, target common.KVStreamWriter, root common.VCommitment, ctx context.Context, out io.Writer) (*SnapshotStats, error) {
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
func SaveSnapshot(state global.StoreReader, branch *BranchData, ctx context.Context, dir string, out ...io.Writer) (string, *SnapshotStats, error) {
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

	// write upgrade libraries from DB partition (before trie data for early access during restore)
	var upgradeLibraries []UpgradeLibraryEntry
	IterateUpgradeLibraries(state, func(slot uint32, yaml []byte) bool {
		upgradeLibraries = append(upgradeLibraries, UpgradeLibraryEntry{Slot: slot, LibraryYAML: yaml})
		return true
	})

	// write upgrade count (big-endian 4 bytes)
	countBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(countBytes, uint32(len(upgradeLibraries)))
	err = outFileStream.Write([]byte{upgradeLibraryDBPartition}, countBytes)
	if err != nil {
		return makeErr(err.Error())
	}
	_, _ = fmt.Fprintf(console, "[SaveSnapshot] upgrade libraries: %d\n", len(upgradeLibraries))

	// write each upgrade library
	for _, entry := range upgradeLibraries {
		slotBytes := base.Slot2Bytes(entry.Slot)
		err = outFileStream.Write(slotBytes, entry.LibraryYAML)
		if err != nil {
			return makeErr(err.Error())
		}
		_, _ = fmt.Fprintf(console, "[SaveSnapshot]   - slot %d: %d bytes\n", entry.Slot, len(entry.LibraryYAML))
	}

	// write trie data (after upgrade libraries)
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

// OpenSnapshotFileStream reads snapshot file header, identity, and upgrade libraries.
// Returns a stream for trie data key/value pairs.
// Format (ver 1): header, root record, identity, upgrade count, upgrade libraries, trie data
func OpenSnapshotFileStream(fname string) (*SnapshotFileStream, error) {
	file, err := os.Open(fname)
	if err != nil {
		return nil, err
	}
	iter := common.BinaryStreamIteratorFromFile(file)
	ret := &SnapshotFileStream{}
	ctx, cancel := context.WithCancel(context.Background())
	ret.Close = cancel

	rawChan := common.KVStreamIteratorToChan(iter, ctx)

	// read header
	pair := <-rawChan
	if pair.IsNil() || pair.Err != nil {
		cancel()
		return nil, fmt.Errorf("OpenSnapshotFileStream: wrong header record")
	}
	if len(pair.Key) > 0 {
		cancel()
		return nil, fmt.Errorf("OpenSnapshotFileStream: header key must be empty")
	}
	if err = json.Unmarshal(pair.Value, &ret.Header); err != nil {
		cancel()
		return nil, fmt.Errorf("OpenSnapshotFileStream: invalid header JSON: %v", err)
	}

	// read root record
	pair = <-rawChan
	if pair.IsNil() || pair.Err != nil {
		cancel()
		return nil, fmt.Errorf("OpenSnapshotFileStream: wrong root record")
	}
	if ret.BranchID, err = base.TransactionIDFromBytes(pair.Key); err != nil {
		cancel()
		return nil, fmt.Errorf("OpenSnapshotFileStream: invalid branch ID: %v", err)
	}
	if ret.RootRecord, err = RootRecordFromBytes(pair.Value); err != nil {
		cancel()
		return nil, fmt.Errorf("OpenSnapshotFileStream: invalid root record: %v", err)
	}

	// read upgrade count marker
	pair = <-rawChan
	if pair.IsNil() || pair.Err != nil {
		cancel()
		return nil, fmt.Errorf("OpenSnapshotFileStream: wrong upgrade count record")
	}
	if len(pair.Key) != 1 || pair.Key[0] != upgradeLibraryDBPartition {
		cancel()
		return nil, fmt.Errorf("OpenSnapshotFileStream: expected upgrade count marker, got key len %d", len(pair.Key))
	}
	if len(pair.Value) < 4 {
		cancel()
		return nil, fmt.Errorf("OpenSnapshotFileStream: invalid upgrade count value")
	}
	upgradeCount := int(binary.BigEndian.Uint32(pair.Value))

	// read upgrade libraries
	for i := 0; i < upgradeCount; i++ {
		pair = <-rawChan
		if pair.IsNil() || pair.Err != nil {
			cancel()
			return nil, fmt.Errorf("OpenSnapshotFileStream: failed to read upgrade library %d", i)
		}
		slot, slotErr := base.SlotFromBytes(pair.Key)
		if slotErr != nil {
			cancel()
			return nil, fmt.Errorf("OpenSnapshotFileStream: invalid upgrade slot: %v", slotErr)
		}
		ret.UpgradeLibraries = append(ret.UpgradeLibraries, UpgradeLibraryEntry{
			Slot:        slot,
			LibraryYAML: pair.Value,
		})
	}

	// remaining records are trie data - pass through channel
	ret.InChan = rawChan
	return ret, nil
}

// GetLedgerConstants parses constants from the first upgrade library (slot 0).
// This is a convenience method for code that needs constants during restore.
func (s *SnapshotFileStream) GetLedgerConstants() (*ledger.Constants, error) {
	if len(s.UpgradeLibraries) == 0 {
		return nil, fmt.Errorf("no upgrade libraries in snapshot")
	}
	// Find slot 0 library
	for _, entry := range s.UpgradeLibraries {
		if entry.Slot == 0 {
			lib, err := ledger.ParseLibraryFromYAML(entry.LibraryYAML, ledger.GetEmbeddedFunctionResolver)
			if err != nil {
				return nil, fmt.Errorf("failed to parse library: %v", err)
			}
			var constants *ledger.Constants
			err = util.CatchPanicOrError(func() error {
				constants = ledger.ConstantsFromLibrary(lib)
				return nil
			})
			if err != nil {
				return nil, err
			}
			return constants, nil
		}
	}
	return nil, fmt.Errorf("no slot 0 library in snapshot")
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
