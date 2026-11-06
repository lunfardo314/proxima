# Sequencer decommissioning

> Phase A BUILT on `feat/seq-decommission` 2026-11-06, not yet released; phase B a draft (spec written
> and revised the same day). Two
> phases. **Phase A**, this document: an upper bound on the sequencer's frozen coverage, set in its
> sequencer data. It is node and wallet policy, needs no ledger change, and is to be implemented and
> shipped now. **Phase B**, `proxi node seq unfreeze`: the controller stops its own sequencer's
> freezes early and takes back the unearned advance. It needs a change to the delegate lock, so it
> waits as a draft in `ledger_upgrade_backlog.md` until a library upgrade takes it. Phase A touches
> `sequencer/seqdata`, `sequencer/txbuilder_seq`, the proposers of every sequencer version,
> `ledger/txbuildercore/seq_rating.go`, `api`, `consolidator`, `proxi/node_cmd/seq_cmd` and
> `proxi/node_cmd/delegate`.

## The problem

A sequencer that freezes delegations prepays each delegator the delegator's share of the
inflation for the whole frozen span (the *advance*), up to 60 epochs of 600 slots, about 4.3 days.
It earns the advance back slot by slot through the inflation on its frozen coverage. Stopping such
a sequencer is bad for both sides:

- **The sequencer** loses the part of every advance the remaining span will not earn back. A
  stopped chain earns no inflation.
- **The network** loses the sequencer's coverage at once, its own balance and every frozen
  delegation with it. The delegators' tokens stay locked until their freezes run out, contributing
  nothing, since nobody freezes them again.

Stopping costs nothing when nothing is frozen: the controller stops the node and ends the chain
with `proxi node endchain`. Decommissioning is therefore the work of getting from "many frozen
delegations" to "none" without either loss.

There are two speeds:

- **Slow (phase A).** The controller sets the bound to 0. The sequencer freezes nothing more, every
  freeze runs out within at most 60 epochs, and the sequencer loses nothing. Its coverage fades out
  epoch by epoch as the freezes expire, not all at once.
- **Fast (phase B).** The controller has the sequencer put its frozen delegations on hold now, and
  the sequencer takes back the unearned part of each advance within a bound the delegate lock
  enforces. The coverage drops at once. This is for when four days is too long.

A bound is more general than an on/off switch. Between 0 and unlimited, it lets an operator cap how
much delegated capital the sequencer takes on: what it can afford to advance, or what it wants to
hold before winding down gradually.

## The bound: `max_frozen`

### The key

A new key in the sequencer data on the sequencer's chain output (`sequencer/seqdata`):

```go
// MaxFrozen is the most frozen coverage, in motes, the sequencer takes on: it
// freezes a delegation only while its frozen coverage stays at or under it.
// nil (key absent) means unlimited; 0 means it freezes nothing, so once the
// freezes it holds run out it can be stopped at no loss.
MaxFrozen *uint64 `json:"max_frozen,omitempty"`
```

A pointer, because absent and 0 mean opposite things: `omitempty` leaves out a nil pointer, never a
pointer to 0. The bytes stay deterministic, as `MarshalJSON` requires. Added to `knownKeys`.
`Clone` copies the value, not the pointer.

Set with `proxi node seq set-params --max_frozen <motes>` and removed with
`proxi node seq set-params --max_frozen unlimited` (a string flag), through the same `set sequencer data`
request (code 2) as every other key. It is therefore authorised by the controller and takes effect
from the milestone that consumes the request. `--json` carries it like any key. `seq info`, the chain
explorer, the API sequencer list (`server.go`, beside `EnforceFreezeBounds`) and the monitor show
it, with the current frozen coverage beside it.

It is unrelated to the existing `freeze_bounds` key, which enforces the ledger's coverage
contribution upper bound (`CoverageContributionUpperBound`) on balance plus frozen coverage. When
both are set, both apply.

### What it bounds

The sequencer's frozen coverage is cell 0 of the frozen-coverage vector on its chain output
(`FrozenCoverage(0)`, `AmountIndexFrozenCoverage`): the delegated amount frozen in the current
epoch. It rises when the sequencer freezes or tops up a delegation, and falls when a freeze runs
out or is stopped.

### The sequencer

Read off the sequencer's own predecessor output (`origSeqData`), like every other key.

- **Freezes.** `SeqTxBuilder.FreezeDelegation` already computes `projectedFrozen0`, the cell 0
  after the freeze, for the `freeze_bounds` check. With the bound set, a freeze that would take
  `projectedFrozen0` above it is skipped with `valid = true`. The delegation is not blacklisted, and
  is frozen once there is room again. The freeze pass goes on to the next candidate, since a smaller
  delegation may still fit. With the bound at 0 the proposers' `insertDelegations`
  (`sequencer/v1`, `v2`, `v3`) return before reading the pool, so no candidate is fetched. The
  bound holds in bootstrap mode too, which otherwise spends the whole budget on refreezing.
- **Top-ups.** A top-up request (code 4) is a freeze: it freezes the delegation, or adds to its
  frozen amount. The same check applies to it, through the same cell. A top-up that does not fit
  waits in the backlog for room, as any request the current milestone cannot take, and its sender
  can reclaim it from `constTagAlongSlots` (30 slots) on.
- **Tag-alongs.** Nothing else changes. Plain tag-alongs, fees, mine transits, set-params,
  withdraw and askstop requests are handled as before, and the sequencer stays an ordinary
  tag-along target. The delegation pool keeps tracking every delegation, frozen or not.

Raising or removing the bound resumes freezing at the next milestone. Delegations whose freezes ran
out in the meantime are frozen again normally, unless their masters moved them.

### The wallet

A sequencer with no room is never a delegation target. The tag-along draw is not affected.

- **Rating.** `SequencerCandidate` gets `MaxFrozen *uint64`, filled by `api.SequencerCandidate`
  from the seq data. Its room is `MaxFrozen - FrozenCoverage`, or unlimited when nil, and 0 when the
  frozen coverage is above the bound, as it is right after the bound is lowered. `DelegationCandidates`
  drops a candidate with no room, as it drops one leaving delegators nothing. That one filter reaches
  `proxi node delegate` (random target), the consolidator, the miner's reward placement and the
  API ratings, and so `proxi node seq_rating` and the monitor.
- **Amount.** A wallet placing a known amount (`delegate`, `delegate topup`, the consolidator, the
  miner's new-delegation and top-up options) also drops a candidate whose room is below that amount.
  The room is read off the LRB state and moves as freezes run out, so it is an estimate; the
  sequencer's check is the one that holds.
- **Pinned target.** `proxi node delegate <amount> -t <seq>` and `delegate topup` to a sequencer
  without room for the amount refuse, with a message giving the bound and the frozen coverage.
  `SelectDelegationTarget` with such a pinned target returns an error: a consolidator pinned to it
  defers, as it does for a pinned target that is inactive, and a miner falls back to the draw.
- **Consolidator.** A spendable delegation (unfrozen or on hold) whose target has no room for it is
  re-delegated at the next tick, without waiting the one epoch the consolidator otherwise allows an
  unfrozen delegation. That brings the delegator's tokens back into coverage at another sequencer.

The ledger still lets anyone create a delegation to any sequencer. One to a sequencer without room
stays unfrozen, and its master can take it back at any time.

## Decommissioning with phase A

1. `proxi node seq set-params --max_frozen 0`.
2. Wait until nothing is frozen, at most 60 epochs, about 4.3 days. Delegators who want out
   earlier can still askstop.
3. Wait until no tag-along requests addressed to the sequencer are left unconsumed, and withdraw the
   balance (`proxi node seq withdraw`) while the node still runs, since withdrawal is a request.
4. Stop the node, then end the chain with `proxi node endchain`. Ending the chain while the node runs
   makes the two compete for the chain output.

## What else decommissioning needs

The first three belong to phase A; the rest are notes.

1. **Decommissioning status in `proxi node seq info`.** One block: the bound and the frozen
   coverage; the frozen delegations (count, amount); the slot by which the last freeze runs out, as a
   date; the unearned advance that would be lost by stopping now; and the tag-along requests addressed
   to the sequencer still unconsumed in the LRB state (they are indexed under its chain ID). The
   last line answers "can I stop now": nothing frozen and no pending requests.
2. **The runbook above, on the docs site's `run_sequencer` page.**
3. **Keep the capital in coverage.** The sequencer's own balance is usually its biggest
   contribution to coverage. Withdrawn to the controller as a plain output, it contributes nothing.
   The runbook should say to delegate it to other sequencers (`proxi node delegate`, or the
   consolidator in `autodelegate` mode), where it counts again once frozen. That is the main lever
   for "minimum impact on coverage", more than anything done with the delegations.
4. **Delegators left unfrozen.** Their tokens are safe; the master path does not need the target to
   exist. Masters running the consolidator are re-delegated automatically. Others earn nothing until
   they act. `proxi node delegate status` should flag a delegation whose target has no room, is
   inactive or is gone, and say what to do.
5. **Bootstrap mode.** A sequencer with the bound at 0 does not refreeze in bootstrap mode either. A
   founder with a large share of coverage should not set it while the network is recovering from an
   outage.
6. **Succession instead of decommissioning.** When the operator only wants to stop running the node,
   not to take the capital out, handing the chain to a new holder (`control_succession.md`) keeps
   every freeze and every delegation in place, with no impact on coverage. That spec is not built;
   decommissioning is the fallback until it is.
7. **Phase B**, the fast path: `ledger_upgrade_backlog.md`, section "Controller-side unfreeze".

## Tests

- `sequencer/seqdata`: absent, 0 and a value round-trip to the same bytes; absent reads as
  unlimited and 0 as 0; `Clone` does not share the pointer.
- `txbuilder_seq`: with a bound, a freeze that would exceed it is skipped and leaves the builder
  unchanged, while a smaller one that fits is taken; a top-up is held back the same way. At 0 nothing
  is frozen, and tag-along requests other than top-ups are taken as before.
- `txbuildercore`: `DelegationCandidates` drops a candidate with no room. A placement with an amount
  drops one with too little room. A pinned target without room errors.
- `tests/`: a multi-sequencer run in which one sequencer is set to 0 and its delegations run out and
  are not refrozen, while it keeps consuming tag-alongs; then the bound is removed and it freezes
  again. Core tests under `-race`.
