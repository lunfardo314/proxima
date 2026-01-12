# Proxima Snapshot Format

## Overview

Snapshots capture the complete ledger state at a specific branch, enabling:
- **State recovery**: Restore node from snapshot when DB is missing or corrupted
- **State cleanup**: Periodic DB compaction by restoring from recent snapshot
- **Network bootstrap**: New nodes can start from a snapshot instead of syncing from genesis
- **Genesis distribution**: Genesis state is distributed as a snapshot file

## File Format (Version 1)

Snapshots are binary files using the `unitrie` KV stream format. Each record is a key-value pair.

```
┌─────────────────────────────────────────────────────────────┐
│ Record 1: Header                                            │
│   Key:   empty (nil)                                        │
│   Value: JSON {"description":"...","version":"ver 1"}       │
├─────────────────────────────────────────────────────────────┤
│ Record 2: Root Record                                       │
│   Key:   branchID (32 bytes)                                │
│   Value: RootRecord bytes (sequencer, root, coverage, etc.) │
├─────────────────────────────────────────────────────────────┤
│ Record 3: Ledger Identity                                   │
│   Key:   empty (nil)                                        │
│   Value: LedgerIdentity bytes (genesis time + description)  │
├─────────────────────────────────────────────────────────────┤
│ Record 4: Upgrade Count                                     │
│   Key:   {0x07} (upgrade partition marker)                  │
│   Value: 4-byte little-endian count of upgrade libraries    │
├─────────────────────────────────────────────────────────────┤
│ Records 5..N: Upgrade Libraries                             │
│   Key:   4-byte big-endian slot number                      │
│   Value: Full compiled library YAML bytes                   │
├─────────────────────────────────────────────────────────────┤
│ Records N+1..end: Trie Data                                 │
│   Key:   partition byte + data (UTXO, tx, account, chain)   │
│   Value: Corresponding state data                           │
└─────────────────────────────────────────────────────────────┘
```

## File Naming

| Type | Pattern | Example |
|------|---------|---------|
| Normal snapshot | `<branchID>.snapshot` | `00000123...abc.snapshot` |
| Temporary file | `__tmp__<branchID>.snapshot` | During write, renamed on completion |
| Genesis snapshot | `genesis.snapshot` | Special case for network bootstrap |

## Save Workflow

The `SaveSnapshot` function in `ledger/multistate/snapshot.go`:

```
1. Create temporary file: __tmp__<branchID>.snapshot
2. Write header (JSON with version)
3. Write root record (branch ID + RootRecord)
4. Write ledger identity (genesis time + description)
5. Iterate upgrade DB partition → write count + libraries
6. Iterate trie state → write all KV pairs
7. Close and rename: __tmp__... → <branchID>.snapshot
```

## Restore Workflow

The `RestoreFromSnapshot` function in `core/core_modules/state_cleanup/restore.go`:

```
1. Open snapshot file stream
2. Read header, root record, identity, upgrade libraries (synchronous)
3. Validate snapshot (check ledger hash matches)
4. Delete existing DB (if present)
5. Create fresh DB with restore-in-progress marker
6. Write all upgrade libraries to upgrade DB partition
7. Create empty trie root with ledger identity
8. Stream trie data → batch insert into trie
9. Write metadata (latest/earliest slot, root record)
10. Clear restore-in-progress marker
11. Verify final root matches expected root
```

## Key Data Structures

### SnapshotFileStream

Returned by `OpenSnapshotFileStream`:

```go
type SnapshotFileStream struct {
    Header           *SnapshotHeader        // Version info
    LedgerIdentity   *ledger.LedgerIdentity // Genesis time + description
    UpgradeLibraries []UpgradeLibraryEntry  // All upgrade libraries
    BranchID         base.TransactionID     // Branch this snapshot represents
    RootRecord       RootRecord             // State metadata
    InChan           chan KVPairOrError     // Trie data stream
    Close            func()                 // Cleanup function
}
```

### UpgradeLibraryEntry

```go
type UpgradeLibraryEntry struct {
    Slot        uint32  // Upgrade slot (0 = genesis)
    LibraryYAML []byte  // Full compiled library YAML
}
```

### RootRecord

```go
type RootRecord struct {
    SequencerID     ledger.ChainID      // Sequencer that produced the branch
    Root            common.VCommitment  // Merkle root of state trie
    LedgerCoverage  uint64              // Total token coverage
    SlotInflation   uint64              // Inflation in this slot
    Supply          uint64              // Total supply at this branch
    NumTransactions uint32              // Transaction count in slot
}
```

## CLI Commands

| Command | Description |
|---------|-------------|
| `proxi snapshot info [file]` | Display snapshot metadata and statistics |
| `proxi snapshot check [file]` | Verify snapshot is compatible with network |
| `proxi snapshot restore -s file` | Restore DB from snapshot |

### Example: Viewing Snapshot Info

```bash
$ proxi snapshot info mystate.snapshot

snapshot file: mystate.snapshot
format version: ver 1
branch id: 00000123...abc (hex = ...)
root record:
    SequencerID: ...
    Root: ...
    LedgerCoverage: 1000000000
    Supply: 1000000000
ledger identity: genesis=1234567890, description="proxima testnet"
upgrade libraries: 1
  - slot 0: 45678 bytes
ledger constants:
    GenesisTimeUnix: 1234567890
    ...
```

## Automatic Restore on Startup

When `state_cleanup` is enabled in `proxima.yaml`, the node automatically restores from snapshot if:

1. **DB is missing**: Fresh node with only a snapshot file
2. **DB is corrupted**: Restore-in-progress marker present from interrupted restore
3. **Cleanup triggered**: State file indicates periodic cleanup in progress

### Configuration

```yaml
state_cleanup:
  enable: true
  period_slots: 8438      # ~24 hours between cleanups
  window_slots: 1406      # ~4 hour random window
  ttl_minutes: 10         # Max time for cleanup operation
  snapshot_directory: snapshot
```

### Restore Process

```
node.Start()
  └─> checkAndRestoreOnStartup()
        ├─> Check if DB missing/corrupted
        ├─> Find latest .snapshot file
        ├─> Validate snapshot matches ledger config
        ├─> Delete old/corrupted DB
        ├─> RestoreFromSnapshot()
        └─> Continue normal startup
```

## Trie Partitions

Data in the trie (and snapshot) is organized by partition:

| Partition Byte | Name | Content | Key Format |
|----------------|------|---------|------------|
| 0x00 | LedgerState | Transactions and UTXOs | TxID (32 bytes) or OutputID (33 bytes) |
| 0x04 | Accounts | Account balances index | Account address bytes |
| 0x05 | ChainID | Chain output index | Chain ID bytes |
| 0x07 | UpgradeLibrary | Library versions (DB only) | Slot number (4 bytes) |

Note: Partition 0x07 (upgrade libraries) is stored in the DB partition, not in the trie. In snapshots, upgrade libraries are stored as separate records before the trie data.

## Error Handling

### Restore-in-Progress Marker

- Written to DB at start of restore
- Cleared only on successful completion
- If present on startup, DB is considered corrupted and deleted

### Root Verification

After restoring all trie data, the final computed root must match the expected root from the snapshot's RootRecord. Mismatch indicates data corruption.

### Atomic Writes

Snapshots are written to temporary files (`__tmp__` prefix) and renamed only after successful completion. This ensures partial/corrupted snapshots are never used.

## Upgrade Libraries in Snapshots

Snapshots include all upgrade libraries from the DB partition. This ensures:

1. **Self-contained**: Snapshot has everything needed to restore state
2. **Version preservation**: Historical library versions preserved for transaction validation
3. **Early access**: Libraries loaded before trie data for validation during restore

Libraries are stored in slot order, with slot 0 always present (genesis library).

## Version History

| Version | Changes |
|---------|---------|
| ver 0 | Initial format with full library YAML as "ledger identity" |
| ver 1 | Minimal identity + separate upgrade libraries section |
