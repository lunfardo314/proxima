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

The sequencer supplied with the node is the reference one, and it is
quasi-optimal. Improved versions of it may follow, so the node carries several
sequencer versions in one binary (see [Versions](#versions)). This document
describes what every version does and how the reference one is built; each
version's own README says where it differs.

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

The sequencer is the only part of the node's issuing code that looks at the
wall clock. It translates wall-clock time into **ledger time** and sets the
target slot from it; everything downstream — the factory in particular — reads
ledger time only.

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

In the reference sequencer, and in every version copied from it, two stages,
deliberately separated.

**`factory/` — the transaction skeleton factory (TSF).** A persistent process
that continuously scans the tippool and produces *skeletons*: incremental
attachers with an extend target and endorsements, no tag-alongs, each with
strictly increasing coverage. It works within a target slot set from outside,
and reads no clock of its own.

**`task/` — proposers.** A task takes the current skeleton and turns it into an
actual proposal for a target timestamp:

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

The rest of a version: `own_milestones.go` holds per-sequencer state,
`mine_rule.go` applies the mine rule to endorsement targets, and `sequencer.go`
and `strategy_async.go` run the slot loop. Shared by all versions: `backlog/`
tracks tag-along candidates and the milestones to endorse, `mine/` is the
canonical winner rule for competing mine transits, `delegationpool/` tracks
delegations targeted at this sequencer, `txbuilder_seq/` builds the actual
transactions and `seqdata/` is the sequencer chain data.

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
`kb/archive/superseded/branch_cost.md`; read it before proposing anything in
that direction again.

## Conflicts, consolidation and delegation

Two design documents govern behaviour the sequencer implements, and neither is
restated here:

* [`kb/archive/shipped/sequencer_conflict_resolution.md`](../kb/archive/shipped/sequencer_conflict_resolution.md)
  — how sequencers resolve conflicting tag-alongs, and why `numSeq` rather than
  coverage is the signal that a branch consolidated properly. Records three
  reverted attempts at widening the extend-endorse search; read it before trying
  a fourth.
* [`kb/archive/shipped/delegation_freeze_distribution.md`](../kb/archive/shipped/delegation_freeze_distribution.md)
  — how freeze epochs are spread across the reachable window so that delegations
  do not all unfreeze at once.

## Configuration

Under `sequencer:` in `proxima.yaml`: `enable`, `version`, `name`, `chain_id`,
`controller_key_file`, `pace`, `max_branches`, `tag_along_drain_rate`,
`backlog_tag_along_ttl_slots`, `backlog_delegation_ttl_slots`,
`milestones_ttl_slots`, and the logging switches. See
`participate/run_sequencer.md` on the documentation site for the operator's
view.

## Changing a sequencer

Sequencer strategy is the area of the codebase where a plausible improvement has
most often made things worse in ways a functional test does not catch. Three
attempts at widening the search were reverted; one greedy lineage-switch change
shipped and was reverted the same hour after it turned LRB coverage and supply
into a sawtooth.

The acceptance test for a strategy change is therefore **not** throughput. It is
that ledger coverage and supply stay flat under live load. Verify on a running
network before trusting it, and run the core tests under `-race`.

## Versions

The operator picks a version by name in the node config:

```yaml
sequencer:
  enable: true
  version: v1      # optional, default v1
  ...
```

A version only decides *how* a sequencer chooses what to issue. Every version
issues ordinary sequencer transactions under the same ledger rules, so versions
differ in strategy, never in what the ledger accepts.

### Layout

| Path | What it is |
|------|------------|
| `sequencer/` | What every version shares: the config (`ConfigOptions`, the `With...` options, reading the `sequencer` section), the version registry, and the small `Sequencer` interface the node uses |
| `sequencer/backlog` | The tag-along backlog: collection, expiry and blacklisting of tag-along outputs, and the queries for milestones to endorse (`CandidatesToEndorseSorted`/`Shuffled`). A version wanting another endorsement order writes its own query |
| `sequencer/mine` | The canonical winner rule for competing mine transits (`kb/mine_conflict_rule.md`, `kb/mine_settlement_upgrade.md`): the transit order, the registry of received transits, the deviation check behind the endorsement refusal. Network policy, so every version applies the same one; each version's `task` keeps only the adapter to its candidates (`settleMineTransits`) |
| `sequencer/delegationpool` | Bookkeeping of the delegations aimed at the sequencer: discovered from the latest reliable branch, transitions in own milestones settled or voided. Shared: where and what to freeze is the version's decision (its `task`) |
| `sequencer/v1/` | Version `v1`, the released reference sequencer, with its own `task` and `factory`. The default. [`v1/README.md`](v1/README.md) |
| `sequencer/v2/` | Version `v2`, `v1` with the strategy by zone of the coverage adjustment, stage 1. Not released. [`v2/README.md`](v2/README.md) |
| `sequencer/seqdata`, `sequencer/txbuilder_seq` | Sequencer chain data and sequencer transaction building. Used by the ledger, the core, the API and `proxi`, so they are not part of any version |
| `node/sequencer_versions.go` | The list of versions compiled into the node, one blank import each |

The node sees a sequencer only through `sequencer.Sequencer`: `Start`,
`SequencerID`, `LedgerCoverage`, `ConsensusContribution`,
`IsVertexReferenced`. Anything else a version does is its own business.

### How a version is selected

Each version registers itself from `init()` of its package:

```go
func init() {
	sequencer.RegisterVersion("v1", func(glb *workflow.Workflow, seqID base.ChainID, opts ...sequencer.ConfigOption) (sequencer.Sequencer, error) {
		...
	})
}
```

`sequencer.NewFromConfig` reads the `sequencer` section, looks up
`sequencer.version` (default `sequencer.DefaultVersion`, `v1`) and calls that
constructor. An unknown name is a start-up error listing the versions that are
compiled in. The node logs `starting sequencer version '<name>'`.

A version is compiled in only if something imports its package, which is what
`node/sequencer_versions.go` is for.

### Developing a new version

A new version starts as a **copy of an existing one**, not as a branch of it. The
version it is copied from keeps running unchanged on every node that selects it,
so the new one can be tried on a few nodes of the live network while the rest
stay on the released one. Below, `v3` is made from `v2`.

1. Copy the whole package:

   ```bash
   cp -r sequencer/v2 sequencer/v3
   ```

2. Rename it inside the copy, the package clause and the imports of its own
   subpackages:

   ```bash
   cd sequencer/v3
   grep -rl 'proxima/sequencer/v2' . | xargs sed -i 's#proxima/sequencer/v2#proxima/sequencer/v3#g'
   grep -rlE '^package v2\r?$' . | xargs sed -i -E 's/^package v2(\r?)$/package v3\1/'
   ```

   set `VersionName = "v3"` in `v3/sequencer.go`, and rewrite `v3/README.md`
   to say what the new version changes.

3. Compile it in: add `_ "github.com/lunfardo314/proxima/sequencer/v3"` to
   `node/sequencer_versions.go`.

4. Check that the copy builds and passes the copied unit tests
   (`go test -race ./sequencer/v3/...`), and that `v2` is untouched
   (`git diff --stat -- sequencer/v2` is empty).

5. Now change `v3`. Run it on a node with `sequencer.version: v3`.

The integration tests in `tests/` build sequencers with `v1.New`. To run them
against another version, switch every `sequencer/v1...` import in the test
files concerned (`test_util.go`, and `claude_factory_test.go` also imports
`v1/factory`); when a version becomes the default, move `tests/` to it.

### Rules

* **A released version is frozen.** It changes only for bug fixes, which keep
  it doing what it was released to do. Improvements go into a new version.
* **A bug fix goes into every version that has the bug.** Copies share no code,
  so a fix in one does not reach the others.
* **Versions share no state.** Nothing package-level in a version may assume
  it is the only copy: package-level variables are per copy, and only one
  sequencer runs in a node. The Prometheus metrics are registered when the
  sequencer is built, under the same names in every version, so dashboards
  work whichever version runs.
* **Shared code moves up only when it is really shared.** A part identical in
  every version and not expected to differ can move from `vN/` to
  `sequencer/`. Moving it must not change the behaviour of any version, and
  after the move no version may need its own variant of it.
* **Shared documentation lives here.** A version's README says only what is
  particular to it.
* **Config.** Every version gets the same `ConfigOptions`. An option general
  to sequencers goes there. An option that only one version understands is
  read by that version, from a subsection named after it (`sequencer.v2:`),
  so a node switching versions does not misread another version's keys.
* **A retired version is deleted** once no operator needs it: remove its
  directory and its line in `node/sequencer_versions.go`. Whether a version
  is still in use on the live network is a decision for the maintainers.
* **Strategy only.** A version must not need a ledger change. If a new
  strategy does, the ledger change goes through a library upgrade like any
  other, and versions that predate it must keep working after it.

### Caveat: versions on one network

Versions are free to differ in how they search, but sequencers converge
because each takes the ledger state with the biggest coverage. A version that
ranks candidates by a different measure can disagree with the others on which
lineage to build on. Such a version is activated network-wide from a slot (see
`kb/coverage_adjustment_B.md`), not per node.
