# Fork detection & recovery on startup

Design spec for making a node whose committed state has diverged from the
network's canonical lineage **recover deterministically or refuse cleanly**,
instead of silently wedging. Companion to `sync_semantics.md` (this refines §2.1
"divergent lineage" and §5 "startup DB-state decision").

Status: **SPEC — not yet implemented.** Evolve `sync_semantics.md` only with
explicit user approval; this file is the working design.

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

### 3. Enforce synced-on-canonical before the sequencer starts

`ensureSyncedIfNecessary` must block until the node is genuinely synced **on the
canonical lineage**, so a frozen/forked/boundary state is not treated as synced
and the sequencer cannot manufacture a one-sided branch. Concretely:

- "Synced" must incorporate the §2 lineage result — a fork or a frozen LRB is NOT
  synced (today `IsSynced()` = "local healthy branch at slot ≥ now−1", which a
  fresh forked branch satisfies).
- Remove the flip-flop: return the wait result directly instead of re-sampling
  `IsSynced()` after the wait loop (the re-sample is what produced the 1 ms
  synced⇄not-synced disagreement). This is correct **only once** "synced" is
  fork-aware — otherwise returning the observed-synced value would start the
  sequencer on a boundary fork.

## What exists vs. what is new

| Piece | Status |
|-------|--------|
| `checkStateTooOldDownload` scenario 6 (download+replace) / 6b (refuse) | exists — `snapshot_restore/too_old_recovery.go` |
| `tryDownloadRemoteSnapshot`, `querySourcesForRecovery` | exists |
| `findCommonStartSlot` / `refuseSync` (canonical-chain walk during catch-up) | exists — `forward_sync/sync.go`; adapt to local-walk + run at startup |
| `GetBranchChainTo` source lineage query | exists — `api/client/client.go` |
| Startup **lineage** check (walk local → canonical, find common ancestor) | **new** — add to the §5 decision |
| Route fork (unreachable) into scenario 6/6b | **new** wiring (reuses existing) |
| Fork-aware `IsSynced` / sequencer gate + flip-flop fix | **new** — `core/workflow/access.go`, `sequencer/sequencer.go` |
| `sync.disable` loud startup warning | **new** — small |

## Validation

Cannot be validated on loc0 (preserved, off-limits) or safely on the live
testnet. Needs a **fork-reproduction harness**: on the laptop 3-node net, drive a
node onto a one-sided fork (e.g. partition its sequencer briefly under load, or
inject a divergent committed branch), restart it with sync enabled, and assert:
re-anchor recovers a reachable fork; scenario-6/6b fires for an unreachable one;
the sequencer never starts on a fork. Add unit coverage for the local-walk
common-ancestor search against a mocked source lineage.

## Open questions

- Exact "on canonical lineage" definition for `IsSynced`: reuse the startup
  common-ancestor result and re-validate periodically, or a lighter continuous
  check?
- Cost of per-branch source queries for a deep fork — batch the canonical window
  in one `GetBranchChainTo` and compare locally (equivalent, fewer round-trips).
