# Reference sequencer strategy by zone (coverage adjustment, stage 1)

> SPEC, not built (rewritten 2026-10-26; the earlier version of 2026-10-23 described a wide
> multi-baseline search as the fix for early branch cutting, which it is not). Stage 1 of two: the
> reference sequencer's strategy, ranked by today's coverage. A node release: no ledger change, no
> activation slot, each node can switch on its own. Stage 2, the adjusted coverage delta that removes
> the early-cut advantage, is `kb/coverage_adjustment_B.md`; it replaces only the score the sequencer
> compares and needs a synchronised activation. Read before touching `sequencer/factory`,
> `sequencer/task`, the zone handling in `sequencer/strategy_async.go`, or the candidate order in
> `sequencer/backlog`.

## 1. Why stage 1 comes first

- The current behaviour is suboptimal with or without the coverage adjustment: the first move of a
  slot is the first good pair found, not the best, and the pre-branch zone rarely produces the
  consolidation it is meant for.
- The early-cut scheme is not being run (its operator confirmed running the stock code). Stage 1
  assumes no early-cut bias; if one appears before stage 2, the reference sequencer follows it, as
  today.
- The changes are the riskier part and are needed in both stages, so they ship and are measured on
  their own. Stage 2 is then expected to be largely transparent to the sequencer: it changes the
  value compared, not the strategy.
- Stage 2 must be activated in sync from a slot (all reference sequencers switching metric together);
  stage 1 has no such requirement and can roll out node by node.

## 2. Principles

- **Coverage decides.** A sequencer maximises the score of its next transaction among admissible
  ones; nothing selects a branch, an issuer or a group by preference.
- **Natural latency is acceptable, artificial delay is not.** The first milestone goes out at tick 9
  with the best pair found by then; nothing waits for a better one.
- **The search cost is the operator's choice.** A wider search costs CPU and time; the defaults are
  safe, configuration can raise them.
- **One score.** Every comparison the sequencer makes goes through one function: in stage 1 it is
  today's coverage, in stage 2 the adjusted score. Only that function changes between the stages.

## 3. The slot by zone

A slot has 128 ticks. Since library upgrade 2 (slot 45000) no sequencer transaction other than a
branch may be stamped before tick 9, and in the pre-branch zone (the last 25 ticks, 103-127) a
sequencer transaction has a single input, its own chain predecessor of the same slot, plus
endorsements.

| Zone | Ticks | Role | Stage 1 |
|---|---|---|---|
| Post-branch | 0-8, first milestone at 9 | the first move: choose the lineage | heavy coverage maximisation by extend-endorse pair |
| Middle | 9-102 | servicing: tag-alongs, delegation freezes, mine settlement, improvement | as now |
| Pre-branch | 103-127 | consolidation before the branch | two milestones, the branch on the second |

### 3.1 Post-branch zone: the best extend-endorse pair

**Today.** The factory targets the slot from its start (`strategy_async.go` sets the target slot), so
it already works during ticks 0-8. But a round seeds from the first pair that attaches
(`chooseFirstExtendEndorsePair`: the chain head with the endorse candidates in the backlog's order,
then the newest own milestone in each candidate's past cone, then the own output committed in each
candidate's branch) and improves that seed only (`improvementLoop`). Which lineage the first move
takes is decided by the candidate order, not by comparing pairs.

**Stage 1.** During ticks 0-8 the factory's only priority is the best pair for the first milestone,
searched as exhaustively as the time allows:

- **Extensions**: every own output the own past cone offers as a chain predecessor: the chain head;
  for each candidate branch, the newest own milestone its past cone references
  (`newestOwnInPastCone`); the own output committed in that branch's state.
- **Endorsements**: every branch of the slot that has arrived (the backlog's candidates at this time
  are the slot's bare branches).
- **Ranking**: each pair attached as a skeleton (an incremental attacher) and ranked by the score; the
  best kept and replaced whenever a better pair appears, as branches keep arriving.
- **Submission**: at tick 9 the best pair goes out at once (the first-move wait was removed in
  v1.2.3). Pairs not attached by then drop out by themselves.
- **Content**: the pair only. A first milestone extending the own milestone of the previous slot
  inflates the chain output (only a cross-slot transition does); tag-alongs and delegation freezes
  wait for the middle of the slot.
- **Bound**: at most `MaxFirstMovePairs` (48) pairs attached per round, today's branch count (8 to
  10 per slot) times the three extension kinds with room to spare; a constant, not configuration.
- **Mode**: the factory searches this way while the sequencer has no non-branch milestone of the
  target slot (`firstMovePending`), whatever the tick; afterwards it improves as before. Pairs are
  attached three at a time; a pair not yet solid is retried in the next round.
- **Posting never blocks**: the factory's output channel (4 skeletons) is drained only when a
  milestone is built, so before tick 9 the factory used to stop after four posts. Now a full
  channel drops its oldest skeleton, which within a slot is never better than the new one.

With no early-cut bias, the arrived branches tie on coverage up to their content, and the bonus
breaks ties (the baseline sequencer output's inflation is in the delta while unconsumed); an
exhaustive comparison is what lets the bonus decide instead of the candidate order.

### 3.2 Middle zone: as now

Tag-alongs, delegation freezes, improvement by endorsements, the mine settlement pulse (tick 86,
`settlementPulseDue`), pace-driven pulses: unchanged.

### 3.3 Pre-branch zone: two milestones

**Today.** The loop holds in the zone (no pulses) until tick 127, tries one final consolidation
milestone, waits for it to attach and submits the branch on it (`strategy_async.go`,
`finalConsolidationTried`). The final milestone rarely materialises: on oseq1 under v1.1.3 the last
milestone of a slot was at tick 96 or later in about 0.7% of slots, because the strategy has nothing
to improve late, not because a late milestone could not be built.

**Stage 1.**

1. **Closing milestone**, early in the zone (from tick 103): endorses every peer's latest milestone
   of the slot not yet in its past cone. It brings the slot's activity of the peers into the
   sequencer's chain early enough for the peers to see it.
2. **Final milestone**, late in the zone (towards tick 127): endorses the peers' closing milestones.
3. **Branch** on the final milestone (the branch's chain predecessor is exempt from the pace, so the
   final milestone can be at the last ticks).

Validity constrains both (`sequencer.easyfl`): the declared coverage delta must strictly exceed the
same-slot predecessor's, and the zone allows no input but the chain predecessor, so a zone milestone
must endorse something new that raises coverage. A closing milestone with nothing new to endorse
(the sequencer's previous milestone already covers everything the peers have issued) is skipped;
the final milestone is then built on the peers' closing milestones if any arrived, otherwise skipped
too, and the branch extends the latest own milestone, as today. Skipping is the strategy, not a
failure; it is counted (section 6).

Timing, to tune in the rehearsal: closing around ticks 103-110, final around 120-127, against 1-3
ticks of propagation and the 3-tick pace. A sequencer that issued nothing earlier in the slot cannot
issue a zone milestone (upgrade 2's same-slot predecessor rule): it branches on its latest milestone.

## 4. Eligibility before maximisation

`task.Run` picks the best proposal among the proposers and only then checks that its coverage beats
the own latest milestone of the slot (`ErrNotGoodEnough`). When the best proposal fails the check, a
valid one is lost:

| Candidate | Coverage |
|---|---|
| own latest milestone of the slot | 100 |
| B (best by the ranking) | 99 |
| C | 101 |

Stage 1 decides each proposal's eligibility first and takes the best eligible one. Two rules apply.
The ledger's, strictly above the effective predecessor's coverage delta (`_effectivePredCoverage`,
0 for a cross-slot predecessor), is already enforced on each proposal before it is built
(`finalize`). The node's, coverage above the own latest non-branch milestone of the slot, stays as
it was: dropping it would let a re-anchor to a lineage of lower coverage go out, switching lineage
back and forth within the slot. In stage 1, where ranking is by coverage, checking first changes
nothing (the best proposal is eligible whenever any is); it is in place for stage 2, where ranking
is by the adjusted score and the two can disagree.

## 5. The score function

All comparisons go through one function of a vertex or a proposal:

- `IsPreferredMilestoneAgainstTheOther` (`core/vertex/cmp.go`), and through it the tippool and the
  backlog's candidate order;
- the factory's ranking and publication (`FinalLedgerCoverage` calls in `factory.go`,
  `extend_endorse.go`, `BestCoverage`);
- `betterProposal` (`task.go`) and the proposers.

In stage 1 the function returns today's coverage (`FinalLedgerCoverage`, the vertex's
`GetLedgerCoverage`). Stage 2 replaces its body with the adjusted score. The eligibility check of
section 4 stays on the coverage delta in both stages.

## 6. Measurement

On the testnet, stage 1 against the release before it:

- share of slots whose winning branch had the top coverage (89% under v1.1.3);
- sequencers consolidated by the latest reliable branch (`proxima_lrb_num_seq`) and its coverage
  delta (health);
- per sequencer and slot: closing and final milestones issued, skipped for having nothing new, and
  their ticks;
- tick and latency of the first milestone (target: tick 9), pairs evaluated per round, CPU;
- winners per issuer; who decides the slot at the first move.

## 7. Tests

- Factory: during the post-branch zone, the best pair over several branches and extension kinds is
  published, not the first; a better pair arriving later replaces it; at tick 9 the best available
  goes out.
- `task.Run`: the eligibility case of section 4 (best proposal ineligible, an eligible one taken); a
  cross-slot re-anchor below the own latest milestone's coverage taken.
- Pre-branch zone: closing then final then branch; closing skipped with nothing new; final skipped;
  a sequencer with no earlier milestone in the slot branching directly.
- Multi-sequencer, in process (`tests/`, one at a time, under `-race`): several branch-producing
  sequencers over many slots, measuring the items of section 6 and that the bonus decides tied
  branches.
- Rehearsal with the local multi-node kit (`.internal/rehearsal_upgrade2/`), including a loaded
  slot, to tune the zone timing.

## 8. Rollout

A node release; nodes switch one by one, no activation slot. Founder's sequencers first, measured,
then announced for everyone. Rolled back by a release.
