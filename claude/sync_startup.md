# Task: Sync on Startup — Auto-download Snapshot from Trusted Sources

> **Update 2026-06-15:** the snapshot source list was decoupled from forward-sync and renamed.
> Snapshot acquisition now reads the **shared top-level `sources`** list (trusted node API
> endpoints), not `sync.sources`. The `sync:` section governs forward-sync tuning only;
> forward-sync **activation is governed by whether the `sources` list is populated** — with
> sources it runs, with none it is off (there is no separate on/off flag; the former
> `sync.disable` flag was removed). Read every `sync.sources` reference below as the top-level
> `sources` list. Order is unchanged: remote `sources` → local `snapshot.directory`.

## Overview

Extend the node startup behavior: when the DB is absent or corrupted, before falling back to
the local snapshot directory, try to download a newer snapshot from the trusted API hosts listed
in `sync.sources`. This allows a new node to bootstrap from the network without manual snapshot
transfer.

## Current behavior (`CheckAndRestoreOnStartup`)

1. Check if DB exists and is valid
2. If missing/corrupted, look for latest `.snapshot` file in `snapshot.directory`
3. Restore from that file
4. If no snapshot found, refuse to start

## New behavior

Insert a step between 1 and 2:

1. Check if DB exists and is valid
2. **If missing/corrupted, try to download a newer snapshot from `sync.sources`:**
   a. For each source in `sync.sources` (skipping self URLs):
      - Call `GET /api/v1/get_snapshot_info` to get the slot and file size of the latest snapshot
      - If the source doesn't host snapshots (error response), skip it
   b. Pick the source with the highest snapshot slot
   c. Compare with the local snapshot (if any): download only if the remote is newer
   d. Download via existing `GET /api/v1/get_snapshot` endpoint (already implemented)
   e. Save to `snapshot.directory`
3. Proceed with existing restore logic (find latest local snapshot, restore)
4. If no snapshot found locally or remotely, refuse to start

## New API endpoint

### `GET /api/v1/get_snapshot_info`

Returns metadata about the latest available snapshot on this host.

**Response**:
```json
{
  "slot": 54646,
  "file_size": 12345678,
  "file_name": "54646_0br_01f790c67d89...snapshot"
}
```

If `snapshot.enable_download_api` is false or no snapshot exists, returns error.

**Server implementation** (`api/server/`):
- Call `srv.GetSnapshotFilePath()` to find the latest snapshot file
- Parse slot from the file name (first number before `_`)
- Stat the file for size
- Return the info

**Client** (`api/client/`):
- `GetSnapshotInfo() (slot uint32, fileSize int64, fileName string, err error)`

**Path constant** in `api/api.go`:
```go
PathGetSnapshotInfo = PrefixAPIV1 + "/get_snapshot_info"
```

## Implementation steps

### Step 1: API endpoint `get_snapshot_info`
- Add path constant and response struct to `api/api.go`
- Implement server handler (reuse `GetSnapshotFilePath()`, parse slot from filename, stat for size)
- Implement client function
- Register handler in server

### Step 2: Modify startup in `snapshot_restore`
- Read `sync.sources` from viper (same list as the sync module uses)
- Filter out self URLs (same `isSelfURL` logic — consider extracting to a shared util)
- For each source, call `GetSnapshotInfo()` — collect slot + URL of the best candidate
- Compare best remote slot with local snapshot slot (parse from local filename)
- If remote is newer, call `DownloadSnapshot(snapshotDir)` (already exists in client)
- Then proceed with normal `CheckAndRestoreOnStartup` flow

### Step 3: Extract `isSelfURL` to shared location
- Move `isSelfURL` from `core/core_modules/sync/sync.go` to a shared place
  (e.g., `global/` or keep in sync package and export it)
- Use from both the sync module and the startup logic

## Edge cases

- **All sources unreachable**: log warning, fall back to local snapshot (current behavior)
- **No sources configured**: skip remote download, fall back to local (current behavior)
- **Remote snapshot is older than local**: skip download, use local
- **Download interrupted**: the existing `DownloadSnapshot` writes to a temp file first;
  incomplete downloads don't corrupt the snapshot directory
- **snapshot.enable_download_api=false on source**: `get_snapshot_info` returns error, source is skipped
- **Multiple sources with same slot**: pick any (first found is fine)

## What stays the same

- `CheckAndRestoreOnStartup` logic for DB detection and restore
- `DownloadSnapshot` client function (already handles Content-Disposition filename)
- Snapshot save/purge cycle on running nodes
- The `get_snapshot` download endpoint (unchanged)
