# Fork detection & recovery on startup

Design spec for making a node whose committed state has diverged from the
network's canonical lineage **recover deterministically or refuse cleanly**,
instead of silently wedging. Companion to `sync_semantics.md` (this refines §2.1
"divergent lineage" and §5 "startup DB-state decision").

Status: **PARTIALLY IMPLEMENTED.** The sequencer start gate (§3) and the sync
module's `OnCanonicalLineage()` fork detector are implemented (see below); the §2
recovery routing (startup re-anchor / snapshot download / refuse) is not yet.
Evolve `sync_semantics.md` only with explicit user approval; this file is the
working design.

**Implemented (this pass):**
- `forward_sync`: `OnCanonicalLineage()` + a dedicated `canonicalMonitorLoop`
  (`refreshCanonicalLineage`) that probes sources for whether the committed LRB is
  on the canonical lineage; fail-open, off the catch-up loop. `Store(true)` init.
- `core/workflow/access.go`: `OnCanonicalLineage()` delegates to the sync module
  (nil → true when sync disabled).
- `sequencer`: start gate is `OnCanonicalLineage() && (IsSynced() || mustBootstrap)`
  with a confirmation window (`syncedConfirmations`; 1 when `mustBootstrap`, since
  its signal is stable), no post-wait re-sample. `mustBootstrap` = the existing
  `DoNotWaitForSyncAtStart` (which the node already sets for `BootstrapFromOldState`)
  `|| ForceActivity || Standalone`.

**Not yet implemented:** §2 startup lineage decision routing (reachable fork →
re-anchor; unreachable → scenario-6 download / 6b refuse), §1 `sync.disable` loud
warning. The gate above already prevents a forked/unsynced node from *manufacturing*
a fork; the §2 work is what *recovers* an already-forked node.

## The incident that motivated it (loc0, 2026-07-01)

loc0 (a sequencer node) was restarted repeatedly under heavy spam load. During
one restart its sequencer resumed while the node was not truly synced and
committed a **one-sided branch at slot 27202** that diverged from the network's
canonical `s27202-0`. From that instant:

- Its LRB **froze at 27202** and slipped monotonically to ~190 slots behind while
  the network advanced normally (healthy at ~27730).
- Every gossiped network milestone failed to attach with **`conflicting branch
  endorsement`** (3,611 times): the milestone endorses the canonical branch, but
  loc0's baseline is its own forked branch at the same slot (`branchesCompatible`
  = same-slot-different-branch ⇒ incompatible; the endorsement check requires
  endorsed == baseline).
- `IsSynced()` flapped true→false (the frozen fork sits right at the health
  boundary), so `ensureSyncedIfNecessary` logged "synced" then "not synced" within
  1 ms and the sequencer refused to start on restart.
- loc0's config had **`sync.disable: true`** — forward sync (which owns the
  fork-detection + `refuseSync` machinery) never ran. `checkStateTooOldDownload`
  didn't help either: it triggers on *slot distance* (loc0 was only ~527 behind,
  under the ~8740 tolerance), and has no notion of *wrong lineage*.

Net: a known-unrecoverable-by-sync state (`sync_semantics.md §2.1`) was **masked
as an endless conflicting-endorsement retry loop** instead of being surfaced.

## Design principles (from the network's security model)

- **Biggest ledger coverage is the consensus rule.** A fork the network abandoned
  has less coverage than canonical *in the live network's view*. A frozen fork
  cannot overtake canonical as the network keeps building — a fork overtaking
  canonical would be a consensus break, not a sync bug.
- **Therefore a node must never advance a fork.** The sequencer must not build
  milestones unless the node is synced **on the canonical lineage**. This is the
  load-bearing prevention: it is exactly the gate loc0 slipped through.
- **A node that finds itself on a fork must re-root or refuse — never mask.** The
  condition is surfaced to the operator (or auto-recovered), never retried
  forever.

## Behavior

### 1. `sync.disable: true` → run as-is (abnormal config)

No fork detection. Disabling sync is not normal and should be avoided. Emit **one
prominent startup warning**: sync disabled ⇒ no fork detection and no catch-up;
the state may be silently too old or on an abandoned fork. Otherwise unchanged.

### 2. Sync enabled (default) → startup fork detection via local-chain walk-back

Fold a lineage check into the §5 startup DB-state decision
(`checkStateTooOldDownload`), alongside the existing slot-distance check. With
`sources` configured:

- Walk the node's **own committed branch chain backward** from its LRB. For each
  local branch, ask a source whether it is on the canonical lineage
  (`GetBranchChainTo` from the source's LRB down over the window; compare txids
  slot-by-slot). Walking the *local* chain and querying remotes per branch is
  preferred over walking the canonical chain — the node always fully knows its own
  chain, so it needs the remote only to answer membership. The first local branch
  found on canonical is the **common ancestor**.

Outcomes:

- **common ancestor == LRB** → on canonical, just behind (or current) → keep the
  DB, normal sync.
- **common ancestor < LRB** (fork, reachable — above the snapshot floor) →
  **re-anchor** forward sync to commit the canonical lineage forward from the
  common ancestor. Because the sequencer is gated off (§3) the fork is frozen, and
  by the coverage rule canonical overtakes it; the LRB re-roots and the forked
  branches are pruned as orphans. (Forward sync's existing `findCommonStartSlot`
  already computes this common start during depth-cap catch-up; the new part is
  running the detection proactively at startup so recovery does not depend on an
  attacher happening to stall at the cap.)
- **common ancestor unreachable** (the fork point is below the snapshot floor — the
  snapshot blinds the walk-back) **or no reachable source** → the DB is on an
  incompatible fork that cannot be re-rooted in place. Route into the **existing
  scenario-6** path: download a younger snapshot from `sources` and replace the DB
  (`tryDownloadRemoteSnapshot`). If no young-enough snapshot is available → **refuse
  (scenario 6b)**: shut down with a clear operator message. **Never** fall back to
  "run as access node without a sequencer" — that serves no purpose. Sources must
  be configured.

### 3. Sequencer start gate: on-canonical (hard) + synced-OR-bootstrap

The gate has TWO parts with different roles:

```
mustBootstrap = ForceActivity || Standalone || DoNotWaitForSyncAtStart || BootstrapFromOldState
start when:   OnCanonicalLineage() && ( IsSynced() || mustBootstrap )
```

**`OnCanonicalLineage()` — the hard fork guard (always required).** The §2
common-ancestor walk returns `common ancestor == LRB`, i.e. the LRB is on the
canonical lineage. This is the right invariant (not the weaker "the start UTXO O
sits in some canonical ancestor"): the resumed sequencer builds a **new milestone
whose baseline is the current LRB** (`solidifyBaseline` picks the heaviest branch
in the past cone), so if the LRB were a fork the new milestone would extend the
fork even if O predated it. Since O is loaded from the LRB, LRB-on-canonical
implies O-on-canonical. **It is fail-open** (see §2 / the sync module):
indeterminate cases — no committed reliable branch (genesis/empty network), no
source ahead of us (we are at the tip), or no reachable source — read `true`, so
the guard blocks ONLY on a *positively detected* fork. This is what lets genesis /
standalone / bootstrap start while still forbidding building on a known fork.

**`IsSynced() || mustBootstrap` — caught-up OR must-bootstrap.** `IsSynced()` is
the unchanged health primitive (recent healthy committed branch = caught up to a
*live* network). It is relaxed by `mustBootstrap`, which is the set of "be active
regardless of sync" conditions:
- the config flags for genesis/dev (`ForceActivity`, `Standalone`,
  `DoNotWaitForSyncAtStart`), and
- **`BootstrapFromOldState`** — the §5 scenario-7 startup detection ("the network's
  committed state is far behind real time" = the network is stalled), set
  independently on *every* node that observes the stall.

**Why the relaxation is mandatory (decentralized bootstrap).** Restarting a
stalled network is not a single "designated bootstrapper": one sequencer does not
have enough coverage for a healthy branch alone. **Many** sequencers must start in
the same bootstrap slot and combine their milestones (via endorsements) until the
consolidated coverage crosses the healthy threshold and the network takes off.
`BootstrapFromOldState` firing on every stalled node lets them all start; requiring
`IsSynced()` there would deadlock the restart (nobody is synced because nobody is
producing). The coverage combining itself is existing proposer/endorsement
behavior — the gate only has to let them start.

**Why keeping the fork guard hard (even for bootstrap) is safe.** Today the
force-start flags *fully* bypass the gate; under this design they relax only
`IsSynced()`, not `OnCanonicalLineage()`. A genuine stall has no fork, so on-canonical
is `true` (or fail-open true) and every bootstrapper starts as before — but a node
whose LRB is a *detected fork* will not bootstrap the fork forward. Strictly safer.

**Reuse the syncing effort — no source queries from the sequencer.** The sync
module (§2) owns the on-canonical determination (`OnCanonicalLineage()`), refreshed
by its own loop; the sequencer reads it. When sync is disabled (§1) there is no
determination → `OnCanonicalLineage()` reads `true` (nil sync module), so the gate
reduces to `IsSynced() || mustBootstrap` (accepting the "mess" disabling implies).

**Flip-flop fix.** `ensureSyncedIfNecessary` must not re-sample after the wait loop
— return the wait result directly (the re-sample logged "synced" then "not synced"
~1 ms apart). Wait until the combined gate holds for a short **confirmation
window** (a few consecutive polls); the counter resets on any false, so a boundary
flicker cannot satisfy it.

## What exists vs. what is new

| Piece | Status |
|-------|--------|
| `checkStateTooOldDownload` scenario 6 (download+replace) / 6b (refuse) | exists — `snapshot_restore/too_old_recovery.go` |
| `tryDownloadRemoteSnapshot`, `querySourcesForRecovery` | exists |
| `findCommonStartSlot` / `refuseSync` (canonical-chain walk during catch-up) | exists — `forward_sync/sync.go`; adapt to local-walk + run at startup |
| `GetBranchChainTo` source lineage query | exists — `api/client/client.go` |
| Startup **lineage** check (walk local → canonical, find common ancestor) | **new** — add to the §5 decision |
| Route fork (unreachable) into scenario 6/6b | **new** wiring (reuses existing) |
| Sync module exposes `OnCanonicalLineage()` (LRB == common ancestor), refreshed by its loop | **new** — `forward_sync/sync.go` |
| Sequencer gate = `OnCanonicalLineage() && (IsSynced() \|\| mustBootstrap)`, confirmation window, no re-sample | **new** — `sequencer/sequencer.go`; `mustBootstrap` reuses existing `ForceActivity`/`Standalone`/`DoNotWaitForSyncAtStart`/`BootstrapFromOldState`; force-start now relaxes only `IsSynced`, not the fork guard |
| `sync.disable` loud startup warning + gate falls back to plain `IsSynced()` | **new** — small |

## Validation

Cannot be validated on loc0 (preserved, off-limits) or safely on the live
testnet. Needs a **fork-reproduction harness**: on the laptop 3-node net, drive a
node onto a one-sided fork (e.g. partition its sequencer briefly under load, or
inject a divergent committed branch), restart it with sync enabled, and assert:
re-anchor recovers a reachable fork; scenario-6/6b fires for an unreachable one;
the sequencer never starts on a fork. Add unit coverage for the local-walk
common-ancestor search against a mocked source lineage.

## Resolved decisions

- **"On canonical lineage" is owned by the sync module**, not by `IsSynced`. The
  sync loop runs the local-chain walk-back and publishes `OnCanonicalLineage()`
  (`LRB == common ancestor`). The sequencer gate reads it; `IsSynced()` stays the
  caught-up/health primitive. No source queries from the sequencer.
- **Sequencer start gate = `OnCanonicalLineage() && (IsSynced() || mustBootstrap)`**,
  held over a confirmation window, no post-wait re-sample. On-canonical is the hard
  fork guard (fail-open for genesis/tip/no-source); `IsSynced()` is relaxed by
  `mustBootstrap` (`ForceActivity`/`Standalone`/`DoNotWaitForSyncAtStart`/
  `BootstrapFromOldState`) so many sequencers can restart a stalled network and
  combine coverage. Force-start now relaxes only `IsSynced`, not the fork guard.

## Open questions

- Cost of per-branch source queries for a deep fork — batch the canonical window
  in one `GetBranchChainTo` and compare locally (equivalent, fewer round-trips).
- Refresh cadence of `OnCanonicalLineage()` in the sync loop (every tick vs. every
  N slots) and how it composes with the existing depth-cap-triggered catch-up.
