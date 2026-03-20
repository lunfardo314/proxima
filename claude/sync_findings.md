# Sync Implementation — Findings and Open Issues (2026-03-20)

## What works

- **Sequential branch-by-branch sync** with pull-ahead (k=5) — branches commit in batches, ~5x faster than 1-by-1
- **Event-driven loop** — `NotifyBranchCommitted` wakes sync loop only when target branch commits
- **Force deferred commit** — sync loop calls `ForceCommitBranch` to avoid 10-second deferred commit delay
- **get_branch_list API** — returns branch IDs forward from a given slot
- **Multiple sync sources** with fallback cycling, self-URL filtering
- **Transaction filtering** — txstore-sourced transactions treated as pulled
- **Attacher cap with timestamp relaxation** — prevents deadlocks (older txs pass when at cap)

## Known issues to resolve

### 1. IsSyncing=true at startup conflicts with sequencer startup
- Node starts with `IsSyncing=true` to prevent gossip recursion
- But sequencer takes ~26 seconds to start (precondition wait + loop startup)
- During that time, `frontierSlot=0` drops ALL transactions
- Even after `IsSyncing` clears on first tick (gap<3), the sequencer is still starting
- No branches produced locally for ~30 seconds → gap grows to 5 → sync kicks in again
- Sync sources all return empty (they're at the same slot) → node stuck in sync mode

### 2. IsSyncing flag vs thresholds are inconsistent
- `thresholdUp=5` is too sensitive for normal startup transients
- `thresholdDown=3` — gap naturally oscillates near this during normal operation
- The hysteresis window (3-5) is too narrow
- `minSyncGap=30` was proposed but not yet committed — needs alignment with sequencer behavior

### 3. Sync mode must be coordinated with sequencer
- Sequencer should not start proposing while `IsSyncing=true`
- But currently sequencer starts independently and its startup delay causes sync to re-trigger
- Need to define the startup sequence: sync first → catch up → THEN start sequencer
- Or: don't enter sync mode at all if gap is small (< 30 slots)

### 4. Initial IsSyncing=true is problematic
- With `frontierSlot=0`, it blocks everything until first tick
- Without it, there's a brief window where gossip can trigger recursion on a truly behind node
- Resolution: either set `frontierSlot` to the latest committed slot at startup (not 0),
  or don't start with IsSyncing=true and rely on minSyncGap to prevent entering sync for small gaps

## Design decisions needed

1. **When should sync mode activate?**
   - Only for large gaps (>= 30 slots)? Then small gaps are handled by normal gossip + attacher cap
   - The attacher cap with timestamp relaxation already prevents deadlocks for small gaps

2. **Startup sequence**
   - Option A: Always start with IsSyncing=false, enter sync only if gap >= minSyncGap (e.g., 30)
   - Option B: Start with IsSyncing=true but set frontierSlot to latest committed slot (not 0)
   - Option C: Delay sequencer start until sync completes

3. **Relationship between IsSyncing and sequencer**
   - Should the sequencer check IsSyncing before proposing?
   - Should the sequencer wait for sync to complete before starting?

## Testnet status
Testnet stopped. All 4 machines have the latest binary (commit `80864096`).
The `minSyncGap=30` change was added to sync.go but NOT committed/pushed.

## Files changed in this session
- `core/core_modules/sync/sync.go` — sync module
- `core/core_modules/seq_attach/seq_attach.go` — attacher cap + sync filter
- `core/core_modules/nonseq_attach/nonseq_attach.go` — sync filter
- `core/core_modules/branches/branches.go` — NotifyBranchCommitted
- `core/workflow/access.go` — IsSyncing, SyncFrontierSlot, ForceCommitBranch, MaxConcurrentAttachers
- `core/workflow/config.go` — MaxConcurrentAttachers config option
- `core/workflow/workflow.go` — sync module wiring
- `core/workflow/txinput.go` — txstore-sourced txs treated as pulled
- `api/api.go` — get_branch_list, get_snapshot_info paths and structs
- `api/server/server.go` — get_branch_list handler registration
- `api/server/snapshot_download.go` — get_snapshot_info handler
- `api/client/client.go` — GetBranchList, GetSnapshotInfo client functions
- `core/core_modules/snapshot_restore/snapshot_restore.go` — auto-download snapshot from sync sources
- `proxi/init_cmd/node_config.template` — sync config section
- `tests/attach_test.go`, `tests/test_util.go` — attacher limit override for tests
