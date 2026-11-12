# Sequencer `v4`: the last word before the branch

> **SPEC, built 2026-11-12 on `feat/seq-v4`, not rehearsed.** Changes 2.1-2.3 are in `sequencer/v4`
> (`sequencer/v4/README.md`); the in-process multi-sequencer tests pass with `v4`, but on one node no
> zone milestone has anything new to collect, so the last milestone is exercised only by the
> rehearsal of section 4. An optional, experimental sequencer version,
> copied from `v3` per the workflow in `sequencer/README.md` and selected with
> `sequencer.version: v4`; `v3` stays the default. Sequencer policy only: no ledger change, no core
> change. The design assumes the case that matters: most sequencers end up running `v4`.

## 1. Why

Measured on the live network over slots 196200-213672 (two days, about 17,500 slots), from the
`BRANCH COMMIT` log lines of three nodes, the latest reliable branch lineage and the DAG explorer:

- **Branches end in exact ties.** In 96% of slots the top branches have the same classical coverage
  to the mote. The tie goes to the biggest branch inflation bonus in 95.8% of them
  (`IsPreferredMilestoneAgainstTheOther`, `core/vertex/cmp.go`). This is the intended outcome:
  neither capital nor latency decides, a VRF draw does.
- **The exceptions come from cutting later.** In 3.8% of slots one branch is strictly heaviest.
  2.3 points of that come from one outside group of four chains, whose branch extends its own
  milestone stamped at **tick 127**. That milestone endorses the other sequencers' final
  milestones of ticks 115-124, so its past cone strictly contains theirs (checked in the DAG of
  four slots: no extra non-sequencer transactions, only other sequencers' ordinary milestones).
  Margin: a median of about 4,000 PROX of coverage, up to about 400,000. A margin of any size beats
  the bonus.
- **`v3` leaves the last ticks unused.** It stamps its final milestone at tick 121
  (`finalConsolidationLeadTicks = 7`, `sequencer/v3/strategy_async.go`) and issues the branch at the
  boundary on it. Nobody can endorse a milestone of the zone before the branch, so every
  sequencer's last milestone lands only in its own branch. Whoever endorses those finals at a
  later tick is strictly heavier.
- **Own branches miss the top.** 69% of the own founders' branches hold the top coverage. The
  active outside issuers reach 80-92%, though they branch only in slots of their choosing. On
  oseq1 the tick-121 final is skipped in 55% of slots, so the branch extends an earlier milestone.
- **Timing is not the constraint today.** `v3` reports zero late-dropped milestones per slot. Its
  branches, though, are committed about 0.85 s after those of the late-cutting group on the same
  node.

What follows if most sequencers cut later: nothing can follow tick 127, so the last milestones
cannot endorse each other. Each holds everyone's finals plus its own, and the branches go back to
exact ties. The late cutter's edge disappears, and the bonus decides again. The size of the bonus
plays no part here: it breaks exact ties and nothing else.

## 2. What `v4` changes

Three changes, each behind its own config key so it can be switched off alone. Everything else is
`v3` unchanged, in particular the factory, the ranking (adjusted score from slot 138000, D8) and
the first move.

### 2.1 A last milestone at tick 127

The pre-branch zone (the last 25 ticks, `pre_branch_consolidation_ticks`) keeps `v3`'s closing and
final milestones and adds a third:

| Milestone | Stamp | Endorses | Built |
|---|---|---|---|
| closing | first tick of the zone the pace allows | the peers' latest milestones | as in `v3` |
| final | `final_lead` ticks before the boundary (`v3`: 7) | the peers' closing milestones | retried each tick until the last milestone's build starts, and only while it leaves the pace to tick 127 |
| **last** | tick 127 | the peers' final milestones | from `last_build_lead` ticks before the boundary, stamped ahead of the clock |

- A zone milestone consumes only its chain predecessor (ledger rule since upgrade 2), so the last
  milestone only collects through endorsements. Like every milestone of a slot it must raise
  coverage (`_enforceCoverageAdvance`). When it finds nothing new it is skipped, and the branch
  extends the final milestone, as in `v3`.
- **Built ahead, stamped 127.** `v3` comments that a final built at the last tick, with one tick to
  build, was skipped in most slots. So the last milestone is not built at tick 127: its build
  starts `last_build_lead` ticks earlier with the target fixed at tick 127. That is new behaviour.
  `v3` stamps a milestone no earlier than the pace and never ahead of the wall clock
  (`tryBuildAndSubmit`). A receiving node, this one included, admits a transaction from its near
  future and attaches it when the clock reaches it (`core/core_modules/txinput_queue`, the `wait`
  counter), so the last milestone attaches at about tick 127. A build that would start in the last
  tick is not started, since it cannot finish before the boundary.
- **Pace.** From the final milestone at tick 121 to tick 127 is exactly the sequencer pace of 6.
  A final milestone stamped later would block the last one, so with the last milestone on the
  final is not issued past tick 127 minus the pace. With `final_lead` above 6 the last milestone
  has more time for the peers' finals to arrive (they arrive up to about 600 ms, 7-8 ticks, after
  their tick). Both leads are configurable so the schedule can be compared (section 4).
- **The branch** extends the freshest own attached milestone, as in `v3`. Since the last milestone
  attaches only once the clock reaches tick 127, the branch waits for it at the boundary, at most
  `lateBranchToleranceTicks` (4) past it; otherwise it would extend the final milestone and fork
  the own chain. At the tolerance's last tick the branch goes out on whatever has attached, the
  same accepted case as `v3`'s final.

### 2.2 Submit time reserved for the fallback

In `v3` every proposer of a build shares one deadline, checked once after the best proposal is
chosen (`strategy_async.go`, `LateDropped`). A slow factory finalization can therefore drop a
base-extend or bootstrap milestone that was ready in time. This matters more once builds run close
to the boundary.

`v4` records when each proposal of `task.Run` was complete. If the best one finished past the
deadline, it submits the best eligible proposal that finished before it. If no time is left to
finalize the factory skeleton within the reserve (`fallback_reserve_ms`), it skips that
finalization. A small change inside `task.Run`, no new mechanism.

### 2.3 Branch latency: measure, then decide

Before changing how the branch is built, `v4` logs, per slot in `SLOT STATS`:
- time from the boundary to the branch build start;
- build duration;
- submit time.

This shows whether the 0.85 s gap is in the build, the attachment or the gossip. Only if the
build dominates is a follow-up worth specifying: preparing the branch's inputs during the last
milestone and only finishing and signing it at the boundary.

## 3. Behaviour by share of `v4`

- **A few `v4` sequencers.** They collect the others' finals late, as the late-cutting group does
  today, and win outright where they collect more. That rewards adopting `v4`.
- **Most sequencers on `v4`.** The last milestones cannot see each other. Each branch holds every
  final plus its own last milestone, which adds nothing others lack, since it consumes only its
  chain predecessor. Branches tie exactly and the bonus decides. The edge of cutting later is gone
  because there is no later tick.
- **Left-out milestones.** In `v3` each sequencer's final is left out of every branch but its own;
  in `v4` its last milestone is. The number of left-out milestones per slot stays the same. So,
  forecast rather than measured, the split-slot rate (`kb/research/split_slots.md`, on `feat/split_slots`) should stay
  near today's 0.7-0.9% of slots.
- **Load.** One more milestone per sequencer per slot, landing at tick 127 together with the
  branches at the boundary: with about 60 sequencers, about 60 milestones and about 70 branches
  within some 160 ms.

## 4. Experiment and acceptance

**Config** (the `sequencer` section, read by `v4` only):

| Key | Default | Meaning |
|---|---|---|
| `v4.last_milestone` | true | change 2.1 on or off |
| `v4.final_lead` | 7 | ticks before the boundary for the final milestone |
| `v4.last_build_lead` | 2 | ticks before the boundary the last milestone's build starts |
| `v4.fallback_reserve_ms` | 150 | change 2.2; 0 turns it off |

**Order.** One change at a time. Change 2.1 alone first, then 2.2. Change 2.3 is logging only and
ships with the first.

**Stages.**
1. **Local multi-node rehearsal.** A mixed network, first a `v4` minority, then a `v4` majority,
   with the `final_lead` / `last_build_lead` pairs 7/2, 10/2 and 12/3.
2. **One own founder on `v4` for a day.** Compared against the other four over the same slots.
3. **Decision on a wider rollout.**

**Measures,** with the scripts that produced section 1:
- share of slots with an exact tie at the top, and how often the bonus decides it;
- share of slots won by a strictly heavier branch, by chain, with the margin;
- own branches at the top (69% today) and wins per chain;
- last and final milestones issued, skipped, late-dropped;
- branch commit time against other issuers;
- split-slot rate and branch health;
- load at the boundary: `catchup_wait`, attachers, memDAG size, `proxima_general_gauge_wait`.

**Accept** if, with a `v4` majority:
- exact ties at the top are at least as frequent as today;
- strictly heavier wins fall below today's level;
- late drops stay near zero;
- the split-slot rate does not rise.

## 5. Out of scope

- Search changes: wider search, keeping plans across own-head changes, caches of coverage or
  score. They were tried before and hurt convergence (`archive/shipped/sequencer_conflict_resolution.md`).
- Branching only when the bonus is high. Under a tie-break by the largest bonus it raises the win
  rate per branch, not the number of wins, and founders branch every slot for liveness.
- The first move after a lost branch race (`kb/research/split_slots.md`, on `feat/split_slots`).
- Any ledger or core change. If the last milestone needs one, it is out of `v4`.

## 6. Open

1. **Answered:** a transaction stamped ahead is held and attached at its time, not refused
   (section 2.1). Still to measure in the rehearsal: how much the wait for the last milestone
   delays the branch (`branch start` in `SLOT STATS`).
2. Why the `v3` final is skipped in 55% of slots on oseq1, and whether the branch then extends a
   milestone that collected less. This should be diagnosed on `v3` before `v4` is measured against
   it.
3. Whether the 8-endorsement cap lets the last milestone collect every peer's final in a network of
   about 60 branching sequencers, or whether a fourth level of collecting appears.
