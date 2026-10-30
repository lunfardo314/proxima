# Sequencer versions in one binary

> **LIVE spec, steps 1, 2 and 4 done on `feat/seq-refactor` (2026-10-30), not merged.** Several
> sequencer versions compiled into the node, the operator choosing one by name in
> `sequencer.version`: `v1`, the release, and `v2`, the coverage-adjustment stage 1 sequencer.
> Running live since 2026-10-30 as v1.3.9 (`ee598eaf`): hboot, oseq1 and oloc2 on `v1`, hloc0
> and oloc1 on `v2`. The development workflow for a new version is in `sequencer/README.md`; this
> document is the design, the plan and the live findings.

## Why

The sequencer supplied with the node is the reference one and is quasi-optimal; improved versions of
it may follow. A new version has to be tried on a few nodes of the live network while the rest keep
the released one, and operators may choose which one to run. Until now a new sequencer lived on a long-running
branch (`develop-coverage-adjustment`), so trying it meant running a different binary, and it could
not ship until it replaced the released one everywhere.

## Design

- **`sequencer/`** keeps what every version shares:
  - the config: `ConfigOptions`, the `With...` options, `paramsFromConfig` reading the `sequencer`
    section, `NewConfigOptions` (defaults with options applied) and `ConfigOptions.Lines`;
  - the registry: `RegisterVersion(name, Constructor)`, `Versions()`, `VersionFromConfig()`,
    `DefaultVersion = "v1"`;
  - `NewFromConfig(glb, extraOpts...) (Sequencer, error)`: reads the section, returns `nil, nil`
    when the sequencer is absent or disabled, otherwise calls the constructor registered under
    `sequencer.version`; an unknown name is an error listing the compiled-in versions;
  - `Sequencer`, the interface the node holds: `Start`, `SequencerID`, `LedgerCoverage`,
    `ConsensusContribution`, `IsVertexReferenced`. Nothing else is needed to run a sequencer from
    the node, so a version may change everything behind it;
  - `delegationpool`, bookkeeping of the delegations aimed at the sequencer (discovery from the
    latest reliable branch, transitions in own milestones settled or voided against it). It holds
    no strategy: the freeze placement and selection are in the version's `task`, the acceptable
    cut comes in as a parameter. Moved up 2026-10-30;
  - `mine`, the canonical winner rule for competing mine transits (`mine_conflict_rule.md`,
    `mine_settlement_upgrade.md`): the transit order, the registry of received transits and the
    deviation check behind the endorsement refusal. It is network policy, which every sequencer
    must apply the same way or miners are treated differently by version; as copies it could only
    drift. Each version's `task/mine_transits.go` keeps only `settleMineTransits`, the adapter to its
    tag-along candidates, and `mine_rule.go` (the refusal bookkeeping and the settlement pulse) stays
    in the version. Moved up 2026-10-30, identifiers exported without the old prefix
    (`MineTransitRegistry` is `mine.Registry`, `InspectMineInclusions` is `mine.InspectInclusions`);
  - `backlog`, the tag-along backlog: collection, expiry and blacklisting of tag-along outputs,
    rejection logging, and the queries for milestones to endorse. The queries are strategy, but a
    version wanting another order writes its own. Moved up 2026-10-30, unchanged;
  - `seqdata` and `txbuilder_seq`, which are not sequencer strategy: the ledger, the core, the API,
    the consolidator and `proxi` import them.
- **`sequencer/v1/`** is the released sequencer, moved unchanged, with its subpackages `task` and
  `factory` (and `backlog` until it moved up). Package `v1`. It registers itself as `"v1"` from
  `init()`. Its `New` and `*Sequencer` stay exported: the integration tests in `tests/` build
  sequencers with `v1.New` and `sequencer.With...` options.
- **`node/sequencer_versions.go`** blank-imports each version, which is what compiles it in. The
  node logs `starting sequencer version '<name>'`.
- **Config**: `sequencer.version`, optional, default `v1`, documented in the generated
  `proxima.yaml` (`proxi/config_cmd/node_config.template`). A key only one version understands
  goes in a subsection named after it (`sequencer.v2:`).

Whole subpackages go into the version, not only the top-level files, so that copying `v1/` gives a
version that shares no code with the one it came from. A bug fix then has to go into every
version that carries the bug, which is the price of a released version staying exactly as it was
released. Code that is really the same everywhere can later move up to `sequencer/`, one piece at a
time, without changing any version's behaviour.

The Prometheus metrics are registered when a sequencer is built, under the same names in every
version. Only one sequencer runs in a node, so the copies do not clash, and dashboards work
whichever version runs.

## Behaviour of step 1

None changes. Checked on the branch:

- every file of `sequencer/v1/` equals its `develop` original (`2e1f383e`, v1.3.8p2) up to the
  package clause and the import paths of the moved subpackages, except `sequencer.go`, where
  `NewFromConfig` became the `init()` registration and the config helpers are called through
  `sequencer.`;
- `go test -race ./sequencer/...`, including a new `versions_test.go` (default, explicit and
  unknown version, disabled sequencer, double registration), and the integration tests
  `Test1SequencerPrunerIdle` and `Test3Seq1TagAlong` under `-race` pass;
- the `sequencer/README.md` recipe was run on the branch: a `v2` copy builds, passes its unit tests
  and compiles into the node next to `v1`; the trial copy was removed.

## Plan

1. **`feat/seq-refactor` from `develop`** (`2e1f383e`): the move and the registry. Built.
2. **Deploy on the live network.** Behaviour must be exactly the release's. Check: every node
   running the release sequencer logs `starting sequencer version 'v1'`, and its branch and milestone rates, missed branches and
   `proxima_seq_*` metrics stay where they were on v1.3.8p2. Deployed 2026-10-30 on oseq1, hboot,
   hloc0 and oloc2, all logging `v1`. The same day v1.3.9 (both versions, the two fixes of the
   live findings below) went on all five sequencers, two of them on `v2`.
3. **Merge `feat/seq-refactor` into `develop`.**
4. **The coverage-adjustment sequencer as `v2`.** Done 2026-10-30 on `feat/seq-refactor` itself
   instead of a separate `feat/sequencer-v2`, from `develop-coverage-adjustment` at `59b68d3b`
   (re-anchor change `41bd1b99` included). Its code changes there are all inside the old
   `sequencer/` subtree (`factory`, `task`, `sequencer.go`, `strategy_async.go`), so the port is
   mechanical: `v2` builds, passes `-race`, and equals the `develop-coverage-adjustment` sequencer
   up to the same glue as `v1`; `v1` is unchanged. The recipe, for a later port of the same kind:

   ```bash
   cp -r sequencer/v1 sequencer/v2
   (cd sequencer/v2 &&
    grep -rl 'proxima/sequencer/v1' . | xargs sed -i 's#proxima/sequencer/v1#proxima/sequencer/v2#g' &&
    grep -rlE '^package v1\r?$' . | xargs sed -i -E 's/^package v1(\r?)$/package v2\1/' &&
    sed -i 's/VersionName = "v1"/VersionName = "v2"/' sequencer.go)
   git diff 2e1f383e develop-coverage-adjustment -- sequencer/ | perl -pe '
     if (/^(diff --git|---|\+\+\+) /) { s#([ab]/)sequencer/#$1sequencer/v2/#g }
     else { s#proxima/sequencer/(task|factory|backlog)\b#proxima/sequencer/v2/$1#g;
            s#^([ +-])package sequencer(\r?)$#$1package v2$2# }' > v2.patch
   git apply v2.patch
   # node/sequencer_versions.go: add _ "github.com/lunfardo314/proxima/sequencer/v2"
   ```

   The diff is taken against `2e1f383e`, the `develop` the branch was last merged with, so it
   carries only the stage 1 changes. Its `kb/` documents (`sequencer_search.md`,
   `coverage_adjustment_B.md`, `research/early_cut_derivation.md`) came along with their code paths
   pointed at `sequencer/v2/`. Its `global/version.go` bump was not ported. From now on stage 1
   work goes on in `sequencer/v2/`; `develop-coverage-adjustment` is retired.
5. **Merge into `develop` and release** when `v2` is good. The release
   carries both, `v1` stays the default, and operators select `v2` per node. Whether `v2` becomes
   the default, and when `v1` is deleted, is decided then.

## Live findings, 2026-10-30

Two bugs common to both versions, both fixed in both, no ledger change:

- **Winners missed their next branch** (`a8f12e57`). A sequencer whose branch won the slot extends
  it first (M1), may then re-anchor onto another lineage, and the others join the winning branch
  through M1. `extendsSupersededOwnMilestone` (`task/proposer_factory.go`) treated every earlier own
  milestone of the slot as superseded and dropped every skeleton extending M1, so the chain stayed
  on the losing lineage and the next branch failed the health gate. Now an own milestone counts as
  superseded only when the latest own milestone descends from it (walked back along the own chain
  within the slot). Traced on oseq1 and confirmed in the DAG (slots 104098, 104124); detail in
  `.internal/handoff_superseded_own_milestone.md`. Live, slots without a branch: oseq1 24% to 0.8%
  (128 slots), oloc1 on `v2` 31% (the last stage 1 trial) to 5.9% (118 slots), release nodes over
  the same slots 0 to 2.5%.
- **The first milestone went out late** (`ee598eaf`). In the post-branch zone the loop fired a pulse
  every pulse interval, each failing only because no milestone may be built before tick 9, and
  each restarting the interval; the slot's first milestone then waited up to a whole interval
  (tick 10-12 in about 40% of oloc1's slots). Now no pulse fires before the post-branch
  consolidation tick and the slot's first pulse is not held by the interval. Live after the
  deploy: the first milestone at tick 9 in every slot on all five sequencers, restart slots aside.

**Why `v2` won only 2% of slots on oloc1** (against 25% on `v1`), measured on the traced stage 1
run before the second fix. `v2`'s first move takes the best pair by coverage, staying on the own
branch only when that is the best. Two candidate causes were examined:

- *A tie lost by construction* — refuted. Both options count the branch bonus: staying through the
  own branch output's balance, endorsing another branch through the baseline adjustment
  (`coverageDeltaAdjustment`, its sequencer output left unconsumed). In the one first move decided
  between bare branches, the pair beat staying by exactly the bonus difference of the two branches.
  Between bare branches the larger bonus wins, the own branch included, as intended.
- *Lateness* — confirmed. 56 of 57 traced first moves came at tick 12 or later, when the `v1`
  neighbours had already extended their own branches at tick 9. A pair endorsing such a milestone
  adds that sequencer's whole chain output (about 12.3T) and beats staying by a margin no bonus
  offsets. That was the late first milestone above, fixed in `ee598eaf`.

What remains is inherent to a mixed network: a `v1` sequencer extends its own branch whatever its
coverage, so a `v2` one whose first move comes even slightly after its neighbours' joins a backed
lineage. A minority of `v2` sequencers wins fewer of its own branches than its `v1` neighbours;
staying on `v1` pays against `v2`. The effect should shrink as `v2` spreads, and vanish only when
all sequencers follow coverage.

### Making `v2` the default

The claim is that `v2` is the better strategy and that, run by everyone, it lets the random branch
bonus decide between branches tied on coverage. The equilibrium wanted: whatever advantage a
strategy shows can be matched by others, and the stock sequencer is good enough that a better one
gains only a margin or serves a niche.

Fairness as the user defines it: a **latency** advantage is fair (being fast and close to the
centre of the network's capital); a **capital** advantage among branches tied on coverage is not.
Capital bias is measured with latency controlled: sequencers of similar arrival times, different
capital.

Measured per sequencer, `v1` against `v2`, over at least 24 hours:

- slots without a branch;
- own-branch win share, against expectation (branches issued);
- the share of slots where the winner had the largest bonus among the branches tied on coverage;
- wasted own milestones and lineage switches per slot;
- income (branch bonus, chain inflation, fees) per unit of capital;
- CPU.

`v2` becomes the default when it misses no more branches than `v1`, earns at least as much per unit
of capital, and the bonus decides more of the ties. Because of the mixed-network effect above, a
switch of all sequencers at once may serve better than node by node.

## Open points

- **Versions that disagree on the lineage.** Versions may search differently, but convergence
  needs every sequencer to prefer the state with the biggest coverage. A version that ranks by
  another measure (stage 2, `coverage_adjustment_B.md`) is activated network-wide from a slot, not
  chosen per node.
- **Tests.** `tests/` runs against `v1`. A version under development is tested there by switching
  the import in the test files concerned; there is no per-version test matrix yet.
