# Fix: detach/reattach race + downstream cleanup

Span: 2026-04-24 — 2026-04-25. Covers the original FATAL race, the
metric/observability cleanup it pulled in, and the memory-leak
investigation that turned out to be the same race in disguise.

## Original problem

Under load, all 4 sequencer nodes died with a FATAL at
`core/attacher/attacher.go:322`:

```
Assertf(a.allInputsDefined(v), "a.allInputsDefined(v)")
```

The attacher's `PastConeBase` cached a compound assertion in
`FlagPastConeVertexInputsSolid`: *every `v.Inputs[i] != nil` AND every
input vid is `IsKnownDefined` in this attacher's past cone.* Part (a)
is **vertex state** that mutates externally:

- GC's `ConvertToDetached` →
  `Vertex.UnReferenceDependencies` → `clear(v.Inputs)`.
- `ReattachVertexNoLock` → `vid._put(_vertex{NewVertex(tx)})` —
  fresh `Vertex{Inputs: make([]*WrappedTx, N)}`, all nil.

When an attacher re-entered `attachVertexUnwrapped` on a vid whose
`Vertex` had been detached+reattached under it, the cached flag said
"solid" but `v.Inputs` was empty → assertion fired → FATAL.

Same shape later surfaced in `CheckFinalPastCone` via
`v.NumMissingInputs()` — different read site, same race.

## Fix shape: don't cache assertions about external mutable state

User-stated principle: **caches cause too much trouble too often.**
When a flag/value encodes "property P holds for an externally-owned
object", the cache goes stale the moment that object mutates. Drop the
cache; recompute from current reality at each read.

Three commits implemented this. They went out together; the testnet
returned to healthy steady state immediately.

### Commit `85c95d10` — remove `FlagPastConeVertexInputsSolid` / `FlagPastConeVertexEndorsementsSolid`

Files: `core/vertex/past_cone.go`, `core/vertex/past_cone_test.go`,
`core/attacher/attacher.go`, `core/attacher/attacher_milestone.go`.

Delete the two flag constants. Replace every read with a fresh call
to `a.allInputsDefined(v)` / `a.allEndorsementsDefined(v)` against the
current `*Vertex`. The two `Assertf` checks at attacher.go:303 and
:322 become structurally impossible: nothing ever assumes
`v.Inputs`/`v.Endorsements` is populated without looking.

Cost per check: O(inputs+endorsements), bounded small (≤256/≤8). Net
−24 lines, no new state.

### Commit `b3531582` — remove redundant `NumMissingInputs` walk in `CheckFinalPastCone`

File: `core/vertex/past_cone.go`.

`CheckFinalPastCone` walked `pc.vertices` and additionally
`Unwrap`-ed each vid as a `Vertex` to call `v.NumMissingInputs()` —
the same external-state read, same race, different consumer. Killed
the loc0-acc, seq1, loc1, loc1-acc nodes minutes after redeploying
the first fix.

The past cone's own `FlagPastConeVertexDefined`, verified by
`checkFinalFlags`, is the authoritative bookkeeping for "all
dependencies present". The Vertex-state walk added nothing but a
race. Drop it.

### Commit `a440c700` — sequencer deadlock watchdog per-tick

File: `sequencer/sequencer.go`, `sequencer/strategy_async.go`.

Boot seq tripped its own deadlock watchdog at 30 s and graceful-shut
itself down. Was a false positive: under sustained load, the throttle
/ pending-awaiting gates can let `doSequencerSlot` span multiple
ledger slots before the branch-zone exit fires. The watchdog was fed
from `sequencerLoop` *after* `doSequencerSlot` returned (per-slot
cadence); when one call ran 30+ s the watchdog killed it even though
the per-tick body was alive and submitting txs.

Move the `Check` inside the per-tick for-loop. Threshold now reflects
loop liveness, not slot completion. Cancel around intentional
pre-loop waits (snapshot pause, clock catch-up).

Similar pattern flagged in `core/attacher/attacher_milestone.go:209`
`lazyRepeat` watchdog: cadence is correct (per-iteration), but the
callback is `Fatalf` instead of `GracefulShutdown`. Not changed —
worth aligning later.

## Memory follow-on (same root cause)

After the FATAL fix, `proxima_pipeline_size` and
`proxima_memDAG_numVerticesGauge` were observed to grow steadily —
2.5–12 k vertices per hour on seq nodes, 22–227 MB/h memory growth.
`[memdag GC] detached: N, deleted: 0` consistently — tombstones never
clearing. Looked like a leak.

### Diagnosis path (with one wrong turn)

Heap profile (post-`gc=1`) on boot:
- 250 MB Badger memtable arenas (compaction equilibrium, not a leak)
- 100 MB ristretto block+index cache (capped at 96 MB)
- ~80 MB easyfl tuples (parsed transactions in memdag entries)
- 16 MB `branches.knowsTxCache`
- ~10 MB consumed-set / WrappedTx structs

Two real, small leaks were fixed in commit `9a4c624f`:

- `branches.knowsTxCache` was an unbounded
  `map[knowsTxKey]bool` populated on every cache-miss
  `BranchKnowsTransaction`, never pruned. **It was a redundant L4** —
  it sat above `Readable.txCache` (in
  `ledger/multistate/state.go`), which already caches the same
  per-reader `(txid → exists)` result. The "avoids contention on
  `Readable.mutex`" comment was wrong: `_lookupTxRecord` uses RLock
  on cache hits.
- `txstore_writer.evictOrder` slice grew unboundedly because
  `TakeCachedTx` removes from `cache` but not from `evictOrder`, and
  the eviction-by-target path only runs when
  `len(cache) ≥ maxCacheSize` (rare under load). Added compaction
  when the slice grows past `2 * len(cache)`.

### The wrong turn

Investigated `WrappedTx.consumed map[byte]set.Set[*WrappedTx]` as
the leak vector — it has no `RemoveConsumer`, so consumers stay
strongly-referenced from their parents forever. Proposed adding
`RemoveConsumer` and calling it on detach.

User pushback (correct): **`consumed` is a back-pointer in DAG sense
(older → newer); it cannot interfere with detachment-based GC because
detach proceeds oldest-first.** When the oldest detached vid is freed
by Go GC, its `consumed` map drops with it, releasing its newer
children. The chain unwinds in waves.

Ran the numbers post-deploy and confirmed the user was right.

### What the post-deploy data shows (boot + 3 other seq nodes, 16 min uptime)

- `[memdag GC] deleted: N` is now consistently **non-zero** (30–78
  per 5 s cycle). Tombstones are clearing. Go GC is reclaiming
  `*WrappedTx` structs.
- `pipeline_size` stable around 3000–3300 (sawtooth around 400–500 MB
  alloc).
- All 4 seq nodes converge to ~2100 vertices, 300–400 MB after 15 min
  ramp-up.

Before (pre-redeploy): vertex count climbing from 10 k toward 30 k+
over hours, memory growing 22–227 MB/h, `deleted: 0` consistently.

### Why memory was "leaking" pre-deploy

Three compounding causes, none of which required structural changes
to `consumed` or past-cone handling:

1. **The FATAL race itself**: 4 nodes were dying; the survivors
   carried the load and accumulated state. Past cones from dead
   attachers lingered. Once nodes ran cleanly the chain unwound.
2. **Unbounded ancillary maps**: `knowsTxCache` and `evictOrder` —
   small per-step contributions that compounded over hours.
3. **Sequencer deadlock false-positive**: triggered graceful
   shutdowns, restarting nodes wiped their memdag — masked steady
   state. Once the watchdog stopped tripping, the actual steady state
   became visible.

The architecture is correct. After the four commits the testnet
operates in bounded steady state under sustained load.

## Architecture review: state-reader caching layers

After deleting `knowsTxCache` the layers are:

| Layer | Location | Caches | Eviction |
|---|---|---|---|
| L1 trie node cache | `*immutable.TrieReader` (inside Readable) | trie nodes by hash | `clearCacheAtSize` (3000 for cached readers, 0 for one-shot) |
| L2 `txCache` | `*multistate.Readable` (`state.go:35`) | `txid → {exists, unspent set256}` | none internal — bounded by Readable lifetime |
| L3 `stateReaders` | `*Branches` (`branches.go:52`) | `*cachedStateReader` per branchID | TTL = 2 slots, hard cap = 100 readers |
| `m` (BranchData) | `*Branches` | `BranchData` per branchID | TTL = 12 slots |
| `pending` | `*Branches` | uncommitted commits | removed on commit / TTL'd |

Each layer caches a distinct abstraction; nothing redundant.

Two intentional bypasses (correct as-is):

1. `GetVirtualStateReaderForTheBranch`
   (`virtual_state_reader.go:188`) — creates fresh `*Readable` per
   call to avoid `Readable.mutex` contention between concurrent
   proposers; ephemeral.
2. `_commitPendingBranchUnlocked` (`branches.go:387`) — fresh
   `baselineReader` for the upgrade-UTXO injection step; ephemeral.

## Observability changes (commit `837dfbfa`)

`proxima_pipeline_size` was being fed by memDAG's stats loop using
just `nVertices + Counter("wait")` — missing
`txSolicitQueue.Len()` and `txStoreWriter.CacheSize()`. The
`Workflow.PipelineSize()` function used by `/api/v1/node_info` and
dagviz already had the correct sum.

Moved the gauge to `Workflow`, fed from a `workflow-stats` 10s loop
calling `w.PipelineSize()`. Same source of truth for Prometheus,
dagviz, and the `[memstats]` log line — they all agree now.

Definition of "tombstone" (added during this work — useful term):
A map entry in `MemDAG.vertices` where `rec.WrappedTx == nil` (set
during GC after `ConvertToDetached`) but `rec.Pointer` (weak) still
resolves because some external code (almost always a published
past-cone clone on a Good seq vid) still holds the `WrappedTx`. The
entry is kept so `GetVertex` returns the same `*WrappedTx` pointer
across re-lookups instead of fabricating a duplicate. The entry is
deleted on the next GC pass after Go GC reclaims the struct.

## Commits in chronological order

| commit | scope |
|---|---|
| `85c95d10` | remove FlagPastConeVertexInputsSolid/EndorsementsSolid; on-the-fly checks |
| `a440c700` | per-tick sequencer deadlock watchdog |
| `b3531582` | drop NumMissingInputs walk in CheckFinalPastCone |
| `837dfbfa` | feed proxima_pipeline_size from Workflow.PipelineSize() |
| `9a4c624f` | delete redundant knowsTxCache (L4); bound txstore evictOrder |

## Open items (not blockers)

- `lazyRepeat` watchdog in `attacher_milestone.go:209` uses `Fatalf`
  rather than `GracefulShutdown`. Worth aligning with the sequencer
  watchdog.
- Pre-existing `ledger/tests` test-order interactions
  (`TestChain1/2/3`, `TestChainLock`, `TestFrozenCoverage1`,
  `TestDelegationLockConsume`) — fail in batch, pass in isolation.
- Defensive cap on `Readable.txCache` is theoretically possible but
  not needed: TTL of 2 slots keeps it bounded in practice.

## Lessons (worth keeping)

1. **Don't cache assertions about external mutable state.** If a flag
   says "property P holds for object X" and X is owned by other code,
   the flag goes stale on X mutation. Drop the cache; recompute.
2. **`consumed` back-pointers don't block GC.** They go from older to
   newer. Detach proceeds oldest-first; when the oldest is freed by
   Go GC, its `consumed` map drops with it, freeing newer children in
   waves. No `RemoveConsumer` needed.
3. **Watchdog cadence must match loop liveness, not coarse cycles.**
   When a loop body covers variable amounts of work (e.g., a
   sequencer slot that may span multiple ledger slots under load), a
   per-cycle watchdog mis-fires. Feed it per tick, cancel around
   intentional waits.
4. **Distinguish "growing under load" from "leaking".** Tombstones
   that don't clear during a clearly-growing test may just be waiting
   on a chain that hasn't unwound yet. `[memdag GC] deleted: 0` is
   suspicious but not conclusive — check whether the system reaches
   steady state.
5. **Heap profiles after `?gc=1`** are the trustworthy snapshot.
   Anything still in-use after a forced GC is genuinely held;
   short-lived allocations vanish.
