# Forward-sync lineage non-stitch — handoff (2026-06-20)

> **CONTINUATION (2026-06-21 EOD).** The forward-sync redesign below was implemented and shipped
> (`9772cdff` lineage-exact + `to_branch` API; `460239a0` count-when-baseline-unsolidified;
> `cd84728c` target = branch hit at cap; `c3f6680d` cap-only-on-branches + set-based targets +
> handoff log). **hloc0 (seq node) now SYNCS.** **hloc0-acc (access node) does NOT** — and it is
> NOT a recursion bug. See the new section "Committed-fork diagnosis + next plan" at the bottom of
> this file. Nothing for the new plan is implemented yet. Original 2026-06-20 notes below are kept
> for history (the bug they describe is fixed).

Status: **(original 2026-06-20)** OPEN, diagnosed-not-proven. Resume by
proving the lineage mismatch (step "Next" below), then deciding the fix.

## One-line problem

`loc0-acc` (access node, no sequencer, on `loc0`, service
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

---

# Committed-fork diagnosis + next plan (2026-06-21 EOD)

## What is fixed and what is not

Shipped this session (develop): `9772cdff` (lineage-exact forward sync; `to_branch` API mode;
LRB removed from sync), `460239a0` (count at-cap attachers even when baseline unsolidified),
`cd84728c` (forward-sync target = the branch hit at the cap, not the unsolidified baseline),
`c3f6680d` (cap only on BRANCH deps; set-based target registry: `AddSyncTarget`/`RemoveSyncTarget`/
`SyncTargetsPending`/`LowestSyncTarget`; single reach-target handoff log).

**hloc0 seq node: SYNCED & running.** **hloc0-acc access node: still stuck.**

## hloc0-acc symptom (commit c3f6680d)

Frozen at slot 57620, ~1665 behind (network 59285), DB exists, snapshot_restore disabled.
Forward sync correctly: `target added s59222` → `adopting target … 1602 branches to commit` →
then loops `branch s57621-0 not yet ready, stopping batch` forever. Attacher pool floods:
att 612 → 10,526, goroutines → 21,191, memory > 1 GB. Committed coverage never changes.
Shutdown BAD dump: `s57621-0 … Undefined past cone: s57620-14, s57620-27`; thousands of BAD
attachers spread over slots ~57582–57595 (and the gap). `s57620-14/27` are never pulled/received.

## Why this is a committed FORK, not a recursion regression (code-traced)

The backward walk *does* go through branch baselines and a committed baseline terminates it:
- `childAttachmentDepth` (attach.go:118) increments depth only on branches.
- `AttachTxID` (attach.go:62–72): when `multistate.FetchBranchData` finds a branch **committed in
  the DB**, it wraps it as a **Good** virtual branch immediately (asserts `== vertex.Good`). So a
  committed baseline branch is recognized without re-walking, and `solidifyBaselineUnwrapped`
  (attacher.go, `case vertex.Good`) terminates.

Therefore a milestone's walk stops the instant it crosses a baseline branch that is committed in
THIS node's DB. The walk cascading ~38 slots down means the baseline branches it crosses are **not
committed here** → the lineage forward sync is committing (the source's, from target s59222) does
**not share hloc0-acc's committed `s57620`**. The node committed a *different* (forked) `s57620`.
Likely cause: an earlier pre-lineage-exact forward sync (≤`9772cdff`, with the non-stitch / LRB-anchor
/ zero-target bugs) force-committed wrong-lineage branches up to 57620. Those commits are in the DB;
syncing cannot un-commit them. The flood is the doomed attempt to bridge the canonical lineage down
to wherever it rejoins this node's committed state (~57582 = the fork point).

## NEXT — the agreed plan (user, 2026-06-21; NOTHING IMPLEMENTED)

1. **Verify the fork on the real DAG** (do NOT infer from logs). Compare hloc0-acc's committed branch
   at slot 57620 against the canonical 57620 on `s57621-0-0192db7919ff`'s lineage (from a synced peer:
   hboot-acc, or boot). Different txid ⇒ fork confirmed. Need the hloc0-acc box IP/host
   (Hetzner net; config only exposes the peer hboot-acc).

2. **Client-side fork detection + re-anchor (FINAL design — supersedes the server-rooted-point idea).**
   The API stays simple: `get_branch_list?to_branch=<target>&from_slot=<S>` — server walks back from
   `to_branch`, returns branches with slot `> from_slot`, oldest-first. **Delete the `max` client
   param** (keep an internal server-side safety ceiling for DoS; signal if truncated). The server does
   NOT check anything about the requestor — fork detection is entirely client-side.

   Protocol in forward sync:
   - **First call:** `to_branch = target`, `from_slot = LRB_slot − batch`. (Below LRB by a batch so the
     returned list overlaps the requestor's own committed branches and a shallow fork is caught in one
     round-trip; for the no-fork case the result is immediately usable.)
   - **Find the latest common branch** = the **highest-slot branch in the returned list that has a local
     root record** (committed locally). Forward sync starts committing from the next branch up. (This is
     essentially the existing "filter already-committed" step — the only real change is lowering
     `from_slot` so the window reaches into committed/common territory instead of only the post-fork tail.)
   - **If no branch in the window is committed locally** (entire window is post-fork): walk older — next
     call `to_branch = oldest-received-branch`, window of **2 batches** (`from_slot = oldest_slot − 2*batch`;
     2 batches so the fork point can't hide in a boundary gap). Repeat until a common branch appears.
   - **Re-org is automatic, no special logic.** Committing the canonical lineage from the common branch
     up adds branches at slots the DB already has on the lost fork; the **biggest-coverage rule** then
     makes the heavier (canonical) lineage win — locally a re-org, globally the lost fork is just
     orphaned. We "just sync the history"; valid branches, so no coverageDelta/monotonicity special-casing.
   - **Refuse conditions** (threshold = a **static constant in the forward_sync module**, not tied to the
     ledger TTL constant): (a) trivial precheck — if `latest_committed_slot` is older than `now − threshold`,
     **refuse immediately**, don't probe; (b) if `latest_common_slot` is older than `now − threshold`,
     refuse after probing. Rationale: below the TxID-state-TTL the trie has pruned txids, so a state that
     old can't be safely built forward from. "Refuse" = clear operator message now; the snapshot fallback
     is item 4. (hloc0-acc at 1665 behind likely hits the trivial precheck → refuse → it becomes the
     item-4 test, while items 2–3 get exercised on a node behind but WITHIN the threshold.)
   - Replaces the current blind `fromSlot = healthySlot` bound, which wrongly assumes the node's frontier
     slot is on the source's lineage (false on a fork).

3. **Implement #2 and test.** Re-anchor/re-org path: a node behind but within the threshold, forked.
   Refuse path: a node beyond the threshold (possibly hloc0-acc as-is). Testnet left as-is.

4. **Fix the startup bug: node does not force-start from a younger snapshot.** Too-old detection /
   snapshot-restore did not trigger here (`snapshot_restore.enable=false`; `CheckAndRestoreOnStartup`
   skipped because the DB exists). Per `sync_semantics.md` §5 a node too far behind with a younger
   snapshot available should restore. Make the startup decision do that (scenario 6). This is the
   "refuse → find younger state" fallback the refuse path in item 2 defers to.

5. **After the sync work is done: docs cleanup pass.** Revisit ALL the sync specs/handoff docs, cut
   verbosity, and distill the essential, settled points (including the client-side fork-detection /
   re-anchor protocol above and the cap-only-on-branches + set-based-target model) into
   `sync_semantics.md`. The scattered handoff/incident docs (`project_sync_*`, this file) are working
   notes; `sync_semantics.md` is the durable, terse spec — fold the conclusions in and prune the rest.

---

# hloc0-acc flood — root cause (DAG-verified) + open bounding question (2026-06-21 EOD #2)

## Status of the plan
- Item 2 (client-side fork detection + re-anchor + refuse) SHIPPED `42b44de4`. It works:
  on hloc0-acc the probe correctly found `common start slot 57620` (NO branch-level fork).
- Diagnostic tracing added under trace tag `sync_diag`: `f96b4fdb` (attacher spawn + baseline
  solidification outcome) and `321d31af` (per-dependency in-state result). Enable via node config
  `trace_tags: [sync_diag]`. These revealed the real cause below.

## The real cause — NOT a fork, NOT a check bug, NOT the baseline cascade

hloc0-acc is stuck at slot 57620, floods to ~10k attachers, never commits. The `sync_diag` traces
showed it is `solidifyPastCone` not terminating (610k `inState=false` vs ~2k `inState=true`; the
false deps are milestones just below the frontier). DAG-verified via `proxi db txstore dag_explorer`
(host 65.21.170.230:8001) — the txids:

- Committed `s57620-0-010f5ade` = seq **85c3e543 (hloc0)**, **0 endorsements**, chain-pred input
  `s57619-27-00becb69`. So the node's committed branch covers ONLY hloc0's own chain.
- Forward-sync target bottom `s57621-0-0192db79` = seq **9d2c6fed (the bootstrap sequencer)**; its
  inputs are bootstrap's chain-pred `s57620-27` + hloc0's stem `s57620-0#1`. So bootstrap's branch
  builds on hloc0's stem but runs its OWN milestone chain.
- The flooding attacher `s57620-14-00299d07` = seq **9d2c6fed (bootstrap)**, and it **endorses**
  `s57619-27-001f1692` (bootstrap's slot-57619 milestone) — a DIFFERENT tx from hloc0's
  `s57619-27-00becb69`.

So the bootstrap sequencer's milestone chain (`s57620-27 → endorses s57619-27-001f1692 → endorses
s57618-27 → …`) is genuinely NOT in hloc0's committed state, because hloc0's branches have **0
endorsements** — they never merged bootstrap's coverage. Validating the canonical (bootstrap)
branches therefore requires solidifying bootstrap's ENTIRE milestone history (~1100+ slots), via the
endorsement chain, none of it in-state → unbounded flood.

`inState=false` is CORRECT; recursing to solidify those delta txs is CORRECT.

## Critical correction (user, do not forget)

**Timestamp is NOT DAG order — only consistent with it.** A tx with timestamp *earlier* than the
baseline can still be in the **delta above** the baseline (above in consumption/DAG order); its
ancestors are rooted in the baseline but the tx itself is in the delta. So a dependency below the
baseline's timestamp that is `inState=false` is normal and valid — NOT a hard stop. (Two wrong fix
ideas to discard: "mark Bad if dep slot <= baseline slot" — wrong, rejects valid delta; "slots
behind" cap — wrong, slots may be empty.)

## What this means

1. **hloc0-acc needs a snapshot restore (item 4).** Its committed state is hloc0's narrow,
   0-endorsement single-sequencer chain; it cannot reconstruct the bootstrap sequencer's history that
   the canonical lineage references. Sync cannot bridge this. (Note: this also raises a question about
   WHY hloc0's committed branches have 0 endorsements / why two sequencers ran parallel chains without
   merging — possibly a network/sequencer dynamics issue worth a separate look.)

2. **OPEN: how to bound a pathologically-large delta (robustness; user is thinking on the metric).**
   The flood is the doomed solidification of a huge delta. `dag_semantics` says a past cone hits the
   baseline in a BOUNDED number of steps (the attachment budget) — but that holds PER attacher, and
   the delta here is solidified by a CASCADE of sub-attachers (each endorsed milestone is pulled and
   gets its own milestoneAttacher), so no single budget bounds the aggregate. The depth cap that
   should bound the cascade is DEAD because depth is unreliable: a vid's `attachmentDepth` is set when
   it is FIRST created, and solicited/gossiped txs are created by the input queue at **depth 0**, and
   `AttachTxID` returns the existing vid without updating depth (attach.go:36-39). So the whole
   cascade is `depth=0` and never caps.

   Candidate metric (NOT confirmed — user to decide): cap the **depth of the non-in-state recursion**
   = the longest chain of UN-ROOTED dependencies before hitting the baseline state. A wide/broad past
   cone is shallow in this metric (hits in-state fast → no false-cap, avoids the reverted 2026-06-18
   per-vertex breadth leak); a pathological delta (incompatible state) is deep (1100 hops) → cap →
   refuse → snapshot. Explicitly NOT slots-behind, NOT branch-count, NOT per-vertex breadth — it's
   path length through non-rooted deps. User wants to think before locking the metric.

## Other facts confirmed this session (for tomorrow)
- Traversal ORDER is correct: `solidifyBaseline` runs to Good before `solidifyPastCone`
  (attacher_milestone.go:135 then :143).
- `solidifyBaselineUnwrapped` requires the baseline-DIRECTION fully `Good` (not just its baseline) —
  a separate over-strictness, not the main cause here.
- `defineInTheStateStatus` / `BranchKnowsTransaction` / `KnowsCommittedTransaction` are correct.
- dag_explorer endpoints: `/api/v1/dag_explorer/{find_tx?q=,tx_detail?txid=,past_cone?txid=&depth=,slot?slot=}`.

---

# ROOT CAUSE — "Good required where baseline-available/rooted should suffice" (2026-06-21 EOD #3)

This supersedes the coverage/lineage framing above (that framing is noise — the problem is purely
DAG solidification against the imposed constraints).

## The bug

Solidification waits for a dependency **vertex to be `Good`** (fully validated) when it only needs the
dependency's **baseline**, which is known/available *before* the vertex is Good. **Waiting for Good is
too strong a constraint.** A tx that is **rooted** (present in the baseline/committed state) is valid
and its baseline is known, yet it is not `Good`; the code does not treat rooted as terminal, so it
re-solidifies it — dragging in its whole past cone.

`solidifyBaselineUnwrapped` resolves a tx's baseline by following the baseline DIRECTION and requires
that direction's `GetTxStatus() == Good` (else it pulls/waits). The committed→`Good` shortcut in
`AttachTxID` exists **only for branches** (`FetchBranchData`). A **rooted non-branch** tx gets no
shortcut → stays `Undefined` → its consumer waits for it to become Good.

## The flood mechanism (factual, traced path)

forward sync solidifies `s57621-0` → chain-pred `s57620-27` → chain-pred `s57620-14` (baseline
`s57620-0`, a committed branch = Good). `s57620-14` **consumes** `s57619-27-001f1692` (a delta tx,
`inState=false`) → recurse. `s57619-27-001f1692`'s baseline DIRECTION is `s57619-14-00f3fb128473` — a
**rooted milestone** (in `s57620-0`'s state) but **non-branch**, so no committed→Good shortcut → stays
`Undefined`. `s57619-27-001f1692` hangs in baseline solidification waiting for it.

Meanwhile every milestone whose baseline *is* resolvable (its baseline branch is committed/Good)
passes baseline solidification and proceeds to re-solidify its past cone, spawning attachers for its
non-branch deps — **not depth-capped** (depth is set on first vid creation; solicited txs are depth 0).
Because **all branch baselines are committed/Good**, baseline solidification never hits an obstacle, so
the wave flows back through the rooted milestones without stopping → unbounded → OOM.

## Factual evidence (from the `sync_diag` log, 10s run)
- 610,594 `refreshDep … inState=false` vs 1,942 `inState=true`; **all 742 distinct `inState=true` txs
  are branches** (`s<slot>-0`), spanning slots 56879..57620 → the branch chain is shared/rooted; the
  milestones are not.
- `s57619-14-00f3fb128473` (the rooted baseline-direction) has **no trace at all** — pulled as a
  baseline direction but never processed/recognized as rooted; the consumer hangs on it.

## Lesson / architecture note (user)
**Availability of the correct baseline in a vertex is DIFFERENT from the vertex being `Good`.** Waiting
for Good is too big a constraint. This is fundamental — needs an architecture rethink so that
"baseline available / rooted" is terminal for solidification, without requiring the dependency vertex
to be fully `Good`. (User is thinking on the valid architecture; nothing to implement yet.)

This also confirms the earlier point: a node SHOULD be able to forward-sync from any state — with this
constraint fixed, the rooted branch chain bounds the wave and a snapshot is only an optimization, not a
requirement.
