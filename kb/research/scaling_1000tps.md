# Scaling assessment: 1000 TPS

Date: 2026-09-03.

Context: follow-up to `claude/attachment_time.md` §"2026-04-27 evening:
scaling assessment for 100 TPS / 8 seq". That document asked whether
100 TPS / 8 sequencers is reachable on 32 GB / 7c bare metal. This one
asks the next question: **is 1000 TPS theoretically possible on
Proxima, and what binds first?**

Method: static reading of `develop` at `534f592` plus linear
extrapolation from the measured 50 TPS / 4 seq baseline in
`attachment_time.md`. No new load test was run — every projected
number below is derived, not observed, and is marked as such.

---

## Verdict

**Yes.** No ledger constant forbids 1000 TPS, and the binding constant
(`constAttachmentCostBudget`) currently runs at ~5 % utilization under
load. The reachable claim is:

> 1000 TPS with single-digit sequencers on 64–128 GB nodes, once
> consensus-level past-cone work is bounded and three O(N) locks are
> removed.

The qualifier is not boilerplate. One cost term is architectural and
does not shrink with engineering (§3); the rest is engineering (§4).

---

## 1. Ledger constants: headroom

| Constant | Value | Location | Binds at 1000 TPS? |
|---|---|---|---|
| `constAttachmentCostBudget` | 550 (`numInputs + numProducedOutputs`) | `ledger/def_constants0.go` `defaultAttachmentCostBudget` | No — measured usage 20–32 |
| `TransactionPaceSequencerTicks` | 2 ticks = 160 ms → 6.25 seq tx/s per chain | `ledger/def_constants0.go` | No |
| `TransactionPaceTicks` | 12 ticks = 960 ms per UTXO chain | `ledger/def_constants0.go` | No — but see demand note |
| Tick / slot | 80 ms, 128 ticks = 10.24 s | `ledger/def_constants0.go`, `base.TicksPerSlot` | Indirectly — see §3 |
| tag-along sub-budget | 2/3 of full budget (`global.Fraction23`) | `sequencer/task/proposal.go` | No |
| `MaxTransactionSize` | 65,536 B | `ledger/transaction/parse.go` | No |
| max endorsements | 8 | `constMaxNumberOfEndorsements` | Amplifies §3 |
| `BuildBudget` | 2 s wall clock | `sequencer/task/task.go` | **Possibly — see below** |

Arithmetic. Tag-alongs get 2/3 × 550 ≈ 366 cost units per sequencer
transaction. A simple user tx costs ~4 (2 in + 2 out) plus 1 for the
tag-along input on the sequencer tx, so ~5 units → ~73 user txs per
milestone. Even at a conservative milestone rate well below the 6.25/s
pace ceiling, one well-fed sequencer carries **150–200 TPS**. 1000 TPS
is 5–8 sequencers, not hundreds.

Measured utilization contradicts nothing here: `attachment_time.md`
records average attachment cost per attachment of 20–32 against a
budget of 550. **The budget is at ~5 %.** The limiter today is not the
constant; it is how much the builder can assemble inside `BuildBudget
= 2 s`, which is what the AIMD `budgetLevel` (`sequencer/sequencer.go`:
`maxBudgetLevel = 6`, `budgetCutOnFailure = 3`,
`budgetIncreaseOnSuccess = 1`) backs off against.

Note on that feedback loop: it measures the **proposer's own** wall
clock. A sequencer on fast hardware receives no back-pressure signal
from slow verifiers. `constAttachmentCostBudget` is the only
verifier-side protection and it is a per-transaction constant, not a
rate limit. At 1000 TPS this asymmetry becomes worth revisiting.

Demand side: `TransactionPaceTicks = 12` caps a single UTXO chain at
~1 tx/s, so 1000 TPS requires ~4,000 concurrent independent senders
(217 senders produced ~50 TPS). Wide parallelism is the design
premise, so this is a load-generation problem, not a protocol one.

---

## 2. Extrapolation from the 50 TPS / 4 seq baseline

Baseline is measured (`attachment_time.md`, 2026-04-27, 217 senders,
4 sequencers, 8 nodes). The ×20 column is **linear extrapolation —
direction, not prediction**.

| Metric | 50 TPS / 4 seq (measured) | ×20 (derived) |
|---|---|---|
| TPS received (gossip) | 80–100/s | 1,600–2,000/s |
| `branch_tx_count` rate | 45–48/s | ~900–1,000/s |
| network in/out per node | ~1 Mbps | ~20 Mbps |
| memDAG vertices | 4.1k seq / 2.7k access | 55k–82k |
| RSS per process | 1.5–1.9 GB | 8–20 GB |
| goroutines (access) | 494–909 | 10k–18k |
| API fan-in | ~650/s total | ~13k/s |
| txs per branch per slot | ~500 | ~10,200 |
| mutations per branch commit | ~2k | ~40k |

Hardware is again not the wall. 20 Mbps per node is trivial; 64–128 GB
boxes are ordinary. Two observations worth keeping:

- RSS extrapolates pessimistically. At 50 TPS most of the 1.5–1.9 GB is
  base cost (Badger, state caches) with only 4.1k vertices, so the
  vertex-proportional slice is smaller than linear scaling implies.
  8–12 GB is the more likely figure; 20 GB is the pessimistic bound.
- ~20 Mbps/node is roughly where Kaspa's published node minimum sits
  (5 MB/s ≈ 40 Mbit/s, which it requires at *any* TPS because its floor
  is set by 10 BPS). Proxima's per-node bandwidth advantage therefore
  persists to ~1000 TPS and disappears somewhere past it. Useful
  framing for external comparison: the advantage is real and it is
  bounded, and stating the bound is more credible than not.

---

## 3. The architectural term: consensus work is per-transaction

This is the part that engineering does not remove.

In a block-DAG (Kaspa), consensus objects are blocks and their rate is
a protocol constant — GHOSTDAG orders 10 objects/s whether each carries
1 transaction or 300. Consensus work is O(BPS), independent of TPS,
until block mass saturates.

In Proxima, consensus objects are transactions. Coverage computation
and conflict detection traverse a past cone whose size is
**O(TPS × slot duration)**:

- at 50 TPS a slot's cone is ~500 vertices;
- at 1000 TPS it is ~10,200 vertices.

Risk #4 in `attachment_time.md` then multiplies it: up to 8
endorsements per milestone, each an `O(merged-pastcone size)`
`MergePastCone` in `attachVertexNonBranch`. At 1000 TPS that is on the
order of **80k vertex-merges per milestone, per sequencer, per
target**. Sequencer nodes carry this; access nodes carry the
solidification half of it.

This is the price of making transactions the consensus objects, and it
is the exact mirror of the advantage Proxima holds at 100 TPS, where
Kaspa pays a fixed 10 BPS tax and Proxima pays almost nothing. It
should be stated plainly rather than left for a reviewer to find.

**The knob that trades it** is `TicksPerSlot × TickDuration` (128 ×
80 ms). A shorter slot cuts cone size per branch proportionally and
raises branch-commit frequency by the same factor — which is Kaspa's
BPS tradeoff reappearing inside Proxima's design, arrived at from the
other direction. At 1000 TPS the choice becomes live: 10.24 s slots
with ~10k-vertex cones, or ~5 s slots with ~5k-vertex cones and double
the commit rate. Deciding this is a *whitepaper*-level question, not a
tuning question, because slot duration is baked into ledger time.

Mitigations that do not require changing the slot: past-cone deltas
(the `FlagPastConeDirectCost` machinery already tracks incremental
cost, so incremental *coverage* is plausibly the same shape), and
capping effective endorsements below 8 under load.

---

## 4. Implementation blockers, ranked

Risk #1 from the 100 TPS assessment (`Readable.GetUTXO` exclusive-lock
contention, 92 % of mutex contention in the 2026-03-15 profile) looks
**retired** — see §5. What remains, in the order it bites:

### 4.1 memdag GC Phase 1 is O(vertices) under the global write lock

`core/memdag/memdag.go` — `doGC` iterates `for txid, rec := range
d.vertices` inside `WithGlobalWriteLock`. Cost per pass scales with
vertex count; vertex count scales with TPS. At 4k vertices this is the
"few ms" the earlier assessment records. At 55k–82k it is tens to
hundreds of ms **blocking every attacher's memdag access**, and
`gcLogSlowThreshold = 100 ms` already exists because this has been
watched before.

This is the worst coupling in the system: the cost of the pass grows
with the load the pass exists to manage. Chunked GC, or
collect-candidates-under-RLock with a write lock only for the nullify
phase, is not optional at 1000 TPS.

### 4.2 Branch commit volume and the `committing` stall

~40k mutations per branch per slot (derived). Every attacher waiting on
`branches.committing[branchID]` stalls for the whole commit duration —
Risk #7, still present, and now 20× longer.

`_commitPendingBranchUnlocked` still runs `PrunableTxIDsAtSlot` per
commit. The TODO item "scan-and-prune txID records periodically, not
every commit" (proposed N = 30 slots, deterministic schedule) stops
being an optimization and becomes a **precondition**. It is a breaking
change requiring coordinated upgrade, so it should be decided early
rather than discovered late.

### 4.3 memDAG residency is fixed in time, so vertices scale with TPS

`vertexTTLSlots = 24` (wall clock, ~4 min) and `vertexLedgerTTLSlots =
12` (`core/memdag/memdag.go`). Because the window is temporal, resident
vertices grow linearly with TPS: 55k–82k at 1000 TPS on the
12-slot bound, up to ~245k on the 24-slot bound if the wall-clock TTL
is what binds. Either the window shortens under load or `WrappedTx` +
past-cone flag storage gets leaner. This is the memory-ceiling term.

### 4.4 `Branches.mutex` and unbounded L2 caches

- `Branches.mutex` (Risk #2) is a single mutex over `m`,
  `stateReaders`, `pending`, `committing` and the 5 s cleanup loop,
  on a path every attacher takes. `sync.Map` for `stateReaders` plus
  snapshot-then-walk for `BranchKnowsTransaction` is the known fix.
- `Readable.txCache` / `Readable.utxoCache` (`ledger/multistate/
  state.go`) are **plain maps with no eviction anywhere** — bounded
  only by the reader's `stateReaderTTLSlots = 2` lifetime. Footprint is
  TPS × TTL × live readers. At 50 TPS this is invisible; at 1000 TPS
  it is the term that converts a throughput spike into an OOM rather
  than a slowdown, which forward sync has already demonstrated once
  (`claude/forward_sync_oom.md`).
- `stateReaderCacheMaxSize = 100` (`branches.go`): a hard cliff. Past
  ~100 live branches every eviction is a cold trie walk. Bounded by
  economics (how many sequencers exist), not by protocol.

### 4.5 Goroutine-per-transaction patterns

Risk #8: `txInputQueue` spawns a goroutine per "future" transaction to
wait for clock alignment. Fine at 50 TPS; at 1000 TPS with branches
stacking at slot edges this wants a timer wheel. Projected access-node
goroutine count 10k–18k is survivable for Go but is a symptom worth
removing. (One attacher goroutine per sequencer transaction is *not* in
this category — that one is correct and bounded by sequencer count.)

### 4.6 API fan-in

~13k/s projected across access nodes. Needs more access nodes, batching,
or both. Not architectural.

---

## 5. What changed since the 100 TPS assessment

| Item | Status |
|---|---|
| Risk #1 mitigation A — unitrie node cache never populated | **Fixed upstream.** `f2f37a1` bumps unitrie (iteratePrefix short-circuit + LRU node cache). The dead-cache finding was confirmed. |
| Risk #1 mitigation B — `RLock` on cache hit | **Landed.** `74a46bc` "multistate: L2 UTXO cache on Readable to parallelize GetUTXO reads". Hits take `RLock`, only misses take the exclusive trie lock. |
| `PrunableTxIDsAtSlot` cold-cache I/O | **Mitigated.** `9c2717a` routes it through the cached state reader. Cadence fix still outstanding — see 4.2. |
| Risk #2 `Branches.mutex` | Open. Commit-outside-lock landed; the mutex itself is still single. |
| Risk #3 memdag global GC lock | Open — now promoted to blocker #1 at 1000 TPS. |
| Risk #5 `stateReaderCacheMaxSize = 100` | Open. Raising to 200–300 was gated on Risk #1, which is now done. |

Net: the contention profile that dominated in March is gone, which is
why the scaling variable has moved from *request rate* to *cache miss
rate*, and why the next walls are the two remaining global locks rather
than the state reader.

---

## 6. Suggested order of work

1. **Chunked memdag GC** (4.1). Highest ratio of impact to effort, and
   it is the term that degrades fastest with TPS. Self-contained.
2. **Decide the `PrunableTxIDsAtSlot` cadence change** (4.2). Breaking,
   coordinated upgrade — decide early even if it lands late.
3. **Bound the L2 caches** (4.4). Small change; converts an OOM mode
   into a latency mode. Do it before any 1000 TPS load test, not after.
4. **Reproduce 200–300 TPS / 4 seq** on the split topology first. The
   100 TPS / 8 seq run was never reported as executed; 1000 TPS is not
   the next experiment. Two doublings from a measured point beat one
   twenty-fold extrapolation.
5. **`Branches.mutex` sharding** (4.4) if 4.1–4.3 do not deliver.
6. **Past-cone delta coverage** (§3) — research task, not engineering.
   This is the one that decides whether 1000 TPS is a ceiling or a
   waypoint.

---

## 7. What to measure

The scaling variable is no longer TPS or sequencer count. Instrument:

- **`Readable` L2 hit/miss ratio**, and the ratio of exclusive to
  shared acquisitions on `Readable.mutex`. This is the single number
  that says whether cost is O(TPS) or degrading toward serialized.
- **`doGC` `sec1Dur`** distribution (already collected in `gcStats`)
  against memDAG vertex count — plot the coupling directly.
- **Branch commit wall time** vs mutations per commit, and the time
  attachers spend blocked on `committing[branchID]`.
- **`proxima_past_cone_size`** against TPS — the empirical curve for §3.
- L2 cache entry counts per live `Readable`, for the memory bound.

Existing Prometheus metrics cover the last two; the first three need
new counters.

---

## Open questions

- Does the AIMD `budgetLevel` need a verifier-side signal, or is
  `constAttachmentCostBudget` sufficient protection at 1000 TPS?
- Is slot duration a tuning parameter or a frozen ledger constant? §3
  turns it into a live design choice at high TPS.
- Is incremental coverage computation over past-cone deltas feasible
  with the existing `FlagPastConeDirectCost` machinery, or does
  coverage fundamentally require full traversal?
