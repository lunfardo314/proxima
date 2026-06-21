# Forward-sync lineage non-stitch — handoff (2026-06-20)

Status: **OPEN, diagnosed-not-proven.** Stopping point for the day. Resume by
proving the lineage mismatch (step "Next" below), then deciding the fix.

## One-line problem

`loc0-acc` (access node, no sequencer, `63.250.56.190`, service
`proxima-loc0-acc.service`) **cannot sync**. Forward sync commits a burst of
branches on each (re)start, then **pins forever on a single boundary branch**,
looping `[forward_sync] branch sNNNNN ... not yet ready, stopping batch`. The
committed frontier (LRB) freezes; "slots behind" climbs without bound.

`loc0-seq` (the sequencer node on the same box) is **fine / fully synced** — it
drives its own commits via its sequencer. The problem is specific to a *behind
access node* relying on forward sync.

## What was SHIPPED today (pushed to develop) — unrelated to the open bug

- `3091d340` forward-sync: uncapped, hands off to recursive sync. **Doc + dead-code
  only.** Removed the now-dead depth-cap-exemption machinery (`latestTargetTicks`,
  `LatestForwardSyncedTimestamp`) that the attacher no longer reads after the
  decoupling; rewrote `sync_semantics.md` §3 + the module doc to the
  uncapped-handoff model. **Did NOT change the runtime `pull_ahead`/`commit_batch`/
  `break`-on-not-ready loop.** So enabling forward sync still hits the same stall.
- `87e62c07` txlogger: resilient DB lifecycle. Fixed an unrelated startup crash
  (`initTxLogger` FATAL `while opening fid: 1 err: Create a new file` — orphaned
  unflushed 128MB memtable). Two fixes: (1) `node/db.go` folds the txlog DB close
  into the coordinated `dbClosedWG`+`workProcessesStopStepChan` shutdown (it was a
  fire-and-forget `ctx.Done()` goroutine that lost the race to process exit, so the
  memtable was never flushed); (2) `txlogger` self-heals — `store.Open` no longer
  panics, and `TxLogEnable` wipes+recreates the disposable DB on open failure.
  Deployed on loc0, recovered.

Both are sound and deployed. Neither touches the lineage-stitch logic.

## The open bug — diagnosis

### Behaviour (observed, evidence-based)

- Each restart: forward sync commits MANY branches in a burst (e.g. coverage
  `1_872_280_303_094_933` → `1_943_641_948_811_659`, near tip ~97%), then pins on
  ONE branch and loops `not yet ready` indefinitely (saw 117 such lines for s52027
  in one window).
- The pin slot **moves every restart**: 51905 → 51972 → 52027. It is NOT one
  defective branch; it is wherever the burst happens to stop.
- While pinned, LRB coverage is **frozen at a constant** while `current_slot`
  advances → slots-behind climbs (saw 50→103). Not converging.
- **No determinism / coverage-delta / monotonicity / BAD-from-cross-check errors.**
  When the attacher pool is clear (just after restart) these exact branches commit
  cleanly. So it is NOT a cross-check rejection, NOT divergent-lineage-of-the-DB,
  NOT the coverageDelta wedge.

### The smoking gun (from the pre-restart BAD lines)

```
ATTACH s52027-0-01e68df22030 ... BAD(... Undefined past cone: s52026-14-0053d0dd.., s52026-27-00bb3a38..)
```

The stuck branch **s52027** needs two **non-branch milestones in slot 52026**
(`s52026-14`, `s52026-27`). The committed frontier (LRB) is **also at slot 52026**.
If those milestones were in the 52026 branch the node committed, s52027 would
stitch instantly. They stay "undefined" → s52027's same-slot dependencies are on a
**different 52026 branch / lineage** than the one the node committed.

### Hypothesis (user's, fits all evidence) — "it literally does not stitch"

Forward sync is following a **different lineage at the seam slot** than the
recursive-pull / committed frontier. The seam is one slot wide and never closes:
forward sync pushes lineage-A's s52027 onto a frontier the node committed on
lineage B; the lineage-A milestones at the seam slot never become rooted on
lineage B, so the next branch never solidifies. Recursive sync (gossiped tips
backward) and forward sync (source branch-list forward) are **two uncoordinated
commit mechanisms** with **nothing forcing them onto the same branch at the seam**.

Contributing code detail: `forward_sync/sync.go requestBranchList` anchors
`forkSafetyDepth` (10) back from the node's own LRB and force-commits whatever
chain the source returns (`GetBranchListAfter(anchor, 100)`). If the node already
committed a *different* branch at the seam slot than the source's chain assumes,
the returned chain does not extend the committed tip, and the next branch's
same-slot milestones are never on the committed lineage.

## NEXT (resume here) — prove it with ONE clean DAG read

Per CLAUDE.md (never infer DAG topology from logs), compare:
1. the branch ID **loc0-acc committed at the seam slot** (its LRB branch), vs
2. the branch at that slot that the **stuck forward-sync branch extends** (its stem
   predecessor) — and where the "undefined" milestones (`-14`, `-27`) live.

If (1) ≠ (2) → non-stitch proven outright.

Blocker hit at stop time: the running node holds the `proximadb.txstore` badger
lock, so `proxi db txstore get` can't open it concurrently; and the node API
returned 404 for `/api/v1/` root and `/txapi/v1/`. **First task: find the node-API
endpoint that fetches a branch/tx by ID** (read `api/server` route registration),
or query a synced peer (loc0-seq / boot) for the canonical seam-slot branch and
compare against loc0-acc's LRB branch. Catch the live pin-slot fresh from
`not yet ready` logs (it moves each restart).

## Fix directions (after proof — design-level, get user go-ahead; flagged "do not hack")

- Make forward sync commit a **contiguous single lineage** anchored on the node's
  ACTUAL committed LRB branch: request the successor of *that exact branch*; if a
  returned branch's baseline ≠ the current committed tip, **re-anchor and
  re-request**, never force-commit a non-contiguous branch.
- Or make meet-in-the-middle **lineage-aware**: detect that the canonical (heaviest)
  lineage diverged from what was committed and follow/adopt the canonical one
  (re-org the last few slots) instead of spinning.
- Tie-in to `sync_semantics.md` §3 "Meeting in the middle" — the doc claims they
  meet "by frontier coverage, not branch matching," which is exactly the assumption
  that breaks here. The doc may need correcting once the fix is settled.

## Live state at stop (for orientation; will have drifted)

- loc0-acc: behind, pinning; pid churned through 790271 / 790580 / 790748 /
  790889 / 790970 (user restarting repeatedly). `sync.disable` was flipped to
  `false` (forward sync ON) on loc0-acc during diagnosis; sources list populated.
- loc0-seq: synced, sequencing, forward sync OFF (correct there).
- Network current slot ~52089; loc0-acc LRB ~52026.

## Related open notes (same wedge family)

`project_sync_restart_catchup.md`, `project_sync_reattach_wedge.md`,
`project_coverage_delta_sync_wedge.md`, `sync_semantics.md` §2 (intro list of
not-yet-implemented parts), §3, §4.
