# Sequencer versions in one binary

> **LIVE spec, step 1 built on `feat/seq-refactor` (2026-10-30), not merged.** Several sequencer
> versions compiled into the node, the operator choosing one by name in `sequencer.version`.
> The development workflow for a new version is in `sequencer/README.md`; this document is the
> design and the plan for getting the coverage-adjustment sequencer onto it as `v2`.

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
  - `seqdata` and `txbuilder_seq`, which are not sequencer strategy: the ledger, the core, the API,
    the consolidator and `proxi` import them.
- **`sequencer/v1/`** is the released sequencer, moved unchanged, with its subpackages `task`,
  `factory` and `backlog`. Package `v1`. It registers itself as `"v1"` from
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
2. **Deploy on the live network.** Behaviour must be exactly the release's. Check: every node logs
   `starting sequencer version 'v1'`, and its branch and milestone rates, missed branches and
   `proxima_seq_*` metrics stay where they were on v1.3.8p2.
3. **Merge `feat/seq-refactor` into `develop`.**
4. **`feat/sequencer-v2` from `develop`**: the coverage-adjustment sequencer as `v2`. Its code
   changes on `develop-coverage-adjustment` are all inside today's `sequencer/` subtree
   (`factory`, `task`, `sequencer.go`, `strategy_async.go`), so the port is mechanical. Rehearsed
   on `feat/seq-refactor`: the result builds, passes `-race`, and equals the
   `develop-coverage-adjustment` sequencer up to the same glue as `v1`.

   ```bash
   cp -r sequencer/v1 sequencer/v2
   (cd sequencer/v2 &&
    grep -rl 'proxima/sequencer/v1' . | xargs sed -i 's#proxima/sequencer/v1#proxima/sequencer/v2#g' &&
    grep -rlE '^package v1\r?$' . | xargs sed -i -E 's/^package v1(\r?)$/package v2\1/' &&
    sed -i 's/VersionName = "v1"/VersionName = "v2"/' sequencer.go)
   git diff develop...develop-coverage-adjustment -- sequencer/ | perl -pe '
     if (/^(diff --git|---|\+\+\+) /) { s#([ab]/)sequencer/#$1sequencer/v2/#g }
     else { s#proxima/sequencer/(task|factory|backlog)\b#proxima/sequencer/v2/$1#g;
            s#^([ +-])package sequencer(\r?)$#$1package v2$2# }' > v2.patch
   git apply v2.patch
   # node/sequencer_versions.go: add _ "github.com/lunfardo314/proxima/sequencer/v2"
   ```

   Apply the diff against the `develop` the branch was based on, so it carries only the stage 1
   changes. If `develop-coverage-adjustment` moves on, regenerate the patch. Its `kb/` changes
   (`sequencer_search.md`, `coverage_adjustment_B.md`, `research/early_cut_derivation.md`) are
   merged as documents; its paths `sequencer/factory` and `sequencer/task` then mean
   `sequencer/v2/...`. Its `global/version.go` bump is not ported. After the port,
   `develop-coverage-adjustment` is retired and work goes on in `sequencer/v2/`.
5. **Merge `feat/sequencer-v2` into `develop` and release** when `v2` is good. The release
   carries both, `v1` stays the default, and operators select `v2` per node. Whether `v2` becomes
   the default, and when `v1` is deleted, is decided then.

## Open points

- **A fix pending for `v1`**: `.internal/handoff_superseded_own_milestone.md` (2026-10-30), a
  winning sequencer missing its next branch because `extendsSupersededOwnMilestone`, now in
  `sequencer/v1/task/proposer_factory.go`, drops a legitimate return to its first milestone. Kept
  out of step 1, which must not change behaviour; it is a separate fix after step 2, and has to go
  into `v2` as well, which carries the same filter.
- **Versions that disagree on the lineage.** Versions may search differently, but convergence
  needs every sequencer to prefer the state with the biggest coverage. A version that ranks by
  another measure (stage 2, `coverage_adjustment_B.md`) is activated network-wide from a slot, not
  chosen per node.
- **Tests.** `tests/` runs against `v1`. A version under development is tested there by switching
  the import in the test files concerned; there is no per-version test matrix yet.
