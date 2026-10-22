# Branch bias: who wins a slot's branch

> RESEARCH, open. Measured 2026-10-19 on take 1 (hloc0's logs since slot ~70, chain walks to slot
> ~9,660) and again 2026-10-21 (slots 17,545 to 29,342, across the slot 29000 upgrade), with an
> operator's field notes checked on 2026-10-22. Built from it so far: four reference-sequencer
> fixes (third input below); the ledger direction, post-branch consolidation ticks, is
> specified in `kb/library_upgrade2.md` and not built. Read before any change to sequencer
> strategy or the coverage definition.

## The question

The whitepaper (arXiv 2411.16456, sections 8.6 and 9.2) expects competing branches of a slot to
have equal coverage, and the branch with the larger random inflation bonus to win, so that "the
amount of tokens on the sequencer milestones does not influence the outcome" (Conclusion 9.1). The
random bonus exists precisely to remove the rich chain bias. Does the live network behave that way?

## What the whitepaper argues

Two branches A and B of one slot with equal coverage C. A follower choosing between them compares,
for its next milestone,

    amountA + I_A + amountB + C/2     (on A's branch)
    amountA + amountB + I_B + C/2     (on B's branch)

Both sides contain both sequencers' amounts, they cancel, and the bonus I decides. Full coverage
adds the branch history with weights halving per slot, the same for branches on one predecessor.

## What is measured

- **The tie holds.** Of 8,581 slots in which hloc0 saw at least two branches, 8,457 (98.6%) had
  equal coverage delta, mostly 2 to 6 branches.
- **The bonus does not decide.** If it did, an issuer would win 1/k of the ties it is in. Win
  ratio = ties won / expected:

  | Issuer | Ties | Won | Expected | Ratio | Capital (balance + frozen) | Pace |
  |---|---|---|---|---|---|---|
  | oseq1 | 2,888 | 2,223 | 948 | 2.34 | 12.18M | 12 |
  | MORA | 1,234 | 716 | 404 | 1.77 | 0.13M | 3 |
  | oloc1 | 3,370 | 1,693 | 1,053 | 1.61 | 12.15M | 12 |
  | boot | 3,412 | 1,446 | 1,091 | 1.33 | 12.05M | 12 |
  | oloc2 | 3,448 | 1,372 | 1,079 | 1.27 | 12.05M | 12 |
  | VULCAN | 452 | 96 | 83 | 1.16 | 0.003M | 4 |
  | JANUS | 1,117 | 107 | 197 | 0.54 | 0.013M | default |
  | MERCUR | 963 | 75 | 165 | 0.45 | 0.007M | default |
  | hloc0 | 8,328 | 729 | 3,438 | 0.21 | 12.11M | 12 |

  Capital is today's value; frozen amounts changed over the period, so the order is approximate.
  hloc0 is the observer: its own branch is in nearly every tie it logs, which depresses its ratio.
- **Overall winners** (canonical branches, whole history): oseq1 24%, oloc1 18%, hloc0 16%, boot
  16%, oloc2 15%, MORA 8%, the three small sequencers about 1% each. Since slot 8,521: oseq1 33%.
  A third-party operator reports oseq1 winning about 75% of the slots in which it submits a branch.

## Mechanism

1. **The choice is made before the symmetry exists.** The cancellation needs both amounts on both
   sides, which is true only once every sequencer has consolidated on both branches. At the start
   of a slot each issuer first extends its own branch, consuming its own branch output, and
   followers endorse the best milestone they hold on each branch at that moment. A follower
   committed to one lineage leaves it only at the cost of a revert.
2. **The issuer's capital enters through its own milestone.** A sequencer output counts as balance
   plus frozen delegations (`ledger.Coverage`). The baseline branch's sequencer output counts fully
   once consumed, which the issuer's own next milestone does; while unconsumed it adds only its
   inflation, the bonus included (`coverageDeltaAdjustment` in `core/attacher/attacher.go`).
   Endorsing issuer X's first milestone therefore adds X's balance and frozen delegations, millions
   of PROX for the founder's sequencers, against a bonus of at most about 5 PROX.
3. **Speed decides among the rest.** A follower takes the best milestone available, so the issuer
   whose first milestone on its own branch appears earliest is endorsed first: pace 3 (MORA) and 4
   (VULCAN) beat equal-capital sequencers at the default pace, and the same operator reports CPU
   latency at the slot boundary making the difference between two otherwise identical sequencers.

## Consequences

- The rich chain bias of section 9.2 returns through two channels: delegated capital, since frozen
  delegations count as the sequencer's coverage, and latency (pace, CPU, network).
- Today it gives the founder's five sequencers about 90% of branch wins and of the branch bonus.
- It pays operators to run the minimum pace and fast hardware, not to issue branches.
- The pace incentive overlaps with the sequencer rating's uncapped transit-count criterion
  (`kb/sequencer_rating.md`).

## Second measurement (2026-10-21): speed alone takes the slot

Slots 17,545 to 29,342 (about 33 hours): every branch hloc0-acc committed (`BRANCH COMMIT`, with
issuer and coverage delta) joined with the latest reliable branch's lineage, walked with
`.internal/minerwatch/lineage`. Arrival is the commit time on hloc0-acc minus the slot's start.

- **Ties are the rule.** The competing branches' coverage delta was equal in 73% to 85% of slots.
  Frozen delegations are about 62T of a 62.5T supply and count in full on every branch, so a
  branch cut early in the previous slot loses nothing measurable.
- **The winner is whoever reaches the capital first.** The founder's sequencers hold 97.5% of the
  capital and build their first milestone of a slot on the best candidate they hold at that moment;
  among tied branches that is the one that arrived first. The winner was the first branch to arrive
  at hloc0-acc in 64% of the last 3,000 pre-upgrade slots and 75% of the slots after it.
- **The dominant winner migrates.** MORA took 76% of slots 22,500 to 24,999. VULCAN's branches
  began to arrive about 250 ms earlier at slot ~25,000 (2026-10-21 05:38 UTC) and from slot
  ~26,000 (08:29 UTC) it took 74% to 88%, MORA falling to 3%. After the upgrade: VULCAN 43%,
  boot 28%, Centauri 24%. Neither change coincides with a release on the founder's nodes.

  | Issuer (slots 26,000 to 28,999) | Wins | Ratio to a fair share of ties | Arrival p50 / p90 (ms) |
  |---|---|---|---|
  | VULCAN | 82.8% | 2.35 | 191 / 209 |
  | oseq1 | 6.3% | 1.99 | 435 / 1,336 |
  | Centauri | 4.9% | 0.40 | 201 / 213 |
  | MORA | 2.6% | 0.10 | 336 / 342 |
  | boot | 1.2% | 0.35 | 451 / 468 |
  | srv1 | 0.1% | 0.01 | 93 / 622 |

  srv1 arrives earliest and wins almost nothing. It is a sequencer whose milestones the mine rule
  was seen refusing for taking transits against the rule, which would keep its lineage from being
  endorsed; not verified slot by slot.
- **How VULCAN is fast.** Its branches extend its own milestone of tick 23 to 91 of the previous
  slot, not the last tick, so their past cone is already solid everywhere when the slot opens and
  they commit about 190 ms in; the founder's branches extend a milestone of the last tick and
  commit about 450 ms in. The early cut predates VULCAN's winning streak; what changed around
  slot 25,000 is the arrival time itself (custom build, earlier submission or better peering, not
  visible from here). VULCAN, Centauri and MORA arrive with almost no jitter (p50 to p90 within
  20 ms), consistent with a branch prepared well ahead and released at the slot boundary.
- **No sign of an attack.** No branch from any issuer was committed before its slot started
  (earliest 8 ms after), so no clock running ahead; the bonus is not ground since it decides
  nothing. Low latency is a legitimate advantage, but here it is also unfair: the bonus goes to
  whoever optimises arrival at the capital holders, which defeats the random bonus.

## Third input (2026-10-22): the winning operator's field notes

An operator running VULCAN, JANUS, MERCUR and SATURN sent its measurements and the changes it
made to its own sequencers (no ledger change). What was checked against the code and the DAG
here, and what came of it:

- **Verified: the slot goes to the largest single milestone at about tick 5.** The founder's
  sequencers build their first milestone of a slot at tick 5 on the best candidate of
  `CandidatesToEndorseSorted`: each sequencer's latest milestone, ordered by its own ledger
  coverage (`IsPreferredMilestoneAgainstTheOther`). Lineages are not summed, so capital spread
  over several milestones counts only once someone bundles it. VULCAN's partners endorse its
  branch at ticks 1 to 2 and VULCAN bundles them at tick 3; by tick 5 that is the heaviest
  milestone and the founder's 97.5% follows it. This refines the second measurement: VULCAN's
  early branch arrival matters because it lets the partners endorse at tick 1. Their figures:
  VULCAN 2% of slots before the bundling, 86% to 89% after; with partners at tick 1 it won 92%,
  at tick 2 77%, at tick 3 8%.
- **Verified: the reference sequencer refused endorsements the ledger allows.** `endorsableFrom`
  (`sequencer/backlog`) required the sequencer pace (3 ticks) between candidate and target; the
  ledger requires one tick (`scanEndorsements`, since v0.8.0). A milestone of the last two ticks
  of a slot was therefore invisible to the founder's final consolidation at tick 127, which MORA
  and Centauri used to carry capital onto one branch only. Fixed: one tick, as the ledger.
- **Verified: the founder's sequencers fork their own chains.** Over 10 slots of the DAG, an own
  output consumed twice: boot 17 times, hloc0 14, oseq1 13, in 8 to 11 of the slots; VULCAN, with
  a guard, once. The factory queues skeletons, and one built on a head that a newer own milestone
  has since extended can still be the heaviest; taking it gives the old head a second successor,
  and the two successors conflict in every past cone holding both. Fixed: the factory proposer
  drops a skeleton extending an own milestone of the target slot that is no longer the latest
  (`extendsSupersededOwnMilestone`); extending an own output of an earlier slot, the re-anchor
  on a lineage switch, stays allowed.
- **Plausible from the code: re-anchoring through the branch state fails.** The factory's
  fallback extended the own chain output committed in the candidate's branch. When a newer own
  milestone is already in the candidate's past cone (a peer endorsed it), that output is spent
  there, and the pair is dropped as a double spend. The operator measured 206 failures against
  24 successes in 42 minutes on its own build; it cannot be counted here without the factory's
  trace tag. Fixed: before the branch state, the factory extends the newest own milestone the
  candidate's past cone references (`newestOwnInPastCone`).
- **Found here, not in the report: the tie-break was grindable.** Candidates of equal coverage
  were ordered by transaction ID, which a branch's issuer can grind by varying the branch. With
  post-branch consolidation, branches tie at tick 9 and the ID would decide. Now: coverage, then,
  between two branches, the branch inflation bonus (a VRF output nobody chooses), then the ID only
  to keep the order total.
- **Open: exclusive late capital.** A sequencer can reveal its own capital in a milestone at tick
  127 and build its own branch on it; nobody else can include it, and that branch gains coverage.
  The pre-branch zone limits extra inputs, but the chain's own input is always allowed. The
  report's further claim that such capital counts twice, in the next branch's coverage delta and
  again in the first milestone of the next slot, is not verified against the coverage arithmetic.
  A possible ledger rule for a later upgrade: in the pre-branch zone a milestone's chain
  predecessor must be of the same slot.
- **Rejected:** choosing the baseline by branch, keeping the issuer's capital or the branch delta
  out of the comparison (each replaces or bends the biggest coverage rule); a cap on transactions
  per slot; linking an operator's sequencers in the rating.
- **To watch:** under load the founder's first milestone sometimes only extends its own branch,
  endorsing nothing (the operator correlates it with slot size: 0-7% of slots with fewer than 50
  transactions in the founder's branch, 86-100% from 80). The removal of the branch deferral
  adds branches per slot, so this may grow until upgrade 2 moves the decision to tick 9.

The four fixes are reference-sequencer policy only, no ledger change.

## Why post-branch consolidation was removed

Until v0.8.0 (2026-04-30) the ledger had `constPostBranchConsolidationTicks` (12) and
`checkPostBranchConsolidationTicks` in `sequencer.easyfl`: no non-branch sequencer transaction
before tick 12 of a slot. Its stated purpose (`kb/archive/shipped/seq-improvements.md`): give
the branches about a second to propagate, so every sequencer sees all of them before anyone
builds on one. It was removed as redundant (`kb/archive/shipped/seq_v080_pbc_removal.md`): the
sequencer pace applied to a branch output was thought to force the first extender to tick 12
anyway. That holds only for a sequencer extending its own branch. A follower extends its own
previous-slot milestone and endorses another sequencer's branch, and an endorsement needs only
a 1-tick gap, so it can build on that branch at tick 1. The same document says so ("could be
tick 1"). Since take 1 the sequencer pace is 3, so even an issuer's own branch can be extended
at tick 3.

## Directions

0. **Post-branch consolidation ticks back in the ledger** (library upgrade, no reset). Mirror
   `checkPreBranchConsolidationTicks`: a sequencer transaction that is not a branch must have
   tick >= `constPostBranchConsolidationTicks`. Every branch that arrives within that window is
   then available to every follower before its first milestone. Arrivals in slots 26,000 to
   29,342: 93.9% within 6 ticks (480 ms), 95.6% within 12 ticks (960 ms), 99.4% of boot's within
   12; the founder's other nodes have a tail near 1.3 s. 12 ticks covers the regular case.
   With nobody allowed to build before the window ends, every sequencer maximises coverage over
   the same bare branches, the amounts cancel and the bonus decides, as the whitepaper argues. A
   policy-only version cannot work: a sequencer that waits while another builds early finds the
   early branch further ahead in coverage. Spec: `kb/library_upgrade2.md`.
   Cost: a tag-along or delegation output waits at least the window into a slot before it can be
   consumed, and nothing is settled in the first second of a slot. The mine settlement tick is at
   the other end of the slot and is not affected.
1. **Rejected 2026-10-21: it breaks the biggest coverage rule.** A sequencer maximises the
   coverage of its next transaction; it does not select a branch. Kept for the record:
   **Baseline selection by branch, not by milestone** (sequencer policy, no hardfork). At slot
   start a follower chooses the baseline branch by branch coverage, then bonus, as the whitepaper
   intends, waits a few ticks for competing branches, then extends the chosen one; all honest
   followers converge on the same branch. Must be checked against `kb/dag_semantics.md` and
   `kb/sync_semantics.md`: it changes which milestones a sequencer builds on, not what is valid.
2. **Keep the issuer's own capital out of the comparison** (coverage definition, hardfork). Cleaner,
   but it changes the core consensus quantity.
3. **Accept and document**: branches go to capital plus speed, the bonus breaks ties between equals.

## Open questions

- Does direction 1 cost liveness or convergence time when the best branch arrives late, and how
  long should a follower wait for competing branches?
- Is the bias harmful for security, or only for the fairness of the branch bonus? Capital-weighted
  branch selection is close to stake weighting; the whitepaper rejects it for fairness, not safety.
- How much of the remaining spread among the founder's sequencers is capital and how much is node
  latency? Needs per-slot arrival times of the first milestone on each branch.

## Reproducing

Branches seen per slot with coverage delta: `BRANCH COMMIT` lines of a node's log. Canonical
winners: the sequencer chain walk (`.internal/minerwatch/seqhist`, branch flag per milestone).
Capital and pace: `.internal/minerwatch/seqrate` and the milestone tick gaps from the same walk.
Winners over a range of slots in one call per 200 branches: `.internal/minerwatch/lineage <api>
<from slot>` against a node's local API, joined by branch ID prefix with the `BRANCH COMMIT` lines.
