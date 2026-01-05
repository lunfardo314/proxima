# State Cleanup Module

The state cleanup module provides automatic ledger state garbage collection via periodic snapshot restore. Over time, the multistate database accumulates historical state that is no longer needed. This module automates the cleanup process by periodically restoring from the latest snapshot.

## Overview

The cleanup process works as follows:

1. **Scheduler** monitors the current slot and triggers cleanup when scheduled
2. **Trigger** validates the latest snapshot and initiates graceful shutdown
3. **Self-restart** replaces the current process with a fresh instance
4. **Restore** on startup detects cleanup-in-progress and restores from snapshot
5. **Resume** normal operation with clean state

## Components

### state_cleanup.go

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

Manages persistent state in `.state_cleanup.json`:

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

- `RestoreFromSnapshot(path, opts)` - Core restore logic, returns stats
- `FindLatestSnapshot(directory)` - Find most recent .snapshot file
- `ValidateSnapshot(path)` - Verify snapshot compatible with current ledger
- `DeleteDatabase(path)` - Remove multistate database directory
- `CheckPermissions(dbPath, snapshotPath)` - Verify read/write access

## Configuration

Add to `proxima.yaml`:

```yaml
state_cleanup:
  enable: false                  # Master switch
  period_slots: 8438             # ~24 hours at 10.24 sec/slot
  window_slots: 1406             # ~4 hour randomization window
  ttl_minutes: 10                # Max time for cleanup, else assume failure
  snapshot_directory: /path/to/snapshots  # Optional: override snapshot directory
  log_file: .state_cleanup.log   # Optional: separate log file for cleanup activity
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
01-05 10:30:00 state_cleanup	INFO	=== State cleanup scheduler started ===
01-05 10:30:00 state_cleanup	INFO	Period: 8438 slots (~24h0m0s)
01-05 10:30:00 state_cleanup	INFO	Next cleanup scheduled for slot: 12345678
...
01-06 10:35:00 state_cleanup	INFO	=== CLEANUP TRIGGERED at slot 12345678 ===
01-06 10:35:00 state_cleanup	INFO	found snapshot: snapshot/branch_12345000.snapshot
01-06 10:35:00 state_cleanup	INFO	snapshot validated successfully
01-06 10:35:01 state_cleanup	INFO	cleanup prepared in 1.2s, initiating restart...
...
01-06 10:35:05 state_cleanup	INFO	=== RESTORE STARTED ===
01-06 10:35:05 state_cleanup	INFO	restoring from snapshot: snapshot/branch_12345000.snapshot
01-06 10:35:05 state_cleanup	INFO	deleted old database in 150ms
01-06 10:35:30 state_cleanup	INFO	restore completed: 1500000 records in 25s
01-06 10:35:30 state_cleanup	INFO	  - transactions: 50000
01-06 10:35:30 state_cleanup	INFO	  - UTXOs: 1200000
01-06 10:35:30 state_cleanup	INFO	  - chains: 150
01-06 10:35:30 state_cleanup	INFO	  - accounts: 249850
01-06 10:35:30 state_cleanup	INFO	=== CLEANUP COMPLETED in 25.5s ===
01-06 10:35:30 state_cleanup	INFO	next cleanup scheduled for slot 12354116
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
│  │ Load .state_cleanup.json │                                   │
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
5. **Node crash during restore**: On next start, TTL check handles recovery
6. **Multiple nodes on same machine**: Each uses own `.state_cleanup.json` in working directory

## Files

| File | Purpose |
|------|---------|
| `.state_cleanup.json` | Persistent cleanup state |
| `.state_cleanup.log` | Optional cleanup activity log |
| `snapshot/*.snapshot` | Snapshot files to restore from |

## Dependencies

- Requires snapshot module to be enabled and producing snapshots
- Uses `util/restart` for platform-specific self-restart
- Integrates with workflow via `Start()` call
- Integrates with node via `CheckAndRestoreOnStartup()` call
