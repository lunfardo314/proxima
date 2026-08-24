# Known-baseline attacher — bounding recursive catch-up (2026-06-25)

## Problem

A far-behind access node doing forward sync floods the attacher pool and OOMs: forward
sync attaches a branch, its past cone contains sequencer milestones, and each milestone's
attacher independently runs **baseline solidification** — following the milestone's baseline
DIRECTION (chain predecessor / endorsement[0]) back toward a `Good` branch. On a far-behind
node those direction vertices are rooted-but-not-`Good`, so the wave re-solidifies the whole
milestone chain backward, unbounded (non-branch deps are not depth-capped). See
`forward_sync_lineage_nonstitch.md` for the original diagnosis.

A first attempt (`determineBaseline`, commit `69baef99`, reverted `d103b1ea`) weakened the
invariant — taking a dependency's baseline before it was `Good`. It did not fix the runaway and
introduced cross-attacher reads of not-yet-`Good` state. Reverted.

## Approach — keep "baseline must be Good", change how the baseline is MANAGED

The invariant stays: an attacher still solidifies its baseline to `Good` before traversing its
past cone. What changes is that the **baseline is propagated down** to sequencer dependencies, so
a recursive catch-up roots at the committed branch instead of re-deriving each dependency's
baseline.

`solidifyBaseline` ALWAYS runs — a dependency determines its OWN (correct, possibly newer) baseline.
A caller-provided baseline is a **floor** (`a.providedBaseline`), not an override: it bounds the
backward pull so the recursion stops at the committed frontier instead of fully attaching every
not-yet-`Good` predecessor.

> An earlier variant used the provided baseline AS the dependency's baseline (skipping
> `solidifyBaseline`). That was wrong: a delta milestone that endorses a newer branch has *that*
> branch as its baseline, so forcing the parent's older baseline produced `conflicting branch
> endorsement`. Surfaced live on hloc0-acc, 2026-06-26. Replaced by the floor model below.

How the floor bounds `solidifyBaselineUnwrapped`:
- direction is a committed branch (newer than the floor) → resolves via the `Good` fast-path to the
  dependency's real, newer baseline (no override);
- direction is a not-yet-`Good` predecessor already committed in the floor
  (`BranchKnowsTransaction(floor, dir)`) → terminal: adopt the floor as the baseline (a superset state
  of the dependency's own, so sound) and stop, instead of pulling and fully attaching the committed
  predecessor (the cascade that floods);
- otherwise pull — bounded, because the floor propagates down the direction chain too.

### Baseline is a `WrappedTx` property

`baselineBranchID *base.TransactionID` moved off `Vertex.BaselineBranchID` /
`DetachedVertex.BranchID` up to `WrappedTx`. It is now a property of any vertex type — crucially
**including VirtualTx** — so a not-yet-attached dependency can carry a provided baseline. A branch
is its own baseline; `BaselineBranch()` is a single vid-level read (no more VirtualTx panic).

### `AttachTxID(WithBaselineFloor(branch))`

For a NEW non-branch sequencer txid with a provided baseline, `AttachTxID` simply **records the
baseline** on the (virtual, Undefined) vid. Its attacher, if started, reads it as the floor.

It does NOT read the baseline's committed state to mark a rooted dependency Good outright. That was
considered and dropped as redundant: `defineInTheStateStatus` runs the same in-state check during
traversal and is the authoritative one — it also walks pending branches and handles TxID TTL expiry,
and caches the result — while `pullIfNeeded` already skips an in-state dependency, so a rooted dep
never spawns an attacher regardless. Doing it in `AttachTxID` would be a redundant, cruder
(`GetStateReaderForTheBranch` can trigger a lazy commit), one-shot DB read on an otherwise lock-only
path. So rooted deps are resolved exactly as before (in-state ⇒ not pulled ⇒ no attacher); the only
new thing is the baseline carried for the delta deps.

### Propagation

`solidifyPastCone` passes `WithBaselineFloor(a.pastCone.GetBaseline())` to every **sequencer**
dependency `AttachTxID` — inputs whose producer is a seq tx, and endorsements (always seq) —
via the `depAttachOpts` helper (branch deps excluded). `solidifyBaselineUnwrapped` also passes the
floor down the direction chain. `newMilestoneAttacher` reads the vid's `ProvidedBaseline()` and hands
it to `newPastConeAttacher(…, baseline)`, which keeps it as the floor `a.providedBaseline`; `run()`
still runs `solidifyBaseline`.

### Net effect

Forward sync solidifies the branch's baseline once; that committed baseline flows down the whole
past cone as a floor. Rooted deps terminate via the existing in-state path (`defineInTheStateStatus`
⇒ not pulled ⇒ no attacher). Non-rooted delta deps determine their own (real) baseline but stop the
backward pull at the floor — bounded by the real delta to the committed baseline, not the unbounded
backward re-solidification.

## Files

- `core/vertex/types.go` — `baselineBranchID` on `WrappedTx`; removed from `Vertex`/`DetachedVertex`.
- `core/vertex/vid.go` — `BaselineBranch()` (vid read), `GetBaselineBranchIDNoLock` /
  `SetBaselineBranchIDNoLock` / `ProvidedBaseline`; locked `SetFlagsUp` (see below).
- `core/vertex/vertex.go`, `virtual_tx.go`, `vid_debug.go` — drop the per-Vertex/Detached field.
- `core/attacher/types.go` — `WithBaselineFloor` option.
- `core/attacher/attach.go` — `AttachTxID` records the provided baseline on a new sequencer dep.
- `core/attacher/attacher.go` — `newPastConeAttacher(…, baseline)` keeps the floor `a.providedBaseline`;
  `solidifyBaselineUnwrapped` floor-bound + direction propagation; `depAttachOpts`; propagation at the
  input/endorsement sites.
- `core/attacher/attacher_milestone.go` — `run()` always runs `solidifyBaseline`; `newMilestoneAttacher`
  passes `ProvidedBaseline()` as the floor.
- `core/attacher/attacher_incremental.go` — `newPastConeAttacher(…, nil)`.

Also re-applied the independent pre-existing `FlagVertexConstraintsValid` flag-word lock
(`WrappedTx.SetFlagsUp` at the seq-validation site) — it lived in the reverted `determineBaseline`
commit, and CLAUDE.md requires `-race` clean for core changes.

## Concurrency

`baselineBranchID` is written lock-free (baseline solidification, sequenced before the vid becomes
`Good`; or at `AttachTxID` creation under the global lock, before the vid is shared) and read under
the read lock (`BaselineBranch` / `ProvidedBaseline`). Cross-attacher reads only happen after the
writer is `Good`, so the `Good` transition (taken under the vid lock) is the happens-before barrier
— same pattern the pre-move `Vertex.BaselineBranchID` relied on. `-race` is clean.

## Status

Build + vet clean; full `go test ./...` green; `-race` clean on the core/sequencer/factory tests.
The unit tests exercise the mechanism (normal milestone attachment now propagates its baseline as a
floor) but NOT the far-behind forward-sync runaway itself — `TestMemDAGLaggingNodeRecursion` attaches a
far-ahead non-branch tip (unknown baseline, depth-cap bounded), not a committed branch whose baseline
propagates down.

Testnet run 1 (hloc0-acc, commit `bdeadc98`): runaway flood fixed (att bounded), but the override
variant pinned forward sync with `conflicting branch endorsement` — fixed by the floor model (`e8b2ed64`).
**Needs a re-run on a far-behind access node to confirm the pin clears.**
