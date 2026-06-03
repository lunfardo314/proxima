# Proxima Snapshot Format

## Overview

Snapshots capture the complete ledger state at a specific branch, enabling:
- **State recovery**: Restore node from snapshot when DB is missing or corrupted
- **State cleanup**: Periodic DB compaction by restoring from recent snapshot
- **Network bootstrap**: New nodes can start from a snapshot instead of syncing from genesis
- **Genesis distribution**: Genesis state is distributed as a snapshot file

## File Format (Version 2)

Snapshots are binary files using the `unitrie` KV stream format. Each record is a key-value pair.

```
┌─────────────────────────────────────────────────────────────┐
│ Record 1: Header                                            │
│   Key:   empty (nil)                                        │
│   Value: JSON {"description":"...","version":"ver 2"}       │
├─────────────────────────────────────────────────────────────┤
│ Record 2: Root Record                                       │
│   Key:   branchID (32 bytes)                                │
│   Value: RootRecord bytes (root + sequencer ID)             │
├─────────────────────────────────────────────────────────────┤
│ Record 3: Upgrade Count                                     │
│   Key:   {0x06} (upgrade-library DB-partition marker)       │
│   Value: 4-byte big-endian count of upgrade libraries       │
├─────────────────────────────────────────────────────────────┤
│ Records 4..N: Upgrade Libraries                             │
│   Key:   4-byte big-endian slot number                      │
│   Value: Full compiled library JSON bytes                   │
├─────────────────────────────────────────────────────────────┤
│ Records N+1..end: Trie Data                                 │
│   Key:   partition byte + data (UTXO/tx, controllers, chain)│
│   Value: Corresponding state data                           │
└─────────────────────────────────────────────────────────────┘
```

Note: The ledger identity (genesis time and description) is stored within the slot-0
library JSON, not as a separate record. (The trie also holds an empty-key identity record,
but that one is intentionally skipped when writing the snapshot.)

## File Naming

Snapshot files are named after the branch ID in its **dashed** form (via `OutputID`/
`TransactionID.AsFileName`).

| Type | Pattern | Notes |
|------|---------|-------|
| Normal snapshot | `<branchID>.snapshot` | `branchID` in dashed form |
| Temporary file | `__tmp__<branchID>.snapshot` | Written during save, renamed on completion |
| Genesis snapshot | `<genesis branchID>.snapshot` | Same scheme; the genesis (slot-0) branch ID |

There is no fixed `genesis.snapshot` filename — the genesis snapshot is named after its
slot-0 branch ID like any other.

## Save Workflow

The `SaveSnapshot` function in `ledger/multistate/snapshot.go`:

```
1. Create temporary file: __tmp__<branchID>.snapshot
2. Write header (JSON with version "ver 2")
3. Write root record (branch ID -> RootRecord)
4. Iterate upgrade DB partition -> write count + library JSON blobs
5. Iterate trie state -> write all KV pairs
6. Close and rename: __tmp__... -> <branchID>.snapshot
```

## Restore Workflow

The `RestoreFromSnapshot` function in `core/core_modules/snapshot_restore/restore.go`:

```
1. Open snapshot file stream
2. Read header, root record, upgrade libraries (synchronous)
3. Validate snapshot (check ledger hash matches)
4. Delete existing DB (if present)
5. Create fresh DB with restore-in-progress marker
6. Write all upgrade libraries to upgrade DB partition
7. Create empty trie root with ledger identity (from slot-0 library JSON)
8. Stream trie data -> batch insert into trie
9. Write metadata (latest/earliest slot, root record)
10. Clear restore-in-progress marker
11. Verify final root matches expected root
```

## Key Data Structures

### SnapshotFileStream

Returned by `OpenSnapshotFileStream` (`ledger/multistate/snapshot.go`):

```go
type SnapshotFileStream struct {
    Header           *SnapshotHeader          // Version info
    UpgradeLibraries []UpgradeLibraryEntry    // All upgrade libraries (includes identity in slot 0)
    BranchID         base.TransactionID       // Branch this snapshot represents
    RootRecord       RootRecord               // State metadata (root + sequencer ID)
    InChan           chan common.KVPairOrError // Trie data stream
    Close            func()                   // Cleanup function
}
```

### UpgradeLibraryEntry

```go
type UpgradeLibraryEntry struct {
    Slot        uint32  // Upgrade slot (0 = genesis)
    LibraryJSON []byte  // Full compiled library JSON
}
```

### RootRecord

After the metadata refactor, the per-branch DB record carries only the trie root and the
sequencer chain ID (`ledger/multistate/state.go`):

```go
type RootRecord struct {
    Root        common.VCommitment  // Merkle root of state trie
    SequencerID base.ChainID        // Sequencer that produced the branch
}
```

Every other deterministic aggregate — supply, coverage, slot inflation, frozen coverage,
confirmed-transaction count, baseline root — now lives inside the **stem output's
`stemLock` / stem data** and is therefore part of the trie commitment. These are projected
out of the stem output into the in-memory `BranchData` struct when a branch is read (in
`FetchBranchDataByRoot`), so callers still see `br.Supply`, `br.CoverageDelta`,
`br.NumConfirmedTransactions`, `br.NumSeqTransactions`, `br.NumSeq`, etc.

## CLI Commands

| Command | Description |
|---------|-------------|
| `proxi snapshot info [file]` | Display snapshot metadata and statistics |
| `proxi snapshot check [file]` | Check the snapshot's branch is part of the node's latest reliable branch (LRB) |
| `proxi snapshot restore [<batch size>] -s <file>` | Restore DB from snapshot (`-b` sets batch size) |
| `proxi snapshot db [<slots back>]` | Write a state snapshot to file (default 10 slots back from the LRB) |

Note: ledger-compatibility (the ledger-hash match used during restore) is checked by
`ValidateSnapshot`; the `check` command performs the LRB-membership check against a running
node.

### Example: Viewing Snapshot Info

```bash
$ proxi snapshot info mystate.snapshot

snapshot file: mystate.snapshot
format version: ver 2
branch id: <dashed branch id> (hex = ...)
root record:
    sequencer id: ...
    root: ...
upgrade libraries: 1
  - slot 0: 45678 bytes
ledger constants (from slot 0 library):
    GenesisTimeUnix: 1234567890
    Description: "proxima testnet"
    ...
```

## Automatic Restore on Startup

The node automatically restores from snapshot on startup if the DB is missing or corrupted,
**regardless of whether `snapshot_restore` is enabled**. The snapshot is always searched in
the single canonical `snapshot.directory` (default: current working directory).

Restore triggers:

1. **DB is missing**: Fresh node, or DB was deleted
2. **DB is corrupted**: Cannot be opened by BadgerDB
3. **Restore interrupted**: Restore-in-progress marker present from a previous incomplete restore
4. **Cleanup in progress**: State file indicates periodic cleanup was triggered

If no snapshot is found in `snapshot.directory`, the node **refuses to start**.

### Configuration

```yaml
snapshot:
  directory: ""            # Single location for snapshots. Default "" = current working directory.

snapshot_restore:
  enable: true             # Enable periodic cleanup (restore on missing/corrupted DB always works)
  period_slots: 8438       # ~24 hours between cleanups
  window_slots: 1406       # ~4 hour random window
  ttl_minutes: 10          # Max time for cleanup operation
```

### Restore Process

```
node.Start()
  └─> CheckAndRestoreOnStartup()
        ├─> Check if DB missing/corrupted
        ├─> Find latest .snapshot in snapshot.directory
        │     └─> No snapshot found → REFUSE TO START
        ├─> Validate snapshot matches ledger config
        ├─> Delete old/corrupted DB
        ├─> RestoreFromSnapshot()
        └─> Continue normal startup
```

## Trie Partitions

State data committed to the trie is organized by a 1-byte partition prefix
(`ledger/multistate/state.go`):

| Partition Byte | Name | Content | Key Format |
|----------------|------|---------|------------|
| 0x00 | `TriePartitionLedgerState` ("UTXO") | Transactions and UTXOs | TxID (32 bytes) or OutputID (33 bytes) — distinguished by key length |
| 0x01 | `TriePartitionControllers` ("ACCN") | Account / controller index | controller (holder ID / chain ID) bytes |
| 0x02 | `TriePartitionChainID` ("CHID") | Chain output index | Chain ID bytes |

Upgrade libraries are **not** a trie partition. They are stored in a separate DB partition
(marker byte `0x06`), and in a snapshot they appear as the upgrade-count + library records
written *before* the trie data (see the file format above).

## Error Handling

### Restore-in-Progress Marker

- Written to DB at start of restore
- Cleared only on successful completion
- If present on startup, DB is considered corrupted and deleted

### Root Verification

After restoring all trie data, the final computed root must match the expected root from the
snapshot's RootRecord. Mismatch indicates data corruption.

### Atomic Writes

Snapshots are written to temporary files (`__tmp__` prefix) and renamed only after successful
completion. This ensures partial/corrupted snapshots are never used.

## Upgrade Libraries in Snapshots

Snapshots include all upgrade libraries from the DB partition. This ensures:

1. **Self-contained**: Snapshot has everything needed to restore state
2. **Version preservation**: Historical library versions preserved for transaction validation
3. **Early access**: Libraries loaded before trie data for validation during restore

Libraries are stored in slot order, with slot 0 always present (genesis library). A "ver 1"
snapshot (YAML libraries) is rejected by a current node with an explicit version-mismatch
error.

## Version History

| Version | Changes |
|---------|---------|
| ver 0 | Initial format with the full library as a single "ledger identity" blob |
| ver 1 | Separate upgrade-libraries section (YAML); ledger identity embedded in slot 0 library; big-endian serialization for upgrade count |
| ver 2 | Upgrade-library blobs switched from YAML to JSON (easyfl JSON serialization). Loading a "ver 1" snapshot now fails fast with a version-mismatch error |
```
