# Forward-Sync OOM on Access Nodes

## Incident: loc0-acc crash 2025-03-23

### Timeline (from proxima.log)

```
14:26 → Start forward-sync from slot 74515, memory ~510 MB, vertices ~2900
14:57 → Slot 80984, ~6,470 branches committed in 32 min (~3.4/sec), vertices: 34,272, memory: 709 MB
14:57:48 → vertices: 34,472, memory: 1,392 MB   ← GC starts losing
14:57:58 → vertices: 34,745, memory: 2,817 MB   ← GC counter stuck at 464-466
14:58:08 → vertices: 34,915, memory: 3,556 MB
14:58:18 → vertices: 35,184, memory: 4,357 MB
14:58:28 → vertices: 35,274, memory: 6,003 MB   ← OOM, log stops
```

### Key Observation

Vertex count grows only ~1000 in 50 seconds (34,272 → 35,274). Memory grows 8x in the same
window (709 MB → 6 GB). The problem is NOT vertex accumulation — it is allocation rate
exceeding GC's ability to reclaim during rapid branch commits.

## Root Cause Analysis

### 1. No rate limiting in forward-sync commit loop

`sync.go` processes all committed branches in a tight loop:
```go
for len(s.branchList) > 0 {
    s.ForceCommitBranch(s.branchList[0])
    if _, ok := multistate.FetchRootRecord(...) {
        s.branchList = s.branchList[1:]
        nCommitted++
    } else {
        break
    }
}
```
No pauses, no GC triggers, no memory checks between iterations.

### 2. Heavy per-branch allocation not freed until GC

Each branch commit allocates:
- `*multistate.Mutations` — full mutation set for the branch
- `[]base.TransactionID` (CommittedTxs) — every txID in the branch's past cone
- State reader with trie cache references
- Trie batch writes to BadgerDB

These objects are freed only after the branch is committed AND Go GC collects them. With
3.4 commits/second, the allocation rate overwhelms GC.

### 3. GC death spiral

GC counter freezes at 466 while memory climbs from 2.8 GB to 6 GB. This is the classic
Go GC death spiral: allocation rate exceeds collection rate → heap grows → GC takes longer
→ more allocations pile up → OOM.

### 4. Vertex TTL is wall-clock-based, not commit-based

MemDAG GC evicts vertices older than 24 wall-clock slots (~4 minutes). During forward-sync,
the node processes hundreds of historical slots per wall-clock slot. Vertices from recently
committed old-slot branches are still "fresh" relative to wall-clock time and won't be evicted.

### 5. State reader cache accumulation

`stateReaderCacheLimit = 3000`. During rapid forward-sync, many state readers are created
for branch commits. Each holds trie cache references that prevent deep GC.

## Implemented Fixes (2026-03-24)

All proposed measures (A–G) have been implemented, plus a programmatic memory limit
with graceful shutdown.

### A+B+D. Batched commit loop with GC trigger

**File:** `core/core_modules/forward_sync/sync.go`

The unbounded commit loop is now capped to `sync.commit_batch` branches per tick (default 10).
After each batch, `runtime.GC()` forces collection of the heavy per-branch allocations before
the next tick. The 1-second tick period provides natural rate limiting.

### C. Programmatic GOMEMLIMIT via config

**File:** `node/node.go`

`debug.SetMemoryLimit()` is called at startup when `memory.limit_mb > 0`. This is the
programmatic equivalent of `GOMEMLIMIT` and is driven from `proxima.yaml`.

### E. Pending branch accumulation bounded

The batch limit in A+B+D naturally bounds pending accumulation: at most `commit_batch`
branches are committed per second, and `pull_ahead` (default 5) limits in-flight pulls.

### F. Eager free of branch commit data

**File:** `core/core_modules/branches/branches.go`

After the DB commit completes and the pending entry is removed from the map,
`pb.Mutations` and `pb.CommittedTxs` are set to nil. This allows GC to reclaim
the heavy allocations immediately rather than waiting for map eviction.

### G. State reader cache hard cap

**File:** `core/core_modules/branches/branches.go`

`_cleanupCachedStateReaders()` now enforces a hard cap of 100 cached state readers.
When exceeded, the oldest entries (by `lastActivity`) are evicted regardless of TTL.

### Memory watchdog with graceful shutdown

**File:** `node/node.go`, `global/global.go`

When `memory.limit_mb` is configured, a background watchdog (every 5s) monitors
`runtime.MemStats.Alloc`. It warns at 80% of the limit and initiates graceful shutdown
(`p.Stop()`) at `memory.shutdown_pct` (default 90%).

`MemoryPressureGC()` on `global.Global` (available to all components via `StartStop` interface):
forces GC at 50% of limit, pauses 500ms if still above 70% after GC.

### Ledger-time-based memDAG GC

**File:** `core/memdag/memdag.go`

The memDAG GC now uses two eviction criteria (either triggers eviction):

1. **Wall-clock TTL** (`vertexTTLSlots = 24`): vertex was added more than 24 wall-clock slots
   ago. Only active when synced — same as before.
2. **Ledger-time TTL** (`vertexLedgerTTLSlots = 48`): vertex's transaction slot is more than
   48 slots behind the latest committed branch. Always active.

During forward-sync, the node commits historical branches (e.g., slot 75000) while the
wall clock is at slot 94000. Previously, all vertices from committed branches looked "fresh"
to the wall-clock TTL and were never evicted — vertex count grew unboundedly (4K → 100K+)
causing OOM. The ledger-time TTL evicts these ancient vertices within seconds of their
branch being committed, keeping vertex count stable (~4-5K) during sync.

## Configuration Reference

### `sync` section

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `sync.sources` | string list | `[]` | Trusted API endpoints for branch-list requests. Self URLs are auto-skipped. |
| `sync.threshold_up` | int | `10` | Start forward-sync when slot gap >= this value. Kept within gossip's recursive-pull reach (MaxAttachmentDepthForPull=50) so there is no dead zone. |
| `sync.threshold_down` | int | `4` | Go idle when slot gap <= this value. Must be < threshold_up. |
| `sync.pull_ahead` | int | `5` | Pull the k-th branch ahead to overlap solidification with pulling. |
| `sync.commit_batch` | int | `10` | Max branches to commit per sync tick before forcing GC. Lower values reduce memory spikes at the cost of slower catch-up. |

### `memory` section

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `memory.limit_mb` | int | `0` (disabled) | Soft memory limit in MB. Sets `debug.SetMemoryLimit()` and enables the watchdog. |
| `memory.shutdown_pct` | int | `90` | Graceful shutdown threshold as percentage of `limit_mb`. Watchdog calls `Stop()` when heap allocation reaches this level. Warn at 80%. |

### Example

```yaml
sync:
  sources:
    - "http://113.30.191.219:8001"
  threshold_up: 15
  threshold_down: 3
  pull_ahead: 5
  commit_batch: 10

memory:
  limit_mb: 6000
  shutdown_pct: 90
```

## Interim Conclusions (2026-03-26)

### Testnet Results

4-node testnet (4 cores, 8 GB RAM each), 217 concurrent senders (~20 TPS sustained):

- **3 of 4 sequencer nodes** ran stable for 18+ hours under continuous load
- **loc0 failed** due to deployment config: both nodes on 8 GB machine configured for 6 GB each (12 GB total demand). Fix: set `memory.limit_mb` per node to half available RAM
- **Non-seq oscillation** discovered and fixed: batched txstore writer's write-behind buffer was invisible to the pull_tx_server, causing peers to miss buffered txs

### Protection Layers Implemented

| Layer | Mechanism | Scope |
|-------|-----------|-------|
| Memory limit | `debug.SetMemoryLimit` + watchdog (80% warn, 90% shutdown) | Node-wide |
| Memory pressure GC | `MemoryPressureGC()` at 50%/70% thresholds, rate-limited 100ms | Any component |
| MemDAG vertex eviction | Wall-clock TTL (24 slots) + ledger-time TTL (48 slots behind latest branch) | Always active |
| Non-seq queue bound | Push-site check (`maxQueueLen=1000`) + vertex limit (500 access / 5000 sequencer) | nonSeqAttach |
| Pulled tx backpressure | Skip txstore lookup when `nonseq_attach_q >= 5000` | Attacher pull |
| Batched txstore writes | Write-behind buffer (100 items / 500ms flush), read-through for pull | txstore_writer |
| Forward-sync pacing | Windowed parallel pull, commit batches, memory-pressure GC between batches | forward_sync |
| State reader cache | Hard cap 100 entries, TTL-based eviction | branches |

### Performance Characteristics (4-core, 8 GB machines)

- **Sequencer node**: ~400-600 MB steady state, ~1.7 GB peak under load
- **Access node**: ~600-1000 MB steady state, ~3 GB peak under load
- **Forward-sync**: ~10 branches/sec with parallel window pull
- **Branch commit**: ~30ms per branch (trie operations)
- **TPS**: ~20 sustained across the testnet

### Known Remaining Issues

1. **Sequencer stall after non-seq flood**: seq1 stopped endorsing others after queue overflow (9978 items). The push-site queue bound should prevent recurrence, but the stall recovery mechanism needs investigation.
2. **ProposerStrategy removed**: sequencer strategy no longer stored on-chain; endorsement count metrics replace strategy metrics.
3. **Access node goroutines**: 700-1000 goroutines vs ~175 for sequencer nodes. Mostly libp2p/quic internals, not a leak. Memory impact is from BadgerDB compaction, not goroutines.

### Next Steps

- Deploy to target hardware (32 GB RAM, 256 GB SSD, 7+ cores) for 100 TPS testing
- Profile with pprof on larger machines to find CPU bottlenecks (constraint evaluation, trie ops)
- Investigate batched txstore write tuning (batch size vs flush delay trade-off)

## Related

- [ratecontrol.md](ratecontrol.md) — general rate control architecture
- [snapshot_optimize.md](snapshot_optimize.md) — snapshot load shedding (also addresses resource contention)
- [sync.md](sync.md) — sync architecture notes
