# Reference sequencer: search for the best move

> SPEC, not built (written 2026-10-23, branch `develop-upgrade2`). Node release only: no ledger
> change, no upgrade, compatible with every v1.2.x node before and after library upgrade 2
> (slot 45000). Widens the reference sequencer's search so that it finds the move with the
> largest coverage among everything available, instead of the first good one. Why: since
> 2026-10-22 a coordinated group wins about 90% of slots with lineages the reference sequencer
> follows because it never builds the alternatives (`kb/research/branch-bias.md`, fourth
> input). Read before touching `sequencer/factory`, `sequencer/task` or the candidate order in
> `sequencer/backlog`.

## Principle

- **Coverage decides.** A sequencer maximises the coverage of its next transaction. Nothing
  else selects a lineage: no preference for a branch, an issuer or a group
  (`kb/dag_semantics.md`, 1.5, including the rule that no timestamp decides whether an output
  counts in the coverage delta).
- **Strategies are countered by strategies.** Any sequencer may play any valid strategy,
  teaming up included. The reference sequencer has to encode the best one; the ledger only keeps
  the best strategy simple (post-branch consolidation, library upgrade 2, is the calm-down zone
  at the start of each slot).
- **The search space is every valid move.** A sequencer transaction that is not a branch either
  (a) continues its own chain from the same slot, its own branch included, or (b) extends one of
  its own earlier milestones across the slot and endorses one or more milestones or branches of
  the target slot (`sequencer.easyfl`). Every such move is a candidate, and coverage ranks them.
- **The cost is the operator's.** A wider search costs CPU, and time, which is latency of the
  sequencer's transactions. How much to spend is the operator's choice; a faster search reaches
  the optimum sooner and lowers latency.

## Why the current search loses

Measured 2026-10-22 (`kb/research/branch-bias.md`, fourth input): the winning branch had the
top coverage in 89% of slots under v1.1.3, in 7-11% since a group of four sequencers started
cutting early and collecting late. The group's lineage is not heavier over a slot; it is
heavier at the moment the reference sequencer decides, because the reference sequencer
compares what is ready at tick 5 and never builds the alternative.

The search today (`sequencer/factory`):

1. **One starting pair** (`chooseFirstExtendEndorsePair`). The first endorse candidate, in the
   order of `CandidatesToEndorseSorted` (latest milestones by their own coverage, transit
   holders first), that pairs with the chain head becomes the seed of the round. At tick 5 that
   is the pre-loaded milestone of the early-cut lineage.
2. **Hill climbing on that lineage only** (`improvementLoop`). Endorsements are added to the
   seed one by one. No skeleton on another baseline is built, so the move "join a late branch
   with the other followers" is never constructed and never compared.
3. **Candidates ranked by their own coverage,** not by the coverage of the skeleton they would
   make. The order of the backlog decides which lineage the round explores.
4. **The round restarts from the new head after each submission,** which is on the chosen
   lineage. A lineage switch later in the slot is possible (extending an own output of an
   earlier slot is allowed by `extendsSupersededOwnMilestone`) but only reached through the
   phase 2 fallback, when the head cannot pair.

## The search

1. **Seeds per baseline.** For every baseline available to the target slot (each branch of the
   slot, and the baselines of the endorse candidates), build seeds with every extend option
   valid on it: the chain head; the newest own milestone in the candidate's past cone
   (`newestOwnInPastCone`); the own chain output committed in that branch. Pair each with the
   best endorse candidate on that baseline.
2. **Improve every seed.** Run the improvement loop per baseline, as today per seed, and keep the
   best skeleton of each baseline.
3. **Rank by the skeleton.** Publish the skeleton with the largest coverage across baselines. The
   candidate order of the backlog becomes the order of trying, not a choice of lineage.
4. **Use the zone.** The factory targets the current slot from the slot's start
   (`strategy_async.go`), so during the post-branch consolidation ticks (0-8 from slot 45000)
   it already evaluates every bare branch. By tick 9 the first move is the best over all of
   them.
5. **Keep comparing during the slot.** Rounds keep all baselines alive. When the best skeleton
   on another baseline exceeds the best on the own lineage (with tag-along and delegation inputs
   counted as today), the sequencer switches: it extends its own output of an earlier slot on
   that baseline. Own milestones of the slot left on the old lineage are orphaned, which is the
   price of the switch and is accepted. The check that a new milestone of the slot must beat the
   own latest one (`task.Run`) stays.
6. **Bounded by configuration.** The number of improvement workers (today the constant
   `NumImprovementWorkers` = 3) and a bound on baselines per round become node configuration
   under `sequencer`, with today's behaviour as the default floor. Higher values buy coverage
   with CPU.

Unchanged: the branch proposer, the bootstrap and base-extend proposers, the tag-along and
delegation insertion, the mine-rule filter (deviating milestones stay excluded; its ordering
becomes the order of trying, since lineages are no longer chosen by the first pair), the
self-fork guard, the zone guards of upgrade 2.

## Interplay

- **First-move wait** (`awaitsFactoryForFirstMove`, active from slot 45000 since v1.2.1): it
  waits for the factory to have evaluated a pair. With the zone used for the search, the factory
  has results by tick 9 and the wait rarely applies. Keep it until measured, then decide whether
  to remove it.
- **Teaming up.** Groups stay legitimate. With the full search, a group's lineage wins when it is
  the heaviest, and loses to the followers' common lineage when that one is heavier. Groups have
  a break-even point in the cost of their chains against the bonuses they win.
- **Branch bonus.** Accepted as paid per branch: a group with several chains holds several
  tickets, which costs it chains. A minimum own capital to issue a branch is a possible later
  step (implemented once in the past); its bounds are open.

## Tests

- In process, multi-sequencer (`tests/`): add a pair of sequencers playing the early-cut and
  collector scheme next to the reference ones. Before the change the scheme's lineage wins most
  slots; after it, the winner is the lineage with the largest coverage at the end of the slot,
  and the reference followers converge on one branch.
- The existing `TestSequencersAcrossUpgrade2` keeps passing, under `-race` (sequencer is core).
- Factory unit tests: seeds on two baselines, the heavier one published; a lineage switch when
  another baseline overtakes.

## Measurement

On the testnet after deploy, as in `kb/research/branch-bias.md` (Reproducing): the share of slots
whose winning branch had the top coverage (89% under v1.1.3, 7-11% since 2026-10-22), winners per
issuer, and the tick of each follower's first move. Compare with the period from slot 45000 on
the current release.

## Open

- The bound on baselines per round when a slot has many branches (8 to 10 today).
- Whether switching lineage mid-slot causes churn when two lineages are close: a margin before
  switching may be needed, which would be a heuristic, not a preference.
- VRF proof grinding of the branch bonus (the next slot's message chains on the proof, which is
  not unique): a ledger fix, deferred.
