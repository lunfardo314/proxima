# Syncing Architecture

## Current approach and its problems

The node syncs by receiving sequencer transactions via gossip and spawning attacher goroutines
for each. Each attacher recursively pulls dependencies, which spawn more attachers. This creates
a tree of goroutines waiting for their dependencies to finish.

### Problems observed (2025-03-19 testnet sessions)

- **Resource explosion**: 235 slots behind: 200 concurrent attachers, 5193 seq transactions dropped.
  Each attacher holds a past cone in memory, waiting for dependencies.
- **Deadlock with rate control**: dependencies arrive via gossip but get dropped by rate control.
  Disabling rate control during sync removes the deadlock but allows unbounded resource usage.
- **Doesn't scale**: more than a few hundred slots back (~1 hour) creates unsustainable attacher load.
- **IsSynced() oscillation**: rate control decisions based on `IsSynced()` can oscillate near the boundary.

### Key insight

Sequential sync doesn't need recursive attachers. We can sync one branch at a time if we know
the branch sequence. The branch chain is deterministic via stem links. Walking it slot by slot,
committing each branch before moving to the next, gives bounded resource usage.

---

## Design: Sequential Sync Mode

### IsSyncing flag (workflow level)

A global `IsSyncing` flag with hysteresis to avoid oscillation:

- **Set to true** when gap between wall-clock slot and latest committed healthy branch >= `SyncThresholdUp` (e.g., 5 slots)
- **Set to false** when gap <= `SyncThresholdDown` (e.g., 3 slots)
- Located in `Workflow` (or a new `sync` core module), accessible via `w.IsSyncing()`
- Distinct from `IsSynced()` — the two are NOT simple inverses due to hysteresis

### Transaction filtering during sync

When `IsSyncing == true`, a transaction passes to attachment only if **both** conditions are met:
- It is **pulled** (`wanted == true`)
- Its timestamp is **at or before** the slot of the last pulled branch in the sync list

Otherwise (not pulled, OR timestamp is after the sync frontier), the transaction is **dropped
from attachment**. It is already persisted in txstore and can be pulled later.

This prevents pull-recursion from gossip-received transactions that arrive ahead of the sync
frontier — even if they were pulled by an attacher that started just before sync mode kicked in.

### Sync process — stateless loop

The sync process runs as a periodic check loop, not a stateful coroutine. On each tick it
evaluates the current state and takes the appropriate action:

```
loop (every ~1s):
  1. Compute gap = wallClockSlot - latestCommittedHealthySlot
  2. If gap < SyncThresholdDown:
       - clear IsSyncing, clear branch list
       - continue
  3. If gap >= SyncThresholdUp (or already IsSyncing):
       - set IsSyncing = true
  4. If branch list is empty:
       - request branch list from sync source (from_slot = latestCommittedSlot)
       - if request fails, log warning, continue (retry next tick)
  5. Remove from head of list any branches already committed in DB
  6. If list is empty after cleanup:
       - clear list (will re-request next tick if still behind)
       - continue
  7. Pull the first branch in the list (if not already pulled)
  8. Wait for commit or timeout, continue
```

This is fully stateless in the sense that the loop can be restarted at any point (node restart,
error recovery) and it will re-derive the correct action from current DB state. The branch list
is just a cache that gets re-requested when exhausted or stale.

### Attacher limit

Keep `maxConcurrentAttachers` as a fixed limit (e.g., 20 or `3 * runtime.NumCPU()`), applied
uniformly regardless of sync state. The current value of 200 is too high. During sync, only 1
branch attacher runs at a time plus a handful of dependency attachers in its past cone, so the
limit is naturally respected.

## New API endpoint

### `GET /api/v1/get_branch_list?from_slot=<slot>&max=<max>`

Returns branch transaction IDs on the main chain, **forward** from `from_slot` to LRB.

**Server implementation** (`api/server/`):
- Find LRB via `FindLatestReliableBranch()`
- Walk back from LRB via `IterateBranchChainBack()` collecting branches
- Filter to those with slot > `from_slot`
- Reverse to oldest-first order
- Cap at `max` entries (default 100)
- Return JSON array of hex-encoded transaction IDs

**Response**:
```json
{
  "branches": ["aabb...", "ccdd...", ...],
  "lrb_slot": 57890
}
```

**Client** (`api/client/`):
- `GetBranchList(fromSlot uint32, max int) ([]base.TransactionID, uint32, error)`

**Path constant** in `api/api.go`:
```go
PathGetBranchList = PrefixAPIV1 + "/get_branch_list"
```

## Configuration

In `proxima.yaml`:
```yaml
sync:
  # trusted API endpoint to request branch lists from during sync
  # typically an access node that is known to be reliable
  source: "http://113.30.191.219:8001"
  # optional overrides (defaults shown)
  threshold_up: 5
  threshold_down: 3
```

If `sync.source` is not configured, the node cannot enter sync mode — it falls back to the
current gossip-only behavior (with a warning log).

## Implementation steps

### Step 1: API endpoint `get_branch_list`
- Add path constant to `api/api.go`
- Add response struct to `api/api.go`
- Implement server handler in `api/server/server.go` (similar to `getMainChain` but forward order)
- Implement client function in `api/client/client.go`
- This can be tested independently

### Step 2: Sync core module
- New package `core/core_modules/sync` (or extend workflow directly)
- `IsSyncing()` method on Workflow with hysteresis
- Stateless sync loop as described above
- Log sync progress: "syncing: committed slot X, target slot Y, N remaining"

### Step 3: Transaction filtering
- Modify `seq_attach.consume()`: when `IsSyncing()`, pass only if pulled AND timestamp <= sync frontier slot
- Modify `nonseq_attach`: same logic
- Sync frontier slot = slot of the branch currently being synced (head of the list)
- The `IsSynced()` function can remain for other uses, or be replaced by `!IsSyncing()`

### Step 4: Attacher limit adjustment
- Change `maxConcurrentAttachers` from 200 to 20 (or `3 * NumCPU`)
- This applies in both synced and syncing modes

### Step 5: Config and startup
- Read `sync.source`, `sync.threshold_up`, `sync.threshold_down` from viper
- Add `sync:` section to `proxi init node` config template (`proxi/init_cmd/`)
- On node startup, check gap immediately — if behind, enter sync mode before starting sequencer
- Sequencer should not start proposing until `IsSyncing == false`

## Edge cases

- **Sync source unreachable**: log warning, retry next loop tick. Do not fall back to
  uncontrolled gossip sync — wait for the source to come back.
- **Sync source returns empty list**: LRB on the source is at or before our committed slot.
  Clear list, re-check gap next tick.
- **Branch pull times out**: retry on next loop tick (branch stays at head of list).
- **Node restart during sync**: on startup, loop re-derives state from DB. No persistent
  sync state needed — branch list is re-requested.
- **Pull recursion started before sync**: the timestamp filter handles this — any dependency
  pulled by a pre-sync attacher that is ahead of the sync frontier gets dropped from attachment.
  The attacher will eventually time out. Its pulled transactions remain in txstore for later.
- **Multiple healthy branches at same slot**: the sync source returns branches on its own main
  chain (LRB path). The syncing node trusts this during catch-up. Once caught up, it determines
  its own LRB.

## What stays the same

- Peer protocol: no changes. Pulls use existing pull_tx_server mechanism
- Transaction persistence: all received txs are saved to txstore by txinput_queue regardless
  of sync state
- Branch commit logic: unchanged
- Snapshot restore: unchanged (handles the case of very large gaps)
