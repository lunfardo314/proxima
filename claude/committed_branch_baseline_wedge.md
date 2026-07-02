# Wedge: committed-but-not-Good branch can't serve as a baseline (N/A-baseline)

Findings note. A sequencer node (hloc0) that had committed the canonical branch at
slot 32512 then **stalled there and could not attach any milestone from 32512 to
the live tip** — every one failed with `conflicting branch endorsement s32512-0`.
DAG-verified as **NOT a fork, NOT load, NOT the fork-detection changes**. It is the
known, code-documented "N/A baseline" phenomenon (attacher.go:124), reached here
via a committed post-snapshot branch that is not `Good` in the memDAG.

Status: **diagnosis only.** Candidate fix identified; needs a `sync_diag`-traced
repro to confirm the exact reason `s32512-0` is not `Good`.

## Incident (hloc0, 2026-07-02, during a fleet spam)

hloc0-seq restarted on the fork-detection binary. Startup was correct: forward-sync
caught it up to 32503, the gate released, the sequencer started. ~5 min later it
committed up to **slot 32512** and then completely stalled: LRB frozen at 32512,
slipping to 77 slots behind the live tip, `936` `conflicting branch endorsement`
warnings against `s32512-0`. The user stopped it.

## DAG evidence (authoritative — not inferred from logs)

Canonical branch chain from a healthy node (loc0-seq LRB), first 2 bytes = slot:

| slot | canonical branch | hloc0 |
|------|------------------|-------|
| 32511 (`0x7eff`) | `00007eff0101 80747dd2…` | = hloc0's committed 32511 |
| 32512 (`0x7f00`) | `00007f000101 707f6e13…` | = the branch it rejected `s32512-0` |

- **Single branch at slot 32512** (`s32512-0-01707f6e`, 937 mentions, no competitor) → not a fork.
- `s32512-0` is **committed** on hloc0 (`BRANCH COMMIT … 'oseq1' coverage 98.89%, tx: 7 seq + 0 non-seq`) and is its LRB (current − "behind" = 32512).
- **Light branch, 0 non-seq** → not a solidification-under-load problem.
- Highest committed slot = 32512; **every milestone 32512→32590 fails**, `933 baseline: N/A` (only the 3 earliest, before `s32512-0` committed, showed `baseline: s32511-0`).
- **Zero** pull / solidification-failure / "not solid" / "not available" lines for the stuck milestones — they are rejected immediately on the baseline, not after failing to fetch anything.

## Mechanism (code-traced)

1. **Baseline resolution** — `solidifyBaselineUnwrapped` (attacher.go:72-128): a
   milestone's baseline is resolved by attaching its `BaselineDirection()` tx and
   reading its status. `Good` → baseline set; `Undefined` → baseline stays **unset
   (N/A)**, attacher only pulls/waits. The code comment at attacher.go:123-124 names
   this exactly: *"Repeated lines here … mean the baseline cannot be resolved (the
   N/A baselines behind the flood)."*
2. **Committed ≠ Good in memDAG** — `AttachTxID` (attach.go:97-115) auto-marks a
   branch `Good` from state **only if it is in the SNAPSHOT state**. A *post-snapshot*
   committed branch (`s32512-0`, slot ≫ snapshot slot) referenced by a successor is
   returned as a **virtual, not-`Good`** vertex (attach.go:98-100) unless it is still
   `Good` in the memDAG. `AttachTxID` does not re-run the milestone attacher, so it
   does not re-derive `Good` from the committed state.
3. **The misnamed error** — a milestone endorsing branch B requires
   `B == a.pastCone.GetBaseline()` (attacher.go:518, `conflicting branch endorsement`).
   With the baseline resolved to N/A (because `s32512-0` isn't `Good`), the endorsement
   of the (correct, canonical) `s32512-0` trips this check. It is **not** a lineage
   conflict — it is an unresolved baseline surfacing under a misleading name.

So: hloc0's LRB is the committed `s32512-0`, but `s32512-0` is not `Good` in its
memDAG, so no successor milestone can adopt it as a baseline → the node cannot
advance a single slot, and the symptom is a `conflicting branch endorsement` storm.

## What it is NOT

- **Not a fork** — single canonical branch at 32512; the monitor correctly stayed
  silent (no fork). The `OnCanonicalLineage` / §2a machinery is not implicated.
- **Not load / not slow commit** — light branch, committed fine; no fetch failures.
- **Not the single source** per se — this is baseline resolution, not fetching.
- **Not the fork-detection changes** — the monitor was dormant at runtime (no target
  added); this is the pre-existing baseline-resolution / N/A-baseline path.

## Candidate fix

Make a **post-snapshot committed branch resolvable as `Good` directly from the
committed state** in `AttachTxID`, the way a snapshot-state branch already is
(attach.go:104-106). Rationale: a branch that is committed (its root record + state
exist) is by definition valid and final within the retention horizon; a successor
should be able to adopt it as a baseline without a live re-attach. Today only the
snapshot-state case is handled; the post-snapshot committed case falls through to
"virtual, not-`Good`" and depends on the branch still being `Good` in the memDAG,
which is exactly what fails after the vertex is pruned / not re-derived. If correct,
this closes the class: a committed branch always serves as a baseline.

Caveats to check before implementing:
- Confirm the branch's root record / state is sufficient to mark it `Good` without
  re-validating (it should be — it was validated before commit).
- Ensure this does not mask a genuinely divergent/forked committed branch (it must
  key on "committed in *this node's* state" = the same lineage, which is sound: a
  fork would be a *different* branch txid, not this one).
- Interaction with the branch/txid retention horizon (claude/txid_ttl_tiered.md):
  only branches within the retained window are eligible.

## Open question (needs a traced repro)

The current log lacks `sync_diag` tracing, so it cannot show **why** `s32512-0` is
not `Good` when its successors reference it — GC'd/pruned from the memDAG, or never
re-derived from state, or a large-delta resolution failure. Repro with
`logger.topics.sync_diag` (or the trace tag) enabled to capture the
`baseline of <ms>: dir s32512-0 UNDEFINED -> pull/wait` lines and whether the pull
ever makes `s32512-0` `Good`. That distinguishes:
- **(A) pruned committed branch not re-derivable as `Good` from state** → the
  candidate fix above closes it; vs
- **(B) delta-too-large / genuinely unsolidifiable past cone** → the separate open
  "bound a pathologically-large delta" work.

Connects to the prior `sync_diag` / large-delta investigation (the "N/A baselines
behind the flood" comment and the open metric-to-bound-the-delta question).
