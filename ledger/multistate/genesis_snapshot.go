package multistate

// This file implements in-memory genesis state building and snapshot creation.
// It allows creating a genesis snapshot without requiring BadgerDB.

import (
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/unitrie/common"
)

// GenesisSnapshotData contains all data needed to write a genesis snapshot.
type GenesisSnapshotData struct {
	// Identity contains minimal ledger identity (genesis time + description)
	Identity *ledger.LedgerIdentity
	// LibraryYAML is the compiled library YAML for slot 0
	LibraryYAML []byte
	// Constants contains parsed ledger constants
	Constants *ledger.Constants
	// BranchID is the genesis transaction ID
	BranchID base.TransactionID
	// RootRecord contains the genesis root record
	RootRecord RootRecord
	// Store contains all state data (trie + upgrade library)
	Store *common.InMemoryKVStore
	// BootstrapChainID is the chain ID of the initial supply output
	BootstrapChainID base.ChainID
}

// BuildGenesisSnapshotData creates genesis state data in memory.
// This function prepares all data needed for a genesis snapshot without using BadgerDB.
//
// Parameters:
//   - privateKey: the genesis controller's ed25519 private key
//   - genesisTimeUnix: Unix timestamp for genesis
//   - description: optional ledger description (empty string uses default)
//
// Returns the genesis snapshot data that can be written to a file.
func BuildGenesisSnapshotData(privateKey ed25519.PrivateKey, genesisTimeUnix uint32, description string) (*GenesisSnapshotData, error) {
	// Validate private key
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid private key size: expected %d, got %d", ed25519.PrivateKeySize, len(privateKey))
	}

	// Create ledger parameters from private key
	var params ledger.InitParameters
	if description != "" {
		params = ledger.DefaultParameters(privateKey, genesisTimeUnix, description)
	} else {
		params = ledger.DefaultParameters(privateKey, genesisTimeUnix)
	}

	// Generate library YAML
	libraryYAML := ledger.LibraryYAMLFromParameters(params, true)

	// Parse library to get constants and validate
	lib, err := ledger.ParseLibraryFromYAML(libraryYAML, ledger.GetEmbeddedFunctionResolverUpgrade0)
	if err != nil {
		return nil, fmt.Errorf("failed to parse library: %w", err)
	}
	constants := ledger.ConstantsFromLibrary(lib)

	// Create in-memory store
	store := common.NewInMemoryKVStore()

	// Store library in upgrade DB partition at slot 0
	if err := WriteUpgradeLibrary(store, 0, libraryYAML); err != nil {
		return nil, fmt.Errorf("failed to write upgrade library: %w", err)
	}

	// Initialize the library cache with the in-memory store.
	// This is required because GenesisOutput internally uses L() for constraint compilation.
	ledger.RegisterResolverForUpgrade(0, ledger.GetEmbeddedFunctionResolverUpgrade0)
	ledger.MustInitLibraryCache(store)

	// Create minimal identity
	identity := ledger.NewLedgerIdentity(genesisTimeUnix, params.Description)

	// Create empty trie root with identity
	emptyRoot, err := WriteEmptyRootWithLedgerIdentity(identity, store)
	if err != nil {
		return nil, fmt.Errorf("failed to create empty root: %w", err)
	}

	// Create genesis outputs using constants from the parsed library
	genesisAddr := ledger.AddressED25519FromPublicKey(constants.GenesisControllerPublicKey)
	initialSupply := constants.InitialSupply

	gout := ledger.GenesisOutput(initialSupply, genesisAddr)
	gStemOut := ledger.GenesisStemOutput()

	// Create upgrade commitment UTXO for slot 0
	// For slot 0, prevHash is the base library hash, prevSlot is MaxSlot
	libraryHash := lib.LibraryHash()
	prevLibraryHash := ledger.BaseLibraryHash()
	upgradeOut := ledger.UpgradeUTXO(0, libraryHash, prevLibraryHash, base.MaxSlot)

	// Create updatable state and apply mutations
	updatable := MustNewUpdatable(store, emptyRoot)
	mutations := genesisUpdateMutations(&gout.OutputWithID, gStemOut, upgradeOut)

	rootParams := &RootRecordParams{
		StemOutputID:      gStemOut.ID,
		SeqID:             gout.ChainID,
		CoverageDelta:     initialSupply,
		SlotInflation:     initialSupply,
		Supply:            initialSupply,
		WriteEarliestSlot: true,
	}
	updatable.MustUpdate(mutations, rootParams)

	// Create root record
	rootRecord := RootRecord{
		Root:          updatable.Root(),
		SequencerID:   gout.ChainID,
		CoverageDelta: initialSupply,
		SlotInflation: initialSupply,
		Supply:        initialSupply,
	}

	return &GenesisSnapshotData{
		Identity:         identity,
		LibraryYAML:      libraryYAML,
		Constants:        constants,
		BranchID:         gStemOut.ID.TransactionID(),
		RootRecord:       rootRecord,
		Store:            store,
		BootstrapChainID: gout.ChainID,
	}, nil
}

// WriteGenesisSnapshot writes genesis state to a snapshot file.
// Returns the full path to the created snapshot file.
func WriteGenesisSnapshot(data *GenesisSnapshotData, dir string, out ...io.Writer) (string, error) {
	console := io.Discard
	if len(out) > 0 {
		console = out[0]
	}

	fname := snapshotFileName(data.BranchID)
	fpath := filepath.Join(dir, fname)

	_, _ = fmt.Fprintf(console, "[WriteGenesisSnapshot] target file: %s\n", fpath)

	// Create file
	file, err := os.Create(fpath)
	if err != nil {
		return "", fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	outFileStream := common.BinaryStreamWriterFromFile(file)

	// Write header
	header := SnapshotHeader{
		Description: "Proxima snapshot file",
		Version:     snapshotFormatVersionString,
	}
	headerBin, err := json.Marshal(&header)
	if err != nil {
		return "", fmt.Errorf("failed to marshal header: %w", err)
	}
	if err := outFileStream.Write(nil, headerBin); err != nil {
		return "", fmt.Errorf("failed to write header: %w", err)
	}
	_, _ = fmt.Fprintf(console, "[WriteGenesisSnapshot] header: %s\n", string(headerBin))

	// Write root record
	if err := outFileStream.Write(data.BranchID[:], data.RootRecord.Bytes()); err != nil {
		return "", fmt.Errorf("failed to write root record: %w", err)
	}
	_, _ = fmt.Fprintf(console, "[WriteGenesisSnapshot] root record written\n")

	// Collect upgrade libraries (should be just slot 0 for genesis)
	var upgradeLibraries []UpgradeLibraryEntry
	IterateUpgradeLibraries(data.Store, func(slot uint32, yaml []byte) bool {
		upgradeLibraries = append(upgradeLibraries, UpgradeLibraryEntry{Slot: slot, LibraryYAML: yaml})
		return true
	})

	// Write upgrade count (big-endian 4 bytes)
	countBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(countBytes, uint32(len(upgradeLibraries)))
	if err := outFileStream.Write([]byte{upgradeLibraryDBPartition}, countBytes); err != nil {
		return "", fmt.Errorf("failed to write upgrade count: %w", err)
	}
	_, _ = fmt.Fprintf(console, "[WriteGenesisSnapshot] upgrade libraries: %d\n", len(upgradeLibraries))

	// Write each upgrade library
	for _, entry := range upgradeLibraries {
		slotBytes := base.Slot2Bytes(entry.Slot)
		if err := outFileStream.Write(slotBytes, entry.LibraryYAML); err != nil {
			return "", fmt.Errorf("failed to write upgrade library: %w", err)
		}
		_, _ = fmt.Fprintf(console, "[WriteGenesisSnapshot]   - slot %d: %d bytes\n", entry.Slot, len(entry.LibraryYAML))
	}

	// Write trie data
	stats, err := writeGenesisTrieData(data.Store, data.RootRecord.Root, outFileStream, console)
	if err != nil {
		return "", fmt.Errorf("failed to write trie data: %w", err)
	}

	if err := outFileStream.Close(); err != nil {
		return "", fmt.Errorf("failed to close file: %w", err)
	}

	_, _ = fmt.Fprintf(console, "[WriteGenesisSnapshot] complete: %d UTXOs, %d transactions\n",
		stats.NumUTXO, stats.NumTx)

	return fpath, nil
}

// writeGenesisTrieData writes trie state data to the snapshot stream.
func writeGenesisTrieData(store *common.InMemoryKVStore, root common.VCommitment, target common.KVStreamWriter, out io.Writer) (*SnapshotStats, error) {
	rdr, err := NewReadable(store, root)
	if err != nil {
		return nil, fmt.Errorf("writeGenesisTrieData: %w", err)
	}

	stats := &SnapshotStats{}
	counter := 0
	ctx := context.Background()

	rdr.Iterator(nil).Iterate(func(k, v []byte) bool {
		select {
		case <-ctx.Done():
			err = fmt.Errorf("writeGenesisTrieData: interrupted")
		default:
			if len(k) > 0 {
				// skip ledger identity record (nil key)
				err = target.Write(k, v)
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

	_, _ = fmt.Fprintf(out, "[writeGenesisTrieData] wrote %d records\n", counter)
	return stats, nil
}

// CreateGenesisSnapshot is a convenience function that builds genesis state and writes it to a snapshot file.
func CreateGenesisSnapshot(privateKey ed25519.PrivateKey, genesisTimeUnix uint32, description string, dir string, out ...io.Writer) (string, *GenesisSnapshotData, error) {
	data, err := BuildGenesisSnapshotData(privateKey, genesisTimeUnix, description)
	if err != nil {
		return "", nil, err
	}

	fpath, err := WriteGenesisSnapshot(data, dir, out...)
	if err != nil {
		return "", nil, err
	}

	return fpath, data, nil
}

// GetConstants returns the ledger constants from the genesis snapshot data.
func (d *GenesisSnapshotData) GetConstants() *ledger.Constants {
	return d.Constants
}

// GetLibraryHash returns the hash of the genesis library.
func (d *GenesisSnapshotData) GetLibraryHash() ([32]byte, error) {
	lib, err := ledger.ParseLibraryFromYAML(d.LibraryYAML, ledger.GetEmbeddedFunctionResolverUpgrade0)
	if err != nil {
		return [32]byte{}, err
	}
	return lib.LibraryHash(), nil
}

// VerifyGenesisSnapshot validates a genesis snapshot file.
// Returns the snapshot data if valid, or an error if invalid.
func VerifyGenesisSnapshot(fname string) (*SnapshotFileStream, error) {
	stream, err := OpenSnapshotFileStream(fname)
	if err != nil {
		return nil, fmt.Errorf("failed to open snapshot: %w", err)
	}

	// Verify it's version 1
	if stream.Header.Version != snapshotFormatVersionString {
		stream.Close()
		return nil, fmt.Errorf("unexpected snapshot version: %s", stream.Header.Version)
	}

	// Verify we have at least one upgrade library (slot 0)
	if len(stream.UpgradeLibraries) == 0 {
		stream.Close()
		return nil, fmt.Errorf("no upgrade libraries in snapshot")
	}

	// Verify slot 0 library exists
	hasSlot0 := false
	for _, entry := range stream.UpgradeLibraries {
		if entry.Slot == 0 {
			hasSlot0 = true
			break
		}
	}
	if !hasSlot0 {
		stream.Close()
		return nil, fmt.Errorf("missing slot 0 library in genesis snapshot")
	}

	// Verify we can parse constants from the library
	_, err = stream.GetLedgerConstants()
	if err != nil {
		stream.Close()
		return nil, fmt.Errorf("failed to parse constants: %w", err)
	}

	// Verify genesis transaction ID
	if stream.BranchID.Slot() != 0 {
		stream.Close()
		return nil, fmt.Errorf("genesis snapshot branch ID should be at slot 0, got slot %d", stream.BranchID.Slot())
	}

	return stream, nil
}
