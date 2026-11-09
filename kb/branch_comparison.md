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
A(b) = A(p) >> (s - s_p)  +  Δ(b) >> (s - s_p - 1)
```

Every part is halved once per slot between the state it covers and `s`. The delta covers the
state of `p`: it is the amounts of outputs unspent in `p`'s state that the branch's past cone
consumes. Measured at a slot `S >= s`: `A_S(b) = A(b) >> (S - s)`. Coverages are compared only as
`A_S` at one common slot. Comparing deltas, or coverages of different slots, is not a coverage
comparison.

For a normal branch (`s_p = s - 1`) `A` is the stored ledger coverage. Only a branch that skips
slots differs, and a branch on a deep baseline starts near zero.

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

## 4. Ranking of a non-branch transaction

For a transaction `T` at slot `t` on baseline branch `B` at slot `s_B`, D8 gives

```
A(T) = (A(B) >> (t - s_B)) >> 1  +  Δ(T) >> (t - s_B)
```

The extra halving of the inherited part inside a slot is today's `FinalLedgerCoverage`. A
transaction on its own slot's branch (`t = s_B`) keeps its delta in full; in a slot without a
branch every candidate on the previous branch has its delta halved once, alike, so their order is
unchanged. The adjusted score (`FinalScore`, §1.7 of `kb/dag_semantics.md`) takes the same delta
halving.

## 5. Properties

- **A descendant always beats its ancestor:** `A(b) >= A(p) >> (s - s_p)`, strictly with a
  positive delta.
- **A deep fork starts near zero:** a branch on a baseline `k` slots back carries at most
  `supply >> (k - 1)` of fresh delta. At 13:09, `k = 394`.
- **A short gap costs everyone the same:** after a slot without a branch, every candidate of the
  next slot sits on a baseline two slots back; all their deltas are halved once.
- **Health is unchanged:** a branch is healthy by its raw delta against the threshold, as today.
  Health says how much stake took part; `A` says which candidate wins. After a long network halt
  the first branches are healthy by raw delta and win by `A` over the stalled lineage, because
  they extend it.
- **Deterministic:** `A` is a function of stored branch data (delta, slot, predecessor) along the
  branch chain, identical on every node (D1), read from the branches module (D4).
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
   from the cached predecessor's value; a predecessor not in the cache is read from the store (one
   step), so the discount of a deep fork, whose baseline is rarely cached, is not lost.
   `FindLatestReliableBranch` takes the heaviest healthy branch by `A_S`, `S` the newest slot with a
   healthy branch (ties: newer slot, then larger ID), and walks back from it among the healthy
   branches of its slot as before.
3. **DB fallback** (`multistate.FindLatestReliableBranch`, used when the cache holds no healthy
   branch, as at start) and `FetchLatestBranches`: the tips of one slot are compared by absolute
   coverage (one step from the store) instead of delta. The fallback still starts from the latest
   slot with a healthy branch: comparing across slots there would need the absolute coverage of
   every retained branch; left as is, see §8.
4. **Sequencer ranking:** `attacher.RankLedgerCoverage` and `attacher.RankScore` are
   `FinalLedgerCoverage` and `FinalScore` with the delta halved by the baseline's age (§4). v3 ranks
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
- **Restart boundary:** the DB fallback compares only the latest healthy slot; a node starting
  with a deep fork's branch as its newest healthy one takes it until its cache fills. The absolute
  coverage itself is read one step from the store, so it is discounted correctly once compared.
- `kb/sync_semantics.md`: the latest reliable branch section to reference D8 once built.
