# sequencer: versions

The sequencer supplied with the node is the reference one, and it is
quasi-optimal. Improved versions of it may follow. So the node carries
**several sequencer versions in one binary**, and the operator picks one by
name in the node config:

```yaml
sequencer:
  enable: true
  version: v1      # optional, default v1
  ...
```

A version only decides *how* a sequencer chooses what to issue. Every version
issues ordinary sequencer transactions under the same ledger rules, so versions
differ in strategy, never in what the ledger accepts.

What a sequencer must do, and how the reference one does it, is in
[`v1/README.md`](v1/README.md).

## Layout

| Path | What it is |
|------|------------|
| `sequencer/` | What every version shares: the config (`ConfigOptions`, the `With...` options, reading the `sequencer` section), the version registry, and the small `Sequencer` interface the node uses |
| `sequencer/delegationpool` | Bookkeeping of the delegations aimed at the sequencer: discovered from the latest reliable branch, transitions in own milestones settled or voided. Shared: where and what to freeze is the version's decision (`v1/task`) |
| `sequencer/v1/` | Version `v1`, the released sequencer, with its own `task`, `factory` and `backlog` |
| `sequencer/seqdata`, `sequencer/txbuilder_seq` | Sequencer chain data and sequencer transaction building. Used by the ledger, the core, the API and `proxi`, so they are not part of any version |
| `node/sequencer_versions.go` | The list of versions compiled into the node, one blank import each |

The node sees a sequencer only through `sequencer.Sequencer`: `Start`,
`SequencerID`, `LedgerCoverage`, `ConsensusContribution`,
`IsVertexReferenced`. Anything else a version does is its own business.

## How a version is selected

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

## Developing a new version

A new version starts as a **copy of an existing one**, not as a branch of it. The
version it is copied from keeps running unchanged on every node that selects it,
so the new one can be tried on a few nodes of the live network while the rest
stay on the released one.

1. Copy the whole package:

   ```bash
   cp -r sequencer/v1 sequencer/v2
   ```

2. Rename it inside the copy, the package clause and the imports of its own
   subpackages:

   ```bash
   cd sequencer/v2
   grep -rl 'proxima/sequencer/v1' . | xargs sed -i 's#proxima/sequencer/v1#proxima/sequencer/v2#g'
   grep -rlE '^package v1\r?$' . | xargs sed -i -E 's/^package v1(\r?)$/package v2\1/'
   ```

   and set `VersionName = "v2"` in `v2/sequencer.go`.

3. Compile it in: add `_ "github.com/lunfardo314/proxima/sequencer/v2"` to
   `node/sequencer_versions.go`.

4. Check that the copy builds and passes the copied unit tests
   (`go test -race ./sequencer/v2/...`), and that `v1` is untouched
   (`git diff --stat -- sequencer/v1` is empty).

5. Now change `v2`. Run it on a node with `sequencer.version: v2`.

The integration tests in `tests/` build sequencers with `v1.New`. To run one of
them against the new version, import `v2` in that test instead; when a version
becomes the default, move `tests/` to it.

## Rules

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

## Caveat: versions on one network

Versions are free to differ in how they search, but sequencers converge
because each takes the ledger state with the biggest coverage. A version that
ranks candidates by a different measure can disagree with the others on which
lineage to build on. Such a version is activated network-wide from a slot (see
`kb/coverage_adjustment_B.md`), not per node.
