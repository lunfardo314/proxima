# Split slots: classical coverage against the adjusted score

> RESEARCH, open. Investigated 2026-11-10 and 2026-11-11 on take 1, from the raw DAG (hloc0-acc and
> oseq1-acc dag explorer), node logs and the offline tools `.internal/seq_analysis/score_eval` and
> `.internal/seq_analysis/split_sides` (classical delta of every branch reconstructed to the mote: 46
> branches of 197125, 60-68 of each of 201306, 202725, 202726, 204129, 204130, 204151, 204305). Nothing
> built. Read before changing the branch health check, the latest reliable branch, the sequencer's
> ranking, its first move, or the coverage definitions.

## 1. What a split slot is

A slot in which the next slot's branches build on a branch more than 15 points (percent of supply) below
the slot's best by classical coverage delta: the classical leader is orphaned by a wide margin. About 0.5%
of slots (section 3). A split lasts one slot.

**The watch sees only a sample.** It reads the `BRANCH COMMIT` lines of one access node, which prints
only the branches it commits (the commit is deferred until something needs the state); every other branch
is discarded uncommitted. 202726 has 60 branches in the DAG, the watch saw 14; 204130 has 61, the watch saw
10. The "inverted" (202726) and "uniform" (204130) shapes reported on 2026-11-11 are artefacts of that
sample: both are ordinary splits (section 3). The watch's rule (two branches more than 15 points under the
best) and its rate of 1-3% are properties of the sample: on every branch the DAG holds, the same rule fires
on 25% of slots in 200000-202000, mostly a few small branches around 66% that nobody builds on. Group
sizes, rates and the sequencer names on each side must be read from the DAG.

There are two kinds:

- **Conflict split.** The two groups spend the same outputs; the classical coverage and the adjusted
  score pick opposite sides, and the score's side wins. 197125, 201306, 202726, 204130, 204151.
- **Early-cut split.** The low branches were cut early in the previous slot and miss a late milestone of
  a large holder; no conflict, the two measures agree, the high side wins. 202725, 204305.

## 2. Mechanism of a conflict split

1. **A branch race in slot S** is won by one branch `W`. Some sequencers issued late milestones in S−1
   that `W` does not contain (it was cut earlier, at tick 90 in 202725).
2. **At the first move of S** (tick 9) a sequencer either extends its own left-out late milestone
   (endorsing `W`, which is valid: nothing in `W` spends it) or restarts from its chain output committed in
   `W`. Those that extend carry the left-out history of S−1 into S, including the late milestones of other
   sequencers their own late milestones endorsed (side A). The rest restart (side B).
3. **The sides spend the same outputs.** A sequencer that restarted spends its committed output on side B,
   while its own late milestone, endorsed by others, spends the same output on side A. No milestone of one
   side can endorse one of the other for the rest of S.
4. **Sequencer `v3` ranks by the adjusted score**, which counts only value entering the baseline's own
   history (`dag_semantics.md` §1.7) and so gives side A no credit for the left-out history. Classical
   coverage counts all of it. By the score side B is ahead; the sequencers stay on B.
5. **Side B wins.** In S+1 every branch builds on B's winning branch; A is orphaned.

**The conflict is made by the restarts, not by the carriers.** A carrier's extension is valid on `W` and
conflicts with nothing in it. What makes the sides conflict is a sequencer spending its committed output a
second time while its own late milestone, which already spent it, travels in a carrier's cone. Had it
extended its late milestone too, both sides would hold the same milestone and could merge.

Who restarts (first moves on the winner's baseline at ticks up to 20, sequencers with a late milestone left
out of `W`; `.internal/seq_analysis/split_sides`, classify mode):

| S | Extend, issued a branch | Extend, no branch | Restart, issued a branch | Restart, no branch |
|---|---|---|---|---|
| 202725 | 6 | 9 | 49 | 12 |
| 204129 | 3 | 8 | 55 | 2 |
| 204150 | 7 | 5 | 49 | 3 |
| 201305 | 10 | 2 | 41 | 16 |

- Nine in ten sequencers whose own branch lost the race restart. Every branch issuer that extends
  extends exactly its losing branch's chain predecessor, its late milestone.
- For the restarters, extending that late milestone onto `W` was conflict-free in 40 of 49, 54 of 55, 49 of
  49 and 29 of 39 cases. The late milestones that conflict with `W` are exactly those that conflict with a
  carrier's cone: none of them could be on side A. **Had every sequencer extended where it could, the
  sides would have had nothing to conflict on.**
- The bootstrap sequencer `boot` (about 15.8% of supply) extends in all four slots, J11K in three; their
  stake is then missing from side B. In 202725 and 204129 boot's own branch tied the winner to the mote and
  lost on the branch inflation bonus. At 202725 boot extended `s202724-104-008bc901b55e` onto RAMEN's
  `s202725-0-016a66d1c57b`, while oseq1, in the same position (late 93 and 104 left out, its branch on 104
  lost), restarted from `s202724-58#0`. Both were conflict-free; by the score they nearly tie (82.38T
  against 82.79T), by classical coverage the extension leads by 30T.

**Why the restart, from the code** (`sequencer/v3/factory/first_move.go`, `firstMovePairs`; `seedPairs` and
`reanchorPair` in `extend_endorse.go`). The extend candidates for an endorsed candidate are the head (the
newest own output in the memDAG), the newest own milestone the candidate's past cone references, and the
own output committed in the candidate's baseline. After a lost race the head is the own losing branch,
which cannot extend onto the winner; the winner's cone references only own milestones from before its
cut; so the late milestone is never offered and the only valid pair is the restart. A sequencer whose head
is its late milestone (no branch issued, or its branch not yet in its own-milestone list, the likely case
for the exceptions) is offered both, and the proposer keeps the higher score, ties going to the skeleton
drained last (`tryFactoryProposal`, `>=`). Not verified by trace; `factory_firstMove` on a few sequencers
would confirm it.

## 3. Measurements

Every slot split by conflict, the two sides on the same baseline. "Double spent": baseline-state outputs
spent on both sides by different transactions, each pair the same sequencer.

| Slot | Side A (orphaned): branches, classical / adjusted | Side B (canonical): branches, classical / adjusted | Sum classical / adjusted | Double spent |
|---|---|---|---|---|
| 197125 | 8: 93% / 37% | 38: 59% / 59% | 152% / 96% | 27 chain outputs |
| 201306 | 7: 93.8% / 42% | 49: 61-62% / equal | 156% / 105% | 52% |
| 202726 | 34: 90.5-93.8% / 33-55% | 26: 70.7-75.5% / 32-60% | 169% / 98.5% | 25.6% |
| 204130 | 8: 82.7-92.1% / 0-50% | 53: 77.8-77.9% / equal | 170% / 102% | 68.5% |
| 204151 | 5: 89-93.7% / 32-39% | 51: 72.7-77.2% / about equal | 171% / 116% | 55.5% |

In each slot the classical winner is on side A and the score winner is the branch every branch of the next
slot builds on. Adjusted sums above 100% come from shared common history spent on both sides, not from the
conflict. The double-spent stake is mostly the founders' sequencers' (about 16% each), which restart.

**History** (every branch from the DAG explorer's slot view, windows of 2000 slots, no fetch errors;
`.internal/seq_analysis/split_history/`):

| Window | Branches per slot (median) | Splits (>15 points) | Next slot not on the best (>1 point) | Branches on the winning side (median) |
|---|---|---|---|---|
| 128000-129999 | 29 | 0.60% | 38.5% | 2 |
| 134000-135999 | 32 | 0.50% | 47.5% | 3 |
| 136000-137999 | 33 | 2.50% | 57.9% | 2 |
| 138000-139999 | 38 | 0 | 1.85% | |
| 142000-161999 (4 windows) | 41-48 | 0-0.20% | 0.55-2.35% | |
| 170000-176999 (2 windows) | 67-73 | 0-0.10% | 0.20-0.55% | |
| 180000-181999 | 71 | 0.50% | 1.35% | 45 |
| 190000-191999 | 84 | 0.50% | 1.10% | 62 |
| 200000-202000 | 64 | 0.45% | 1.20% | 51 |

Splits are not caused by the score: under classical ranking (before slot 138000) they occurred at the same
rate as today, and the first 40,000 slots of the score had almost none. The switch changed their shape:
before it, a few low branches won against an orphaned majority of high branches; now a large low side wins
against a small orphaned high group, the side A / side B picture of section 2. Before 138000 the next slot
also left the best branch by more than one point in 38-58% of slots, after it in 0.2-2.4% (not explained;
the arrival-speed bias of `branch-bias.md` is the likely reading). The 2.5% peak of 136000-137999 starts
before library upgrade 4 (137000) and is not tied to a release. The rise after about 177000 is not
explained either; the bootstrap sequencer as carrier (section 2) is a candidate.

The 202726 reading "the latest reliable branch on a 75% branch" was MERCUR's `s202726-0-0150f9ed5bc3`, the
canonical branch, read one slot later: every tip was in 202727 and on MERCUR, so it was their common
ancestor, the normal one-slot lag of `FindLatestReliableBranch`. A slot earlier both sides were within the
competing-tip half rule and the reliable branch was their common ancestor in 202725.

## 4. Consequences

**Optics.** The watch, the `BRANCH COMMIT` percentages and the health figure measure classical coverage,
so they show the canonical side as the low group.

**Classical coverage counts one stake on two conflicting sides.** The sequencer that restarted counts on B
by its restart and on A by its own late milestone, which spends the same output. Signing two spends of
one's own output costs nothing and the reference sequencer does it on its own. Hence the 7/12 health
threshold is not a proof that a majority of stake backs a branch: two conflicting branches can both be
healthy in one slot. The adjusted delta removes this double count, but not every one: a sequencer that
extends both sides with its own chain counts on both by either measure (inference, not observed).

**Finality holds.** The latest reliable branch is the branch every competing healthy tip of the slot
contains, so in a split it is their common ancestor and neither side is final until the next slot.

**Liveness weakens with conflicts.** Side B carries only the stake that is on B. If that falls under 7/12,
the reference sequencer does not issue its branch (the issuance gate checks the classical delta), and the
slot continues on side A, whose classical delta is inflated by the double count. A quantitative bound, and
what a deliberate actor could do with it, is in `.internal` (not public).

## 5. Fundamental or fixable

The security assumption is that honest holders control more than 1/2 + θ of the stake; the code's
7/12 health threshold and its complement 5/12 are that bound with θ = 1/12. A camp with stake `c`, honest
`1 − c`, every honest sequencer on side B:

- **c < 5/12, inside the assumption.** B is healthy by either measure (`1 − c > 7/12`) and wins by the
  score (`1 − c > c`). The camp's side A can still be healthy by classical delta (its own stake plus the
  honest late history it carries), so the slot has two healthy conflicting tips and the latest reliable
  branch falls back to their common ancestor: one more slot of finality latency per split, the camp's own
  work orphaned. No stall, no reversal. A nuisance the camp pays for, not an attack.
- **5/12 < c < 7/12, outside the assumption.** B is under 7/12 by either measure; no honest branch is
  issued. What happens next depends on the measure the issuance gate uses, and is fundamental either way:
  by classical delta (today) the camp's side, inflated by the honest history it carries, is healthy and
  commits, so the camp decides what the slot commits; by adjusted delta neither side is healthy and the
  slot has no branch. Control against stall: neither is fixable without changing the threshold.
- **c > 7/12.** The camp is the majority.

So the attack does not lower the bound: it needs `c > 5/12`, which the assumption excludes. The private
note's "below a majority" means below 1/2, not below the bound. What double counting adds inside the
assumption is the split itself and a slot of latency, and both have an honest cause: no camp is needed for
the 0.5% of slots measured in section 3. That part is fixable (section 6, option 5). With it, a camp
carrying honest late milestones no longer conflicts with the honest side, which holds the same milestones;
the camp can make a conflict only with its own stake, by spending its own outputs differently on the two
sides, and the double count is then its own stake alone.

Practical mitigation for the zone above 5/12, where nothing in the protocol helps: detect a deliberate
pattern (splits on consecutive slots with the same sequencers carrying the orphaned side, as distinct from
the honest one-slot splits) and alert, and make the honest splits rare enough that the pattern stands out.

## 6. Options

1. **Accept** (current). Splits last one slot; safety holds through the walk-back to a common ancestor.
2. **Judge health by the adjusted delta.** Not possible as stated. Health is a stored, validated value
   (`BranchData.IsHealthy` reads the committed coverage delta; `FindLatestHealthySlot` drives sync and
   memDAG pruning from the database), while the adjusted delta is never validated or stored and exists only
   on a node that attached the branch against its true baseline (D4, D7). Moving health, the latest
   reliable branch or sync onto it needs it either validated (a hardfork: the sequencer constraint
   declaring it) or stored unvalidated (breaks D4). The only cheap part is the issuance gate
   (`sequencer/v3/sequencer.go`, own branch, score at hand), and on side B the two measures are equal, so
   switching the gate only stops side A issuing: where B is under 7/12 the slot then has no branch instead
   of continuing on A. A liveness cost, not a gain.
3. **Rank `v3` by the classical coverage again.** Brings back the late-consolidation bias the score
   removes.
4. **More flexibility to revert within a slot** (`.internal/sequencer_backlog.md` item 1). Does not address
   the metric disagreement; the 2026-08-11 greedy switch was reverted.
5. **Never abandon own history that can be extended** (recommended). A sequencer does not spend an own
   output a second time while a newer own milestone that spent it can be extended on the same baseline.
   Two changes in the `v3` factory and proposer, sequencer policy only, no ledger change:
   - *Offer the late milestone.* Among the extend candidates of `firstMovePairs`, and of the head phase of
     `seedPairs`, add the newest own non-branch milestone of an earlier slot when the head is an own branch
     (it is that branch's chain predecessor). The incremental attacher rejects it where it conflicts.
   - *Prefer it.* On one baseline, a skeleton whose chain input is an own ancestor of another valid
     skeleton's chain input is not taken: in `seedPairs` the head's pair is kept over the re-anchor on the
     same baseline whenever both are valid (today the higher score wins), and in `tryFactoryProposal` an
     equal score goes to the newer chain input. The score difference given up is at most what the late
     milestone paid out; normally the extension scores at least as high, since its output carries the
     tag-alongs it consumed.

   Effect: in the four slots measured, every conflict between the sides would have been gone. Only the
   late milestones that conflict with `W` still restart, and those are on no carrier's side. It also makes
   the outcome robust: a single carrier (an old release, another implementation, a deliberate one) no
   longer turns every restarter's stake into a conflict, since there are no restarters to conflict with.
   The opposite policy, always restart from the committed output (this document's earlier option 5), is
   rejected for that reason: it is safe only if no sequencer at all carries a late milestone, and one does
   in every split measured. Most of the double-spent stake is the founders' sequencers' (section 3), so the
   own five sequencers on the change already remove most of it. Measure before and after with the
   DAG-based detector of `.internal/seq_analysis/split_history` (rate and depth of splits) and the classify
   mode (restarters with an extendable late milestone, which should go to 0 for upgraded sequencers).
6. **Log the adjusted delta beside the classical one** in `BRANCH COMMIT` (the score is on the vertex
   when the branch commits). Fixes the optics only.
7. **Base the watch on the DAG.** Replace the `BRANCH COMMIT` sample by the slot view of the DAG
   explorer, the split rule by the orphaned-leader rule of section 1, and add the deliberate-pattern alert
   of section 5.

## 7. Open questions

- What brought splits back from about slot 177000, after almost none since 138000: whether it is when the
  bootstrap sequencer started carrying side A, or a change of release, timing or sequencer count.
- Confirm by trace (`factory_firstMove`) that a branch-losing sequencer is offered no extension of its
  late milestone, and why the exceptions (boot, J11K) are: own branch not yet in the own-milestone list,
  or timing.
- Why some sequencers with no branch and an extendable head still restart (12 at 202725, 16 at 201305):
  a higher score for the restart (the late milestone paid something out) or the drain order on a tie.

## 8. Watch

The network-watch session alerts on: split rate above 5% per 1000 slots, two consecutive split slots, a low
group under 58.33%, and the latest reliable branch on a low-group branch for two readings. Its labels are
classical (the "low group" is usually the canonical side) and its branch counts are a sample (section 1).
