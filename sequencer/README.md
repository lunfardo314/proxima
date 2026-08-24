# sequencer

A sequencer is a software agent acting for one token holder. It issues
transactions on that holder's behalf, and its objective is the holder's return:
inflation on the holder's own funds, inflation on funds delegated to it, and
tag-along fees from transactions it includes.

Nothing here is altruistic, and that is the design. Proxima's cooperative
consensus rests on the claim that a sequencer pursuing its own profit converges
with every other sequencer doing the same — because the way to earn is to build
on the ledger state with the biggest coverage, and so is everyone else's. Its
corollary matters just as much: behaviour that damages the network, such as
flooding it out of greed or trying to outmanoeuvre other sequencers, works
against the holder whose funds are at stake.

There can be many sequencer implementations. This package is the reference —
standard — implementation, shipped together with the core node.

## What it must do

The governing rule is one sentence: **issue transactions with the biggest
ledger coverage achievable in the current context, given the constraints.**

Concretely, per slot, the sequencer tries to:

* issue at least one milestone;
* keep funds delegated to it frozen and generating inflation;
* consume tag-along outputs, preferring higher fees — which is also how other
  holders get their transactions into the ledger.

Missing a slot costs coverage, so avoiding downtime matters more than optimising
any single milestone.

## Ledger time: meeting in space and time

The sequencer is the only part of this package that looks at the wall clock. It
translates wall-clock time into **ledger time** and sets the target slot from
it; everything downstream — the factory in particular — reads ledger time only.

The consequence is what makes the whole thing work. Every sequencer transaction
is timestamped on the assumption that ledger time is a **global reference shared
by every node in the network**. Because they all make that assumption, all
sequencers act inside the same, relatively narrow, window of time.

That window is the precondition for cooperative consensus. To cooperate, token
holders have to come together in *space and time*: in the same slot, on the same
ledger state. Every sequencer transaction is the proof that such a meeting
happened — it endorses, it extends, it consumes, all within the window.

If sequencers holding the **majority of capital** start failing to meet in space
and time, they stop being able to build on each other's work, and consensus
fails. A straggler on its own does not break anything; it simply loses.

This is why the factory is forbidden the wall clock, and why a sequencer whose
clock drifts is not merely late but is issuing transactions into the wrong
meeting.

## How a milestone gets built

Two stages, deliberately separated.

**`factory/` — the transaction skeleton factory (TSF).** A persistent process
that continuously scans the tippool and produces *skeletons*: incremental
attachers with an extend target and endorsements, no tag-alongs, each with
strictly increasing coverage. It works within a target slot set from outside,
and reads no clock of its own.

**`task/` — proposers.** A task takes the current skeleton and turns it into an
actual proposal for a target timestamp. Three of them:

* `proposer_base.go` — extends the skeleton, and separately produces branch
  proposals at a slot boundary. Its `tryBaseExtendProposal` is the fallback when
  the factory has no skeleton: extend the sequencer's own latest milestone with
  no endorsements, but only if that improves coverage.
* `proposer_bootstrap.go` — a non-branch transaction with an **explicit
  baseline**, issued once per slot while the network is not branching. When
  every sequencer's start output is far in the past there is nothing to endorse;
  the explicit baseline breaks that deadlock, and once several sequencers are
  producing bootstrap transactions they can endorse each other and coverage
  starts growing again.

The rest of the package: `backlog/` tracks tag-along candidates, `delegationpool/`
tracks delegations targeted at this sequencer, `txbuilder_seq/` builds the actual
transactions, `own_milestones.go` and `seqdata/` hold per-sequencer state, and
`sequencer.go` runs the slot loop.

## Branches

A branch transaction sits on the slot edge and consumes the predecessor branch's
stem output, which makes all branches on one slot edge mutually conflicting by
construction. Producing one commits ledger state to the database.

The **branch inflation bonus** is derived from a VRF —
`branchInflationBonus : randomFromSeed(proof, base)` in
`ledger/def/inflation.easyfl`. It is a lottery on a proof the sequencer cannot
steer, not something earned by work.

A design that instead made branch issuance *cost* something, by requiring the
bonus to be mined, was worked out in detail and rejected. Its own analysis is
why: mining raises the cost for an honest sequencer but does nothing to stop a
malicious one from issuing zero-work branches that it knows will lose, since
every node still pays full parse, validation, attachment and past-cone
solidification before the branch is discarded. The write-up is kept at
`claude/archive/superseded/branch_cost.md`; read it before proposing anything in
that direction again.

## Conflicts, consolidation and delegation

Two live design documents govern behaviour this package implements, and neither
is restated here:

* [`claude/sequencer_conflict_resolution.md`](../claude/sequencer_conflict_resolution.md)
  — how sequencers resolve conflicting tag-alongs, and why `numSeq` rather than
  coverage is the signal that a branch consolidated properly. Records three
  reverted attempts at widening the extend-endorse search; read it before trying
  a fourth.
* [`claude/delegation_freeze_distribution.md`](../claude/delegation_freeze_distribution.md)
  — how freeze epochs are spread across the reachable window so that delegations
  do not all unfreeze at once.

## Configuration

Under `sequencer:` in `proxima.yaml`: `enable`, `name`, `chain_id`,
`controller_key_file`, `pace`, `max_branches`, `tag_along_drain_rate`,
`backlog_tag_along_ttl_slots`, `backlog_delegation_ttl_slots`,
`milestones_ttl_slots`, and the logging switches. See
`participate/run_sequencer.md` on the documentation site for the operator's
view.

## Changing this package

Sequencer strategy is the area of the codebase where a plausible improvement has
most often made things worse in ways a functional test does not catch. Three
attempts at widening the search were reverted; one greedy lineage-switch change
shipped and was reverted the same hour after it turned LRB coverage and supply
into a sawtooth.

The acceptance test for a strategy change is therefore **not** throughput. It is
that ledger coverage and supply stay flat under live load. Verify on a running
network before trusting it, and run the core tests under `-race`.
