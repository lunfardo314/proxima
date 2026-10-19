# Branch bias: who wins a slot's branch

> RESEARCH, open. Measured 2026-10-19 on take 1 (hloc0's logs since slot ~70, chain walks to slot
> ~9,660). Nothing here is built. For discussion before any change to sequencer strategy or the
> coverage definition.

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

## Directions

1. **Baseline selection by branch, not by milestone** (sequencer policy, no hardfork). At slot
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
