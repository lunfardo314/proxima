# Attachment time optimization — 2026-04-26

Context: investigation into what dominates milestone-attachment latency
on every node, with the goal of reducing the 250-650 ms steady-state
attachment time observed across the 8-node testnet.

## Methodology

1. Read existing Prometheus metrics first (`proxima_glb_attachmentDurationMs`
   vs `proxima_tx_validation_time_ns`) to find the proportion of validation
   vs surrounding overhead.
2. Capture pprof CPU + mutex profiles from a representative node under
   load, both on the sequencer (boot) and the access node (boot-acc),
   since the two paths exercise different code (proposer vs
   milestone-attacher only).
3. Identify dominant hot paths, fix the cheap ones first, re-profile to
   see what surfaces next.

## Findings

### EasyFL validation is not the bottleneck

`tx_validation_time_ns / attachmentDurationMs` is **<1%** on every
node. Optimizing constraint scripts is wasted effort. The remaining
~99% lives in plumbing: lock acquisition, past-cone walks, state
lookups, branch registration.

### CPU profile — access node, OLD binary (pre-fix)

- **`util/queue/queue.inputLoop` busy-poll: ~73% of CPU** spread across
  five separate `inputLoop` goroutines (txinput_queue, txlogger,
  events, poker, generic). When the deque had data and the consumer
  was running `consume()`, both non-blocking selects in the loop hit
  `default:` and the goroutine spun. Pure waste, no throughput.
- `PastCone.Lines`: 2.23%, called eagerly on every
  `solidifyPastCone` retry via a `Tracef` whose tag was off — the
  Lines argument was evaluated before `Tracef` could short-circuit.
- `milestoneAttacher.run`: only 7.71% — most of the access node's
  CPU was busy-poll, not real work.

### Mutex profile — access node, OLD binary

- Total mutex wait: **676 s accumulated in 60 s wall** (~11
  goroutines waiting on average).
- 49% of wait was attackers stalled behind `memdag.doGC` holding
  `memdag.mutex`.
- 22% was the milestone-attacher path itself.

### CPU profile — sequencer node (boot), OLD binary

Different picture from the access node — proposer paths dominated:
`PastCone.CoverageDeltaRaw` was **~21% of CPU**, almost entirely
called from `factory.runRound → FinalLedgerCoverage`. State reads
(`unitrie.NodeStore.FetchNodeData` via Badger) were 28.8% of total CPU.

This led to an early misdiagnosis: I thought `CoverageDelta` was the
top milestone-attacher cost on every node. The user correctly pointed
out that proposers run only on sequencers, so the relevant profile for
*milestone-attacher* optimization is the access node where the
proposer is absent.

## Fixes shipped

### 1. `queue.inputLoop` — single blocking select

Commit `3e319683`. Replace the two non-blocking selects + `default:`
with one blocking select on `(<-inCh, outCh<-front)`. Either side
makes progress; neither starves. Same "never jams" property (deque is
unbounded). Close semantics preserved: when `inCh` closes, drain the
remaining elements synchronously if `processRemainingOnClose` is set,
otherwise return.

Bug found and fixed during testing: my drain loop returned without
updating the `q.len` atomic, so `q.Len()` read stale until next push.
Added explicit `updateLen()` at the drain return.

### 2. `PastCone.Lines` — defer eager argument

Commit `d4cf6d88`. Wrapped
`a.pastCone.Lines("     ").Join("\n")` in a `func() string` so
`lazyargs.Eval` defers it until the trace tag is actually enabled.
The codebase already had this pattern at `past_cone.go:822`.

### 3. memdag-GC instrumentation

Commit `d80dee52` (deployed earlier in the day). Per-section timings
+ counters logged when work was done or when either locked section
exceeded 100 ms. Steady-state shows `t1: 0.5-2 ms`, `t2:
100-300 µs`, with rare 5 ms peaks during deletion bursts. **No pass
close to the 30 s deadlock threshold** in normal operation.

## Measured wins

Comparing 5-min average `proxima_glb_attachmentDurationMs` before
and after all today's fixes (queue, Lines, peering Step 5+6,
sequencer-connectedness gate):

| Node          | Before | After | Δ |
|---------------|-------:|------:|---:|
| boot:14000    | 657 ms | 321 ms | **−51%** |
| boot-acc      | 616 ms | 325 ms | **−47%** |
| seq1:14000    | 529 ms | 211 ms | **−60%** |
| loc1:14000    | 266 ms | 114 ms | **−57%** |
| loc1-acc      | 443 ms | 218 ms | **−51%** |
| loc0-acc      | 443 ms | 408 ms | −8% |
| loc0:14000    | 278 ms | 405 ms | +46% (multispammer load — expected) |
| seq1-acc      | 575 ms | 609 ms | +6% |

Mutex contention: total wait dropped **24×** (676 s → 27.9 s in 60 s
wall). doGC fell out of the top contention sources entirely.

CPU utilization on access node: 57% → 15% in steady state — most of
the previous 57% was queue spin, not work.

## Grafana observations (post-deploy ~20:24 CEST)

- **Attachment time**: noise floor went from 1-2 s to 200-400 ms. The
  remaining periodic spikes are evenly spaced (~11 min) and correlate
  with snapshot cadence (64 slots = ~11 min).
- **TPS**: shifted from ~25-27 to ~28-30 with a much cleaner band.
- **Endorsements**: more transactions with 2-3 endorsements,
  proposer finding more useful skeletons.
- **Pipeline sawtooth**: more regular post-deploy. Higher peak,
  cleaner trough, GC catching up between cycles.
- **Memory**: unchanged.

## What's left

After the fixes, the new dominant mutex source is
`Readable.GetUTXO` (state.go:185-197). Every state read takes
`r.mutex.Lock()` exclusively because the recent unitrie LRU
(`container/list`) is not goroutine-safe for cache-position updates
on hits. With many concurrent attachers reading from the same
baseline `Readable`, they queue.

The other identified residual is **snapshot cost**. Every 64 slots
(~11 min) `core_modules/snapshot.doSnapshot` walks the entire state
trie via `iteratePrefix`, reading every leaf through Badger. During
the snapshot window, attachers compete with the snapshotter for
Badger I/O and CPU, producing the 1-2 s attachment-time spikes
visible in Grafana.

## Plan for tomorrow

Order matters: confirm steady-state first, then act on data, not
guesses.

1. **Observe overnight** (currently running). Look for:
   - Whether attachment-time spikes persist or settle further as
     caches warm up.
   - Whether any boot-deadlock pattern recurs (the d80dee52
     instrumentation will catch it).
   - Whether memory steady state holds at ~400-500 MB or drifts.

2. **If steady-state attachment time is acceptable**: stop
   optimizing attachers; pivot to whatever surfaces from
   observation.

3. **If not, options in order of preference**:

   a. **`Readable.GetUTXO` lock contention**. Investigate the
      unitrie LRU to see whether read-side updates can be made
      lock-free or atomic. Possibilities: shard the LRU; use
      `RLock` for hits with atomic LRU updates; switch to a
      clock-style policy that doesn't mutate on read. This is a
      unitrie change, so it touches the dependency.

   b. **Snapshot cost mitigation**. Architectural — incremental
      snapshots (save deltas only), lower-priority I/O during
      snapshot, or run snapshot on a replica. None are trivial.
      Probably not worth it unless the 11-min spikes are causing
      operational problems.

   c. **CoverageDelta memoization**. Real but small (~1.86% on
      access node). Measured in absolute terms, not worth the
      complexity discussed in the original plan thread.

4. **Investigate `loc0` 46% increase**. Once we confirm
   multispammer load is the cause (and not a regression), no
   action needed. If it looks like a regression, dig in.

## Anti-decisions (things I almost did, then didn't)

- **Single-pass walk for CoverageDelta + CheckConflicts**: would
  have saved ~1-2% on milestone attachers. Not worth the
  algorithmic complexity for that gain.
- **Incremental coverage delta tracking inside PastCone**: user
  correctly judged the bookkeeping (register/unregister on every
  consumer add/remove, with merge/clone semantics) as bug-prone.
  The simpler memoization-on-cache alternative I proposed was
  also unnecessary once we determined CoverageDelta is small on
  the milestone-attacher path.
- **Increase trie LRU `stateReaderCacheLimit` (3000 → 30k)**:
  ~300 MB per cached Readable × up to 100 cached. Pushes us
  past the configured memory limit. Skipped in favor of
  attacking the contention point itself.

## Files touched today

- `util/queue/queue.go` — blocking-select inputLoop
- `core/attacher/attacher_milestone.go` — Lines deferral
- `core/memdag/memdag.go` — doGC instrumentation (already
  committed earlier in the day)
- `peering/*` — Step 5 (HB removal) and Step 6 (ConnManager) —
  also contributed to the mutex-contention reduction
- `sequencer/sequencer.go` — `IsConnectedToNetwork()` gate
  (post-HB necessity)
