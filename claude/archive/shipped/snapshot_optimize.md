# Snapshot Optimization

## Problem

Snapshot generation competes for resources (CPU, memory, disk I/O) with normal transaction processing.
On resource-constrained machines (8GB RAM, 2 nodes per machine), this competition can trigger OOM or
degrade performance enough to cause sync loss, cascading into attacher pile-ups and memory spikes.

See also: [ratecontrol.md](../superseded/ratecontrol.md), [sync.md](../../sync.md)

## Deployment Recommendation

Sequencer and snapshotting should live on different machines. Access nodes generate snapshots;
sequencer nodes consume them. This avoids the worst-case scenario where snapshot I/O starves
the sequencer's proposer loop.

## Phase 1: Load Shedding During Snapshot (implemented 2025-03-23)

### Strategy

During snapshot generation, the node voluntarily sheds load:

1. **Drop non-pulled transactions**: Both `seq_attach` and `nonseq_attach` queues drop all
   non-pulled transactions while `IsSnapshotting()` is true. Pulled transactions always pass
   (needed for solidification of already-running attachers).

2. **Pause sequencer**: The sequencer loop pauses proposing while `IsSnapshotting()`.
   This avoids producing branches with incomplete past cones during degraded operation.

3. **Accept temporary desync**: The node will fall behind during snapshot generation.
   After the snapshot completes, resources are freed and the node resyncs via forward-sync.

### Why this is safe

- All dropped transactions are already persisted in `txstore` (attachment happens after
  `attachTx()` which writes to txstore). Nothing is lost.
- Pulled transactions still pass, so in-flight attachers can complete solidification.
- The forward-sync mechanism handles resync automatically once resources are freed.

### Implementation

**Global flag** (`global/types.go`, `global/global.go`):
- `IsSnapshotting() bool` and `SetSnapshotting(on bool)` added to `StartStop` interface
- Backed by `atomic.Bool` in `Global` struct — zero overhead when not snapshotting

**Snapshot module** (`core/core_modules/snapshot/snapshot.go`):
- `SetSnapshotting(true)` before `SaveSnapshot()`, `SetSnapshotting(false)` after
- Flag is always cleared, even on error (sequential code, no early return between set/clear)

**Attach queue gates** (`seq_attach/seq_attach.go`, `nonseq_attach/nonseq_attach.go`):
- `IsSnapshotting()` checked before other resource gates
- Only non-pulled transactions are dropped
- Uses existing `seq_drop` / `nonseq_drop` counters for monitoring

**Sequencer pause** (`sequencer/sequencer.go`):
- At the top of `doSequencerStep()`, if `IsSnapshotting()`, wait in `RepeatSync` loop
- Logs pause/resume for operational visibility
- Context-aware: respects shutdown signal during wait

## Phase 1: Snapshot Scheduling (implemented 2025-03-23)

### Extended period

Default snapshot period changed from 30 slots (~5 min) to **176 slots (~30 min)**.
Template (`proxi init node`) updated to match.

Rationale: 5-minute snapshots are unnecessarily frequent. The snapshot is a safety net, not
a real-time requirement. 30 minutes provides ample coverage while reducing I/O and resource
contention by ~6x.

### Randomized start

Each node now starts its first snapshot after a random delay of `[0, period)`. Subsequent
snapshots are periodic.

Rationale: Without randomization, all nodes started from similar genesis timestamps tend to
snapshot at roughly the same time, creating correlated resource spikes across the network.
With 4 machines (8 nodes) and 30-minute period, random offsets spread snapshots ~3.75 minutes
apart on average.

Implementation: The snapshot module starts a goroutine that waits for the random delay,
runs the first snapshot, then enters the periodic `RepeatSync` loop. The initial delay
is logged for operational debugging.

### Files changed

| File | Change |
|------|--------|
| `global/types.go` | Added `IsSnapshotting()`/`SetSnapshotting()` to `StartStop` interface |
| `global/global.go` | Implemented with `atomic.Bool` field |
| `core/core_modules/snapshot/snapshot.go` | Set flag during snapshot, randomized start, period 176 slots |
| `core/core_modules/seq_attach/seq_attach.go` | Drop non-pulled when snapshotting |
| `core/core_modules/nonseq_attach/nonseq_attach.go` | Drop non-pulled when snapshotting |
| `sequencer/sequencer.go` | Pause proposing during snapshot |
| `proxi/init_cmd/node_config.template` | Default period 176 slots |

## Phase 2 (future session): Trie Traversal Optimization in unitrie

### Problem

The unitrie iterator does **depth-first traversal with one `FetchNodeData()` per trie node** —
no batching, no prefetching at the trie level. Each fetch = one BadgerDB `Get()`.

### Performance estimates

**Key variables:**
- Trie nodes visited per snapshot ≈ 1.5-3x number of leaf entries (uniform key distribution)
- BadgerDB `Get()` latency: ~1-10 us (RAM-cached), ~100-500 us (SSD)

#### 1M entries, 32GB RAM, 7 cores

| Scenario | Nodes | Per-read | Total |
|----------|-------|----------|-------|
| Hot (trie in OS page cache) | ~2M | ~5 us | ~10 seconds |
| Warm (partial cache) | ~2M | ~50 us | ~100 seconds |
| Cold (SSD) | ~2M | ~200 us | ~7 minutes |

At 1M entries the trie likely fits in 32GB RAM. After first traversal: **10-30 seconds**.

#### 100M entries, 32GB RAM, 7 cores

| Scenario | Nodes | Per-read | Total |
|----------|-------|----------|-------|
| Partial cache | ~200M | ~100 us | ~5.5 hours |
| Cold (SSD) | ~200M | ~300 us | ~17 hours |

At 100M entries, the trie won't fit in RAM. **Unacceptable without optimization.**

### Optimization approaches (to be implemented in unitrie)

**1. BadgerDB prefix iterator** (recommended for snapshot)
- Trie data lives under a known key prefix in BadgerDB
- A single `Iterator` with `PrefetchSize=100` turns random reads into sequential scan
- Potentially 10-50x faster than per-node `Get()`
- Requires unitrie API addition: a "raw KV dump" mode for snapshot

**2. Batch node fetching**
- Prefetch child nodes before descending
- Moderate improvement, works within existing trie structure

**3. Direct BadgerDB stream** (most radical)
- For snapshot: bypass trie structure entirely, iterate all KV pairs under state partition prefix
- Reconstruct trie root commitment separately
- Orders of magnitude faster for large states
- BadgerDB native iteration is optimized for sequential access

**4. Incremental snapshots**
- Track which trie nodes changed since last snapshot, write only delta
- Eliminates scaling problem entirely
- Most complex to implement

### Recommendation

Option 1 (prefix iterator) is the pragmatic first step. It can be added as a new method on
the trie reader without changing the existing API. The snapshot code would use this instead of
`Iterator(nil).Iterate()`.

Option 3 is the ultimate solution for very large states but requires more architectural thought
about how to validate consistency (the root commitment must still be verified).
