# Snapshot Restore Module

The snapshot restore module provides automatic ledger state management via periodic snapshot restore. Over time, the multistate database accumulates historical state that is no longer needed. This module automates the cleanup process by periodically restoring from the latest snapshot, and also handles bootstrap from genesis snapshot when the database is missing.

## Overview

The cleanup process works as follows:

1. **Scheduler** monitors the current slot and triggers cleanup when scheduled
2. **Trigger** validates the latest snapshot and initiates graceful shutdown
3. **Self-restart** replaces the current process with a fresh instance
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
- `FindLatestSnapshot(directory)` - Find most recent .snapshot file (excludes `__tmp__*` files that are still being written)
- `ValidateSnapshot(path)` - Verify snapshot compatible with current ledger
- `DeleteDatabase(path)` - Remove multistate database directory
- `CheckPermissions(dbPath, snapshotPath)` - Verify read/write access
- `CopyFile(src, dst)` - Copy a file (used to copy snapshot to working directory)

### Database integrity (multistate/roots.go)

The restore process uses a `restoreInProgressDBPartition` marker in the database:

- `WriteRestoreInProgressRecord(w)` - Set marker at start of restore
- `DeleteRestoreInProgressRecord(w)` - Clear marker after successful completion
- `IsRestoreInProgress(store)` - Check if marker exists (indicates corrupted DB)

## Configuration

Add to `proxima.yaml`:

```yaml
snapshot_restore:
  enable: false                  # Master switch
  period_slots: 8438             # ~24 hours at 10.24 sec/slot
  window_slots: 1406             # ~4 hour randomization window
  ttl_minutes: 10                # Max time for cleanup, else assume failure
  snapshot_directory: /path/to/snapshots  # Optional: override snapshot directory. Where to search for the latest state snapshot
  log_file: .snapshot_restore.log   # Optional: separate log file for cleanup activity
```

### Parameters

| Parameter | Default | Description |
|-----------|---------|-------------|
| `enable` | `false` | Enable/disable automatic cleanup |
| `period_slots` | `8438` | Slots between cleanups (~24h) |
| `window_slots` | `1406` | Random offset window (~4h) to avoid mass restarts |
| `ttl_minutes` | `10` | If cleanup takes longer, assume failure and reset |
| `snapshot_directory` | `snapshot.directory` | Directory to find snapshots (can point to another node's snapshots) |
| `log_file` | (none) | Optional separate log file for cleanup events |

### Cleanup Logging

When `log_file` is configured, cleanup activity is logged to a separate file with detailed stats:

```
01-05 10:30:00 snapshot_restore	INFO	=== State cleanup scheduler started ===
01-05 10:30:00 snapshot_restore	INFO	Period: 8438 slots (~24h0m0s)
01-05 10:30:00 snapshot_restore	INFO	Next cleanup scheduled for slot: 12345678
...
01-06 10:35:00 snapshot_restore	INFO	=== CLEANUP TRIGGERED at slot 12345678 ===
01-06 10:35:00 snapshot_restore	INFO	found snapshot: snapshot/branch_12345000.snapshot
01-06 10:35:00 snapshot_restore	INFO	snapshot validated successfully
01-06 10:35:01 snapshot_restore	INFO	cleanup prepared in 1.2s, initiating restart...
...
01-06 10:35:05 snapshot_restore	INFO	=== RESTORE STARTED ===
01-06 10:35:05 snapshot_restore	INFO	snapshot file: /home/node/snapshot/branch_12345000.snapshot
01-06 10:35:05 snapshot_restore	INFO	database size before: 2.45 GB
01-06 10:35:05 snapshot_restore	INFO	deleted old database in 150ms
01-06 10:35:30 snapshot_restore	INFO	restore completed: 1500000 records in 25s
01-06 10:35:30 snapshot_restore	INFO	  - transactions: 50000
01-06 10:35:30 snapshot_restore	INFO	  - UTXOs: 1200000
01-06 10:35:30 snapshot_restore	INFO	  - chains: 150
01-06 10:35:30 snapshot_restore	INFO	  - accounts: 249850
01-06 10:35:30 snapshot_restore	INFO	database size after: 1.82 GB
01-06 10:35:30 snapshot_restore	INFO	database size reduced by: 645.12 MB (25.7%)
01-06 10:35:30 snapshot_restore	INFO	snapshot copied to: branch_12345000.snapshot
01-06 10:35:30 snapshot_restore	INFO	old snapshots cleaned up in working directory
01-06 10:35:30 snapshot_restore	INFO	=== CLEANUP COMPLETED in 25.5s ===
01-06 10:35:30 snapshot_restore	INFO	next cleanup scheduled for slot 12354116
```

## Workflow

### Normal Operation

```
┌─────────────────────────────────────────────────────────────────┐
│                         Node Running                             │
│                                                                  │
│  ┌──────────────────┐    every 60s    ┌──────────────────────┐  │
│  │ Cleanup Scheduler │───────────────►│ Check current slot   │  │
│  └──────────────────┘                 │ vs next_cleanup_slot │  │
│                                       └──────────┬───────────┘  │
│                                                  │               │
│                              ┌───────────────────┴───────────┐  │
│                              ▼                               ▼  │
│                    slot < scheduled          slot >= scheduled  │
│                         │                           │            │
│                         ▼                           ▼            │
│                    Do nothing              Trigger Cleanup       │
│                                                  │               │
└──────────────────────────────────────────────────┼───────────────┘
                                                   │
                                                   ▼
                                          ┌───────────────┐
                                          │ Validate      │
                                          │ Snapshot      │
                                          └───────┬───────┘
                                                  │
                                                  ▼
                                          ┌───────────────┐
                                          │ Set cleanup   │
                                          │ in progress   │
                                          └───────┬───────┘
                                                  │
                                                  ▼
                                          ┌───────────────┐
                                          │ Graceful      │
                                          │ Shutdown      │
                                          └───────┬───────┘
                                                  │
                                                  ▼
                                          ┌───────────────┐
                                          │ Self-Restart  │
                                          │ (syscall.Exec)│
                                          └───────────────┘
```

### Startup with Cleanup in Progress

```
┌─────────────────────────────────────────────────────────────────┐
│                       Node Startup                               │
│                                                                  │
│  ┌──────────────────────────┐                                   │
│  │ CheckAndRestoreOnStartup │                                   │
│  └────────────┬─────────────┘                                   │
│               │                                                  │
│               ▼                                                  │
│  ┌──────────────────────────┐                                   │
│  │ Load .snapshot_restore.json │                                   │
│  └────────────┬─────────────┘                                   │
│               │                                                  │
│       ┌───────┴───────┐                                         │
│       ▼               ▼                                         │
│  in_progress=false  in_progress=true                            │
│       │               │                                         │
│       ▼               ▼                                         │
│  Normal startup   ┌───────────────┐                             │
│                   │ Check TTL     │                             │
│                   └───────┬───────┘                             │
│                           │                                      │
│               ┌───────────┴───────────┐                         │
│               ▼                       ▼                         │
│          TTL exceeded            TTL OK                         │
│               │                       │                         │
│               ▼                       ▼                         │
│          Reset state          ┌───────────────┐                 │
│          Normal startup       │ Delete old DB │                 │
│                               └───────┬───────┘                 │
│                                       │                         │
│                                       ▼                         │
│                               ┌───────────────┐                 │
│                               │ Restore from  │                 │
│                               │ Snapshot      │                 │
│                               └───────┬───────┘                 │
│                                       │                         │
│                                       ▼                         │
│                               ┌───────────────┐                 │
│                               │ Copy snapshot │                 │
│                               │ to working dir│                 │
│                               └───────┬───────┘                 │
│                                       │                         │
│                                       ▼                         │
│                               ┌───────────────┐                 │
│                               │ Cleanup old   │                 │
│                               │ snapshots     │                 │
│                               └───────┬───────┘                 │
│                                       │                         │
│                                       ▼                         │
│                               ┌───────────────┐                 │
│                               │ Complete      │                 │
│                               │ cleanup state │                 │
│                               └───────┬───────┘                 │
│                                       │                         │
│                                       ▼                         │
│                               Normal startup                    │
│                               (with clean DB)                   │
└─────────────────────────────────────────────────────────────────┘
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

1. **No snapshot available**: Logs error, reschedules cleanup for later
2. **Snapshot incompatible**: Validates ledger hash before restore
3. **TTL exceeded**: If cleanup takes too long, resets state and continues normal startup
4. **Permission denied**: Logs error, reschedules cleanup
5. **Node crash during restore**: Database has `restoreInProgress` marker; on next restore attempt, corrupted DB is automatically deleted and restore proceeds fresh
6. **Multiple nodes on same machine**: Each uses own `.snapshot_restore.json` in working directory
7. **Temporary snapshot files**: Files with `__tmp__` prefix are skipped (still being written by snapshot module)
8. **Database absent**: Fresh database is created automatically during restore
9. **Database corrupted/unopenable**: Automatically deleted and recreated during restore

## Post-Restore Behavior

After a successful restore:

1. **Snapshot copy**: The used snapshot file is copied to the node's working directory
2. **Cleanup**: All other `.snapshot` files in the working directory are deleted, keeping only the most recent one
3. This ensures the working directory always has exactly one snapshot file - the one used for the last restore

## Files

| File | Purpose |
|------|---------|
| `.snapshot_restore.json` | Persistent cleanup state |
| `.snapshot_restore.log` | Optional cleanup activity log |
| `snapshot/*.snapshot` | Snapshot files to restore from (source directory) |
| `*.snapshot` (working dir) | Copy of last used snapshot file |

## Dependencies

- Unless `snaphot_directory` points to another node's snapshots, requires snapshot module to be enabled and producing snapshots
- Uses `util/restart` for platform-specific self-restart
- Integrates with workflow via `Start()` call
- Integrates with node via `CheckAndRestoreOnStartup()` call
