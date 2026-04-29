# Snapshot Restore Module

The snapshot restore module provides automatic ledger state management via periodic snapshot restore. Over time, the multistate database accumulates historical state that is no longer needed. This module automates the cleanup process by periodically restoring from the latest snapshot, and also handles recovery when the database is missing or corrupted on startup.

## Snapshot Directory

The `snapshot.directory` config key is the **single authoritative location** for snapshot files, used by both the snapshot creation module and the snapshot restore module. Default is `""` (current working directory).

- Snapshot creation writes to this directory
- Snapshot restore searches this directory
- On startup with missing/corrupted DB, the node finds the latest snapshot here
- If no snapshot is found, **the node refuses to start**

The `snapshot_restore.snapshot_directory` config can override this for the restore module only (e.g., to point to another node's snapshot directory for shared snapshots).

## Overview

The cleanup process works as follows:

1. **Scheduler** monitors the current slot and triggers cleanup when scheduled
2. **Trigger** finds the latest snapshot in the snapshot directory and validates it
3. **Graceful shutdown** and **self-restart** replaces the current process
4. **Restore** on startup detects cleanup-in-progress and restores from snapshot
5. **Resume** normal operation with clean state

## Components

### snapshot_restore.go

Main scheduler module with the following key functions:

- `Start(env)` - Initializes and starts the cleanup scheduler
- `CheckAndRestoreOnStartup(log)` - Called at node startup to check if restore is needed
- `scheduleNextCleanup()` - Calculates next cleanup slot with randomization
- `checkAndTriggerCleanup()` - Periodic check, triggers cleanup when due
- `triggerCleanup()` - Validates snapshot, sets flags, initiates shutdown

**Global flags** (used by main.go for restart coordination):
- `CleanupRequestedFlag` - Set when cleanup triggered, signals restart needed
- `SnapshotFileForRestore` - Path to snapshot file for restore

### state_file.go

Manages persistent state in `.snapshot_restore.json`:

```json
{
  "last_cleanup_completed": "2024-01-05T10:30:00Z",
  "next_cleanup_slot": 12345678,
  "cleanup_in_progress": false,
  "cleanup_started_at": null,
  "snapshot_file": null
}
```

Key methods:
- `NewStateFileManager(path)` - Create or load state file
- `SetNextCleanupSlot(slot)` - Schedule next cleanup
- `StartCleanup(snapshotFile)` - Mark cleanup as in progress
- `CompleteCleanup(nextSlot)` - Mark cleanup complete, schedule next
- `ResetCleanupState()` - Reset on TTL exceeded
- `IsCleanupTTLExceeded(ttl)` - Check if cleanup taking too long

### restore.go

Restore utilities extracted from proxi snapshot command:

- `RestoreFromSnapshot(path, opts)` - Core restore logic, returns stats. Automatically handles corrupted DB from interrupted restores.
- `CheckAndDeleteCorruptedDB(dbPath, console)` - Check if DB has restore-in-progress marker and delete if corrupted
- `FindLatestSnapshot(directory)` - Find most recent .snapshot file (excludes `__tmp__*` files that are still being written). Uses `"."` (cwd) if directory is empty.
- `ValidateSnapshot(path)` - Verify snapshot compatible with current ledger
- `DeleteDatabase(path)` - Remove multistate database directory
- `CheckPermissions(dbPath, snapshotPath)` - Verify read/write access

### snapshot.SnapshotDirectory()

Shared function in `core/core_modules/snapshot/snapshot.go` that returns the resolved `snapshot.directory` config value. Both snapshot creation and snapshot_restore use this to determine the canonical snapshot location.

### Database integrity (multistate/roots.go)

The restore process uses a `restoreInProgressDBPartition` marker in the database:

- `WriteRestoreInProgressRecord(w)` - Set marker at start of restore
- `DeleteRestoreInProgressRecord(w)` - Clear marker after successful completion
- `IsRestoreInProgress(store)` - Check if marker exists (indicates corrupted DB)

## Configuration

Add to `proxima.yaml`:

```yaml
snapshot:
  enable: false
  directory: ""                # Single location for all snapshots. Default "" = current working directory.
  period_in_slots: 64          # How often to create snapshots (when enabled)
  keep_latest: 2               # Number of snapshots to retain
  enable_api: false            # Enable snapshot download API endpoint

snapshot_restore:
  enable: false                  # Master switch for periodic cleanup
  period_slots: 8438             # ~24 hours at 10.24 sec/slot
  window_slots: 1406             # ~4 hour randomization window
  ttl_minutes: 10                # Max time for cleanup, else assume failure
  # snapshot_directory: /path/to/snapshots  # Optional: override snapshot.directory
  # log_file: .snapshot_restore.log         # Optional: separate log file
```

### Parameters

| Parameter | Default | Description |
|-----------|---------|-------------|
| `snapshot.directory` | `""` (cwd) | Single location for snapshot files |
| `snapshot.enable` | `false` | Enable periodic snapshot creation |
| `snapshot_restore.enable` | `false` | Enable periodic cleanup/restore |
| `snapshot_restore.period_slots` | `8438` | Slots between cleanups (~24h) |
| `snapshot_restore.window_slots` | `1406` | Random offset window (~4h) to avoid mass restarts |
| `snapshot_restore.ttl_minutes` | `10` | If cleanup takes longer, assume failure and reset |
| `snapshot_restore.snapshot_directory` | (uses `snapshot.directory`) | Override: can point to another node's snapshots |
| `snapshot_restore.log_file` | (none) | Optional separate log file for cleanup events |

### Snapshot Isolation

The snapshot creation process is designed as a low-priority background task that does not block the sequencer or other node operations:

- **Separate state reader**: Each snapshot creates a fresh `Readable` with its own `TrieReader` and `NodeStore`. No shared mutex with the cached state readers in `branches.stateReaders`.
- **No lock contention**: The trie iterator bypasses `Readable.mutex`. Each `TrieReader` has its own cache.
- **BadgerDB concurrent reads**: The only shared resource is the underlying BadgerDB, which natively supports concurrent reads without blocking.
- **Background execution**: Runs via `RepeatInBackground` goroutine.

## Workflow

### Startup: DB Missing or Corrupted

```
node.Start()
  └─> CheckAndRestoreOnStartup()
        ├─> CheckAndDeleteCorruptedDB()
        │     ├─> DB missing → needs restore
        │     ├─> DB can't open → delete, needs restore
        │     └─> DB has restore-in-progress marker → delete, needs restore
        ├─> Find latest .snapshot in snapshot.directory
        │     └─> No snapshot found → REFUSE TO START (fatal error)
        ├─> Validate snapshot matches ledger config
        ├─> Delete old/corrupted DB
        ├─> RestoreFromSnapshot()
        └─> Continue normal startup
```

### Normal Operation (periodic cleanup)

```
┌──────────────────────────────────────────────────────────────┐
│                         Node Running                          │
│                                                               │
│  ┌──────────────────┐    every 60s    ┌────────────────────┐ │
│  │ Cleanup Scheduler │───────────────►│ Check current slot │ │
│  └──────────────────┘                 │ vs next_cleanup    │ │
│                                       └────────┬───────────┘ │
│                              ┌─────────────────┴──────────┐  │
│                              ▼                            ▼  │
│                    slot < scheduled          slot >= scheduled │
│                         │                           │         │
│                         ▼                           ▼         │
│                    Do nothing              Find latest snapshot│
│                                            in snapshot.directory
│                                                  │            │
│                              ┌───────────────────┴─────────┐ │
│                              ▼                             ▼ │
│                    No snapshot found          Snapshot found  │
│                    → reschedule                     │         │
│                                                    ▼         │
│                                          Validate + shutdown │
│                                          → self-restart      │
└──────────────────────────────────────────────────────────────┘
```

## Platform Support

The self-restart mechanism is platform-specific:

| Platform | Implementation | Behavior |
|----------|---------------|----------|
| Linux | `syscall.Exec` | Replaces process (same PID) |
| Darwin | `syscall.Exec` | Replaces process (same PID) |
| Windows | `exec.Command` | Spawns new process, exits old (new PID) |

See `util/restart/` for platform-specific implementations.

## Edge Cases

1. **No snapshot available on startup**: Node refuses to start with a fatal error
2. **No snapshot available for periodic cleanup**: Logs error, reschedules cleanup for later
3. **Snapshot incompatible**: Validates ledger hash before restore
4. **TTL exceeded**: If cleanup takes too long, resets state and continues normal startup
5. **Permission denied**: Logs error, reschedules cleanup
6. **Node crash during restore**: Database has `restoreInProgress` marker; on next startup, corrupted DB is automatically deleted and restore proceeds fresh
7. **Multiple nodes on same machine**: Each uses own `.snapshot_restore.json` in working directory
8. **Temporary snapshot files**: Files with `__tmp__` prefix are skipped (still being written by snapshot module)
9. **Database absent**: Fresh database is created automatically during restore
10. **Database corrupted/unopenable**: Automatically deleted and recreated during restore

## Files

| File | Purpose |
|------|---------|
| `.snapshot_restore.json` | Persistent cleanup state |
| `.snapshot_restore.log` | Optional cleanup activity log |
| `<snapshot.directory>/*.snapshot` | Snapshot files (single canonical location) |

## Dependencies

- Requires snapshot files to be available in the configured `snapshot.directory`
- Unless `snapshot_restore.snapshot_directory` points elsewhere, typically needs snapshot module enabled and producing snapshots
- Uses `util/restart` for platform-specific self-restart
- Uses `snapshot.SnapshotDirectory()` for consistent directory resolution
- Integrates with workflow via `Start()` call
- Integrates with node via `CheckAndRestoreOnStartup()` call
