# forward_sync — what to read, and what to watch out for

This file deliberately does **not** describe how forward sync works. Two places
already do, and a third copy would only drift:

* [`claude/sync_semantics.md`](../../../claude/sync_semantics.md) — the
  authoritative model of how a node catches up. A **hard constraint**: code here
  must be consistent with it, and it is evolved only with explicit user
  approval.
* The package comment at the top of `sync.go` — the mechanism as built: what
  triggers forward sync, why it has no reach cap of its own, how it hands off to
  recursive sync, and why pull parallelism is not a reach limit.

What follows is the operational picture and the known soft spots.

## Configuration

| Key | Meaning |
|-----|---------|
| `sources` | Trusted node API endpoints. **Shared with snapshot restore**, and forward sync's on/off switch: it is active exactly when this list is non-empty after self-filtering. There is no separate enable flag. |
| `sync.pull_ahead` | How many branches to pull in parallel, ascending by slot, so their attachers overlap network round-trips. Bounds concurrency, not reach. |
| `sync.commit_batch` | How many branches to commit per tick. Bounds per-tick throughput, not reach. |
| `sync.max_slots_behind` | Refuse to forward-sync at all if the latest committed or latest common branch is further behind than this. A node-local policy, not a ledger constant. |

`max_slots_behind` is the one worth understanding: it is the point at which
**restoring a fresh snapshot beats building forward**. Its default matches half
the branch txID retention — the depth to which branch baselines stay resolvable.
Past that, the branches you would need are no longer identifiable, so building
forward is not merely slow but unsound.

## Known soft spots

Two issues are real, latent, and not fixed. They surfaced during a live
stop/restart exercise and are recorded in
[`claude/archive/incidents/stress_sequencer_shutdown.md`](../../../claude/archive/incidents/stress_sequencer_shutdown.md).
Both were re-verified against `develop` on 2026-08-24.

**`vid.baselineBranchID` conflates two things** — the baseline *floor* and the
*resolved* baseline. Commit `d73b4142` removed the bootstrap path that produced a
bad floor; it did not remove the overloading. Forward sync pins older baselines
the same way, so the same failure shape can recur here.

**`SetBaseline` and `GetBaseline` disagree about which field wins.**
`PastCone.SetBaseline` writes `delta.baselineBranchID` while a delta is open, but
`GetBaseline` returns the outer field whenever it is non-nil, and `baselineKnowsTx`
reads the outer field directly. A baseline swapped by `MergePastCone` inside an
incremental attacher's delta is therefore invisible until `CommitDelta`.

One more, in the attacher rather than here: the known-baseline floor that stops a
far-behind node re-solidifying the whole cone
([`claude/archive/incidents/known_baseline_attacher.md`](../../../claude/archive/incidents/known_baseline_attacher.md))
is race-clean and works, but **no test reproduces the runaway it prevents** — the
unit tests exercise the mechanism, not the far-behind scenario.

## Not an open issue

`enforceSeqCoverageDelta` in `core/attacher/wrapup.go` skips its cross-check when
the attach baseline is at or past the milestone's own slot. That is deliberate,
not a hole: during snapshot restore and forward sync a milestone is re-attached
against a foreign baseline, so the recomputed delta is meaningless, and rejecting
it would wedge the sync path permanently by cascading BAD to everything pulled
behind it. The strict-increase invariant is still enforced on-chain by
`_enforceCoverageAdvance`, which is baseline-agnostic. An earlier incident note
recorded this fix as "shipped but uncommitted"; it is committed.
