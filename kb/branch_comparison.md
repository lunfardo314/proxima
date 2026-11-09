# Comparing branches: absolute coverage at one slot (applying D8)

> Status: BUILT on `fix/old-branch`, not released. Written 2026-11-09 after the rollback of slot
> 187951. Applies the invariant **D8** of `kb/dag_semantics.md` (added the same day, approved by the
> user) to the node's choice of the latest reliable branch and to the sequencers' ranking. Touches
> `kb/sync_semantics.md` (latest reliable branch). No ledger change: stored coverage values and
> every validity rule stay as they are.

## 1. The rule (D8)

The absolute coverage of a branch `b` at its own slot `s`, with predecessor branch `p` at slot
`s_p` and coverage delta `Δ(b)`:

```
A(b) = L(p) >> (s - s_p)  +  Δ(b) >> (s - s_p - 1)
```

`L(p)` is the predecessor's stored ledger coverage. Every part is halved once per slot between the
state it covers and `s`. `A` is computed in one step from stored values and never by walking the
branch chain, so it is the same on every node whatever it holds in memory, and the ledger's own
recurrence `L(b) = L(p) >> K + Δ(b)` stays the only recurrence in the system: no value computed by
summing separately halved deltas along the chain is ever compared with a recurrence value, so the
two cannot disagree by rounding. The delta covers the
state of `p`: it is the amounts of outputs unspent in `p`'s state that the branch's past cone
consumes. Measured at a slot `S >= s`: `A_S(b) = A(b) >> (S - s)`. Coverages are compared only as
`A_S` at one common slot. Comparing deltas, or coverages of different slots, is not a coverage
comparison.

For a normal branch (`s_p = s - 1`) `A` is the stored ledger coverage, to the mote. Only a branch
that skips slots differs, and a branch on a deep baseline starts near zero. The child of a branch
that skipped slots inherits that branch's stored coverage, in which its own delta was not
discounted; this matters only for the second branch of a deep fork (about `L(F) >> 1` more), which
still loses to a healthy lineage that keeps branching (§5).

## 2. What happened at 13:09 (slot 187951), in numbers

- Healthy lineage: branches of 187949, ledger coverage about 142T (delta 71.13T plus half of the
  previous branch). Slot 187950 has no branch (every candidate refused as unhealthy, the heaviest
  at 103.78T).
- Rollback: 73 branches of 187951 built directly on 187557, stored coverage 69.42T: inherited
  `L(187557) >> 394 = 0`, delta 69.42T (90.7% of supply) against the state of 187557.

| Comparison at slot 187951 | Healthy lineage | Rollback | Winner |
|---|---|---|---|
| Code today: newest slot with a healthy branch | 187949 | 187951 | rollback |
| Halving the inherited part only | 142T >> 2 = 35.5T (the refused 187950 branch: 103.78T >> 1 = 51.9T) | 69.42T | rollback |
| D8 | 35.5T | 69.42T >> 393 = 0 | healthy |

The second row is why D8 halves the delta too: with the delta counted in full, a fork from any
depth whose first branch gathers a large delta beats a lineage that missed one or two branches.

## 3. Where the code breaks D8 today

1. **Latest reliable branch**, the node's only decision between branches
   (`Branches.FindLatestReliableBranch` in `core/core_modules/branches/branches.go`, and the DB
   fallback `multistate.FindLatestReliableBranch` in `ledger/multistate/roots.go`):
   - takes the newest slot holding a healthy branch, whatever the coverage of branches in older
     slots: a comparison of different slots;
   - within that slot picks the heaviest tip by coverage delta: two branches of one slot can sit
     on baselines of different age, and a branch on a deep baseline with delta 69T would beat one
     on the previous branch with delta 60T plus 52T inherited.
2. **Ledger coverage of a transaction for ranking** (`attacher.FinalLedgerCoverage`,
   `attacher.FinalScore`, used by sequencer `v3` through `factory.RankValue`): halves the
   inherited part by the slot difference, adds the delta in full. A proposal on a baseline hundreds
   of slots old ranks as high as one on the previous branch. The v1.6.1p1 gate in the proposal
   finalizer refuses baselines older than the latest reliable branch; this item makes the ranking
   itself obey D8.

Not on the node's decision path: `MemDAG.HeaviestStateForLatestTimeSlot` (tests only) and
`multistate.FetchLatestBranches` / `FetchHeaviestBranchChainNSlotsBack` (`proxi db` tools, test
helpers) take the latest slot first, then the biggest delta. They decide nothing, but should follow
D8 so that tools and tests show what the node decides.

## 4. Ranking of candidates

A sequencer ranks a candidate transaction `T` at slot `t` on baseline branch `B` at slot `s_B` by its
full coverage under D8: the baseline's **absolute** coverage, never its stored one, plus the delta
halved by the baseline's age:

```
rank(T) = (A(B) >> (t - s_B)) >> [T not a branch]  +  Δ(T) >> (t - s_B - [T a branch])
```

The extra halving of the inherited part inside a slot is today's `FinalLedgerCoverage`. A
transaction on its own slot's branch, or a branch on the previous slot's one, keeps its delta in
full; for a normal baseline `A(B) = L(B)`, so the rank is the stored and declared coverage. In a slot
without a branch every candidate on the previous branch has its delta halved once, alike.

Ranking by the stored `L(B)` instead broke the biggest-coverage rule: in the second rehearsal run
(a branch F on a baseline 32 slots back, the only branch of its slot) F's stored coverage was its
full delta, so milestones built on F inherited half of it and ranked level with the healthy lineage;
the majority sequencer continued F and the healthy lineage was rolled back on every node. With
`A(F) ≈ 0` they rank near zero.

The adjusted score (`FinalScore`, §1.7 of `kb/dag_semantics.md`) is discounted the same way: a branch
inherits `A(B)`, any other transaction the baseline's score halved once more per slot the baseline
itself skipped (`s_B` minus its predecessor's slot, minus one; 0 for a normal baseline).

## 5. Properties

- **A descendant always beats its ancestor:** `A(b) >= L(p) >> (s - s_p)`, strictly with a
  positive delta.
- **A deep fork starts near zero:** a branch on a baseline `k` slots back carries at most
  `supply >> (k - 1)` of fresh delta. At 13:09, `k = 394`.
- **A short gap costs everyone the same:** after a slot without a branch, every candidate of the
  next slot sits on a baseline two slots back; all their deltas are halved once.
- **Health is unchanged:** a branch is healthy by its raw delta against the threshold, as today.
  Health says how much stake took part; `A` says which candidate wins. After a long network halt
  the first branches are healthy by raw delta and win by `A` over the stalled lineage, because
  they extend it.
- **Deterministic:** `A` is a function of the stored data of the branch and its predecessor,
  identical on every node (D1), read from stored values (D4).
- **Limit, by design:** D8 stops the first branch of a deep fork. If the stake majority keeps
  building on it anyway, its next branch is normal (delta in full) and wins over a lineage that
  stopped: that is majority consensus, which no node rule should overrule. Honest sequencers do
  not build on it: their ranking (§4) gives it near-zero value, and the v1.6.1p1 gate refuses its
  baseline.

## 6. Changes (built)

1. **`ledger/multistate/absolute_coverage.go`:** `AbsoluteCoverage` (the formula of §1),
   `CoverageAtSlot`, `BranchData.PredecessorBranchID` (from the stem lock) and
   `AbsoluteCoverageFromStore` (one step: the predecessor's stored ledger coverage taken as its
   absolute coverage).
2. **Branches module:** every cached branch carries `absCoverage`, computed when it enters the cache
   from its predecessor's stored ledger coverage, taken from the cache or read from the store; a
   deep fork's baseline is rarely cached, and its discount must not be lost.
   `FindLatestReliableBranch` takes the heaviest healthy branch by `A_S`, `S` the newest slot with a
   healthy branch (ties: newer slot, then larger ID), and walks back from it to the newest branch
   contained in every competing tip. A competing tip is a healthy branch of the heaviest one's slot
   with at least half its absolute coverage: a lighter one cannot win under D8, and counting it
   pulled the reliable branch back to the fork point for a slot on every node, old and new (rehearsal
   of 2026-11-09, a healthy branch on a baseline 32 slots back beside the normal branches of its slot).
3. **DB fallback** (`multistate.FindLatestReliableBranch`, used when the cache holds no healthy
   branch, as at start, and by the forward-sync check of whether the node's latest reliable branch
   is on its sources' lineage): the same selection from stored data. It scans back from the newest
   slot with a healthy branch and stops when no older slot can win (a coverage never exceeds 4x
   supply, halved per slot of distance), normally after two or three slots. `FetchLatestBranches`
   orders the latest slot's branches by absolute coverage. `HeavierBranch` (the comparison with its
   tie-break) is shared by both selections.
4. **Sequencer ranking:** `attacher.RankLedgerCoverage` and `attacher.RankScore` are
   `FinalLedgerCoverage` and `FinalScore` with the baseline's absolute coverage inherited and the
   delta halved by the baseline's age (§4); `Branches.AbsoluteCoverage` gives the baseline's absolute
   coverage and the slots it skipped. v3 ranks
   with them (`factory.RankValue`, the proposal's rank value), v2 too (a rank value on its proposal
   and in its factory). `FinalLedgerCoverage` and `FinalScore` are unchanged: they give the coverage
   and the adjusted score stored on the vertex, the declared coverage delta and the cross-checks,
   so the coverage adjustment (§1.7 of `kb/dag_semantics.md`) computes exactly what it did; only the
   ranking of a candidate on a baseline older than its slot's branch changes.

## 7. Tests

- Unit: `A` along a chain with a one-slot gap, a many-slot gap and a deep fork; descendant beats
  ancestor; the 13:09 numbers keep the healthy lineage.
- Latest reliable branch: healthy branches of slot `s-2` (normal lineage) and of slot `s` (on a
  baseline 300 slots back, delta 90% of supply): the older lineage stays reliable. Two branches of
  one slot on baselines of different age: the one with the larger `A` wins, not the larger delta.
- Integration: a slot without a branch followed by branches on a deep baseline; no switch.
- Halt recovery: no branch for 20 slots, then a branch on the last one; it becomes reliable.

## 8. Open

- **Branch health gate:** a sequencer does not issue a branch whose delta is below the threshold,
  which is why 187950 had none, though its heaviest candidate (103.78T) was the heaviest branch on
  offer. Under D8, missing one branch costs the healthy lineage only one halving, so this spec does
  not depend on removing the gate. Whether the gate should go, with health kept only for finality,
  is a separate decision.
- **Slot 187949:** why it failed to consolidate is still unexplained.
- `kb/sync_semantics.md`: the latest reliable branch section to reference D8 once built.
