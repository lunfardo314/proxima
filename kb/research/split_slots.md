# Split slots: classical coverage against the adjusted score

> RESEARCH, open. Investigated 2026-11-10 and 2026-11-11 on take 1, from the raw DAG (hloc0-acc and
> oseq1-acc dag explorer), node logs and the offline scorer `.internal/seq_analysis/score_eval`
> (classical delta reconstructed to the mote for all 46 branches of slot 197125). Nothing built; the
> watch alerts on the signs listed in the last section. Read before changing the branch health check,
> the latest reliable branch, the sequencer's ranking or the coverage definitions.

## 1. What a split slot is

A slot whose branches fall into two coverage groups more than 15 points apart in the classical coverage
delta the `BRANCH COMMIT` line prints. About 1-3% of slots, the same rate before and after library
upgrade 6 and the resumption of mining (2.15% from 2026-11-09 to slot 196000, 1.1% while mining was
paused, 1.2-2.9% after slot 197000). A split lasts one slot.

## 2. Mechanism

1. **A branch race in slot S** is won by one branch. Some sequencers issued late milestones in S that the
   winning branch does not contain.
2. **In S+1 two first moves are possible.** Those sequencers continue from their own late milestone of S,
   bringing the left-out history into their lineage (side A). The rest restart from the outputs committed
   in the winning branch (side B). A sequencer whose late milestone was left out may end up on both: its
   late milestone in A's history, its restart in B's.
3. **The two sides spend the same outputs** (at slot 197124: 27 chain outputs of 197123 spent once on each
   side, the second spend being the same sequencer's restart), so no milestone of one side can endorse one
   of the other for the rest of S+1.
4. **Sequencer `v3` ranks by the adjusted score**, which by design (`dag_semantics.md` §1.7) gives no credit
   for consolidating left-out history late. Side A's classical lead is exactly that history, so by the score
   side B is ahead and the sequencers stay on B; the re-anchor onto A is in the search (`reanchorPair`) but
   ranks lower.
5. **Side B wins.** At S+2 everyone builds on B's winning branch; A is orphaned.

Slot 197125, from the scorer:

| | Branches | Classical coverage | Adjusted score | Classical delta | Adjusted delta |
|---|---|---|---|---|---|
| Side A, orphaned | 8 | 143.68T | 100.78T | 71.2T (93%) | 28.3T (37%) |
| Side B, canonical | 38 | 117.73T | 117.73T | 45.3T (59%) | 45.3T (59%) |

The canonical lineage contains B's `s197125-0-01f3a547f18d`; none of A's branches. The mine transit of the
slot is not involved (first consumed 77 ticks after the sides parted; in other splits on either side or
absent).

## 3. Consequences

**Optics.** The watch, the `BRANCH COMMIT` percentages and the health figure measure classical coverage, so
they show the canonical side as the low group and the orphaned side as the high one.

**Classical coverage counts one stake on two conflicting sides.** A branch's delta counts every baseline-state
output its history spends. A sequencer whose late milestone was left out and which then restarted has spent
its chain output, its whole stake, once in each side's history; the output counts in both deltas. Signing two
spends of one's own output costs nothing (one is orphaned, nothing is slashed) and the reference sequencer
does it on its own. At 197125 the two conflicting sides sum to 152% of supply by classical delta, 96% by
adjusted delta. Hence the 7/12 health threshold is not a proof that a majority of stake backs a branch:
two conflicting branches can both be healthy in one slot.

**Finality holds.** The latest reliable branch is the branch every competing healthy tip of the slot contains,
so in a split it is their common ancestor and neither side is final until the next slot. The competing-tip
rule of D8 (half the heaviest absolute coverage) keeps a healthy side B: its absolute coverage is at least
116.6T against at most 148.5T for A on the same baseline.

**Liveness weakens with conflicts.** The canonical side carries only the stake not counted for the other side.
If that falls under 7/12, the reference sequencer does not issue its branch (the issuance gate checks the
classical delta), the slot has no branch on the side the sequencers chose, and the next slot builds on the
other side's branch or, without any healthy branch, on none. The latest reliable branch lags; after three
slots bootstrap mode starts. A slot without a branch was the opening of the 2026-11-09 rollback, since
closed by the v1.6.1p1 baseline gate and D8. More conflicts mean more lost races, so more and deeper splits.
A quantitative bound, and what a deliberate actor could do with it, is in `.internal` (not public).

**The latest reliable branch can briefly sit on the orphaned side.** D8 compares absolute coverage built on
the stored classical coverage, so during a split it prefers A's branches until a branch on B's lineage
arrives in the next slot. Seen at 197125 and as an inverted split at 202726 (the canonical side the majority,
the latest reliable branch on a 75% branch for about 40 seconds; not DAG-checked).

## 4. Options

1. **Accept** (current). Splits last one slot; safety holds through the walk-back to a common ancestor.
2. **Judge health, and possibly the issuance gate and the latest reliable branch, by the adjusted delta**, the
   measure the sequencers maximise and which does not double-count. Consensus is unchanged; the node's view of
   it follows the sequencers. Amends `dag_semantics.md` §1.7 (which keeps health and the latest reliable branch
   on the classical coverage) and D8.
3. **Rank `v3` by the classical coverage again.** Brings back the late-consolidation bias the score removes.
4. **More flexibility to revert within a slot** (`.internal/sequencer_backlog.md` item 1). Does not address the
   metric disagreement; the 2026-08-11 greedy switch was reverted.

## 5. Open questions

- Whether splits were rarer before the score started deciding (slot 138000, 2026-11-03), which would make the
  metric disagreement their cause rather than one of their features.
- The scorer on more splits, including the inverted 202726, to confirm the double counting is general.
- Why the latest reliable branch at 202726 was a tip of the slot itself rather than a common ancestor.

## 6. Watch

The network-watch session alerts on: split rate above 5% per 1000 slots, two consecutive split slots, a low
group under 58.33%, and the latest reliable branch on a low-group branch for two readings. Its labels are
classical: the "low group" is usually the canonical side.
