# Sync semantics — how a node catches up with the network

> **HARD CONSTRAINT** — Authoritative model of how a node catches up with the network.
> **Binds:** `core/core_modules/forward_sync`, `core/attacher`, `core/workflow`, `sequencer`, `node`. Evolved only with explicit user approval.

Status: living document. First draft 2026-06-17.

## What this document is

This is the **general semantic model of syncing** in Proxima: how a node that is
behind the network's advancing consensus state catches up with it. It is a
companion to [`dag_semantics.md`](dag_semantics.md), at the same altitude —
general and implementation-independent, describing responsibilities and intended
behaviour, not data structures or code paths. `dag_semantics.md` describes the
DAG and the memDAG; this document describes the process by which the memDAG and
the persisted state are *brought up to date*.

Recursive sync, forward sync, and snapshot restart all exist in the code, but
this document describes the **intended** semantics after a re-architecture, and
several load-bearing parts are **not yet implemented** — they describe target
behaviour, not current code. As of this draft the following are intended/TODO:

- **Attacher agnosticism (the central principle).** The attacher must know
  *nothing* about forward sync, the network frontier, or any other
  synchronisation concept. It does exactly one thing: recurse the past cone via
  pulls, bounded by a **depth cap that is a pure constant given the
  configuration**. Today it is coupled — `isDepthCapped()` consults
  `LatestForwardSyncedTimestamp()` (it "knows" forward sync) — and that coupling
  is the 2026-06-20 freeze (§2). The fix removes the frontier term entirely;
  the only base case is "is this dependency already in committed state?" (§2).
- **Per-branch recursion depth** (§2.1). Today the attacher counts a per-*vertex*
  attachment depth; the intended metric counts *branches* (lineage distance). This
  makes "at the cap" mean "genuinely far behind" and eliminates the
  false-cap-at-the-tip that caused the 2026-06-18 leak.
- **Snapshot-first for far-behind; forward sync off by default.** The primary
  recovery for a node that is far behind is to **cold-start from a closer baseline
  — a newer snapshot** (§5), not to grind a long forward-sync. Forward sync brought
  more complexity and failure modes than it solved (the dead-zone, the
  deferred-commit freeze, the attacher coupling); it is demoted to an **optional,
  off-by-default fallback**, enabled by the operator only when no suitable snapshot
  is available. Activation is governed solely by whether the top-level `sources`
  list is configured — there is no separate on/off flag: with at least one source
  forward sync runs, with none it is off (recursion + snapshot only) — see §3.
- **Forward sync is uncapped and hands off to recursive sync** (§3). When enabled,
  forward sync commits forward until it reaches the frontier where recursive sync
  stopped, hands the sync process over to recursion, and stops — no fixed
  forward-sync window, so no gap can open between the two frontiers (the 2026-06-20
  restore dead zone). Not yet implemented (today forward sync has a fixed window).
- **Refuse, don't wait, beyond the cap.** A node whose recursion reaches the depth
  cap is too far behind for recursive sync alone. *Implemented:* when forward sync is
  off (no `sources`), reaching the cap triggers a **graceful shutdown** with an
  operator-facing reason (restore a newer snapshot, or configure `sources`), rather
  than poll-waiting for a forward sync that will never run. When forward sync is on,
  the capped branch is instead registered as its target and the attacher waits for
  it to be committed (§2.1, §4). The attacher's cap handler reads only the boolean
  "is forward sync enabled"; the target-servicing orchestration stays in the sync layer.
- **General prune of stalled/orphaned attachers** (§2.1, §4) — wall-clock orphan
  detection that overrides attacher pins, regardless of attacher origin. Not yet
  present; its absence is exactly the 2026-06-18 leak.
- **Startup DB-state decision & snapshots** (§5) — the exhaustive startup-scenario
  table (start-from-DB vs replace-from-snapshot vs refuse), the DB-direct "too old"
  detection, the TTL-bounded tolerance ST, the "don't rush to delete" rule, and the
  periodic state-cleanup recovery. Implemented: too-old recovery via
  `CheckAndRestoreOnStartup` + `snapshot_restore.max_state_age_slots` (ST, on by
  default = `TxIDStateTTLSlots`/2), including scenario-6b refuse-and-shut-down.

Both the document and implementation will evolve incrementally: where document and
code disagree the intended semantics lead, but established correct behaviour
corrects the document rather than the other way round (same rule as
`dag_semantics.md`). Missing or to-be-changed parts are called out as such and
implemented later.

Rules for this document:

- It is **general and largely implementation-independent**. Constants and config knobs
  are named for orientation, but exact values, file paths, and algorithms live in
  code and in the focused `claude/*.md` incident/handoff docs, which link here.
- It is kept **reasonably short**. Detail belongs in the linked working docs, not
  here.
- It **evolves only with user approval**, like `dag_semantics.md`.

---

## 1. What syncing is

- The network's **consensus state advances continuously**: sequencers keep
  issuing milestones and branches, slot after slot, so the committed ledger state
  the network converges on is always moving forward.
- **Syncing is catching up with that advancing state.** A node is *synced* when
  its own state tracks the network's consensus state within a small, steady-state
  lag (a slot or a few); it is *behind* otherwise.
- Whenever a node **starts**, or **disconnects** and reconnects, its state is
  usually behind the consensus state — by anything **from one or a few slots to
  arbitrarily many (unbounded)**, depending on how long it was absent.
- Closing that gap is **normal node operation**, not an exceptional mode. The
  machinery below runs continuously; "being in sync" is just the case where the
  gap it is closing happens to be small.

Mechanisms that close the gap, in order of primacy:

1. **Recursive sync** (§2) — always-on, demand-driven, walks the past *backward*
   from received tips until it meets state the node already has. This is the
   fundamental mechanism and handles the steady state and small-to-moderate gaps.
2. **Snapshot restart** (§5) — for a node that is **far behind**, the right
   recovery is to adopt a **closer baseline**: cold-start a new DB from a newer
   snapshot near the present, so the remaining gap is small enough for recursive
   sync. This — not a long grind — is the primary far-behind path.
3. **Forward sync** (§3) — an **optional, off-by-default** accelerator that builds
   state *forward* by requesting branches in order from sync sources. It is a
   fallback the operator enables only when no suitable snapshot is available;
   experience showed it brings more complexity and failure modes than it solves
   (the dead-zone, the deferred-commit freeze, the attacher coupling), so it is no
   longer on by default.

Recursive sync and (when enabled) forward sync work toward each other and **meet
in the middle** (§3–§4) — but, crucially, **recursive sync is complete on its own**
for any gap within the depth cap; forward sync only *accelerates* it, and the
attacher is wholly unaware of whether forward sync is running (§2).

Note that it is not guaranteed that a node can sync with the network always: it can
happen that a node's state (or its snapshot) is not in the lineage the network is
currently evolving on. In that case the process must **refuse and surface to the
operator** (§2.1), not stall silently — recovery is a newer snapshot on the live
lineage.

---

## 2. Recursive sync (always on)

Recursive sync is the **most common** and the **fundamental** form of catching
up. It is **always ON** — an intrinsic part of how the node attaches
transactions, not a separate subsystem that gets switched on when "behind".

How it works:

- On start (and continuously thereafter) the node **receives transactions via
  gossip** from its peers.
- When the node receives a **branch** (or any sequencer transaction), it **spawns
  an attacher** for it. To solidify that transaction the attacher needs its past
  cone, so it **pulls the missing dependencies** — inputs, endorsements, and the
  baseline branch — **from the network** or **from the txstore**.
- Those pulled dependencies are themselves transactions, including **other
  branches**, each of which spawns its own attachment and pulls *its* missing
  dependencies. The process **recurses backward** through the tangle.
- The recursion **stops where it hits state the node already has** — transactions
  already in the memDAG / txstore, or outputs already rooted in a committed branch
  state. At that point the past cone is complete and attachment can finish.

Key properties:

- It is **demand-driven**: the node only pulls what some tip it received actually
  needs. There is no global "fetch everything" step.
- It is **the node continuously trying to reach the current consensus state** by
  pulling whatever is missing and spawning attachers for it. This is the natural
  baseline behavior; everything else in this document is a mitigation or an
  accelerator of it.
- The depth of the backward walk equals **how far behind the node is**. If the
  node was down a long time, the recursion can reach **very far back** — in
  principle unbounded.
- The backward recursion is **unbounded by nature**. E.g. a node down for a week must
walk back a week of branches. When node starts attaching a tip of the tangle, the recursion wave back may take time, unbounded in general. The process
recursing back may even stop, temporary (due to communication disruptions) or permanently (when requested transaction is not available from 
the available nodes). During that time, nodes keeps receiving transaction from live network that builds the DAG even further. 
This makes the distance (in slots and in DAG depth) between back-recursion frontier and latest tips unbounded and unknown in advance. 
- same time, the attachment of the sequencer transactions is a process bounded by the attachment budget. See [dag_semantics.md](dag_semantics.md). 
It means every past cone will hit the baseline state in bounded number of steps. What makes syncing unbounded is **unbounded distance
from the sequencer transaction to the state known by the node**.

**No attachment timeout — but pulls do have a deadline.** Two different timeouts
must not be conflated:

- The **attachment process has no timeout.** A sequencer attachment is bounded by
  the attachment budget, so a single past cone always reaches its baseline in a
  bounded number of steps; what is unbounded is only the *distance* from the
  received tip back to state the node already has. An attacher legitimately
  waiting for that distance to be closed must not be aborted just because it has
  waited a while.
- An **individual pull of a specific transaction does have a deadline.** When the
  attacher pulls a missing dependency and it does not arrive after repeated
  attempts, that pull fails and the attachment goes BAD. This is what bounds a
  *solidification attack* (a validly-signed tip whose dependency simply does not
  exist): the attack is **not** stopped by the budget cap — such a tip's past
  cone is tiny — but by (a) the upstream signature + per-holder rate limits that
  bound how many such tips can be injected, and (b) the pull deadline that reaps
  each dangling dependency.

The depth cap (§2.1) sits between these two. A dependency **within** the cap **is**
pulled (or taken from the local txstore/cache) and is subject to the pull deadline.
A dependency **beyond** the cap is **not pulled at all** — the attacher has reached
its bound and stops descending.

**The attacher is agnostic — the cap is a pure constant.** This is the load-bearing
principle. The cap is `depth > maxAttachmentDepth`, where `maxAttachmentDepth` is a
**constant fixed by configuration**. The attacher consults *nothing else*: not the
forward-sync frontier, not whether forward sync is even running. Its
only base case for terminating the recursion is the one in §2 above — **the
dependency is already in committed (rooted) state.** Any coupling of the cap to a
synchronisation concept (today: `depTs.After(LatestForwardSyncedTimestamp())`) is a
bug — it is exactly the 2026-06-20 freeze (§2), where a node with the branches in
its own txstore refused to use them because a *disabled* forward sync's frontier
never advanced.

Why this is sufficient, with or without forward sync:

- **The recursion terminates via the "in committed state" check, not via the cap.**
  As committed state advances — by recursive sync itself committing branches it
  reached, or by forward sync committing them independently — deeper dependencies
  become rooted, and the attacher's next pass terminates there. The attacher never
  needs to *know* what advanced the state; it just re-checks "is this rooted yet?"
  So a small cap is fine when forward sync is on (it advances the state), and a
  large cap lets recursion bridge the whole gap from the local txstore when forward
  sync is off. The cap size is the *only* thing that differs, and it is set by
  config, not decided by the attacher.
- **Beyond the cap, the attacher does not wait for any specific helper.** It polls
  (re-checking the base case) and emits a single **neutral** signal — "I am blocked
  at the depth cap" (the global at-cap counter, §4). It does not name forward sync.
  A separate orchestration layer (§2.1, §4) — which *does* know about forward sync
  and snapshots — decides what to do about a node stuck at the cap: enable/await
  forward sync if configured, otherwise **refuse and ask the operator for a newer
  snapshot**. The attacher itself never gives up and never assumes a rescuer.

### 2.1 The unbounded sync depth problem and its mitigations

Letting the backward recursion run unbounded is impractical: it can
stall the node, flood it with pulls, and be abused.

Unbounded recursive sync is **mitigated** by a depth cap plus a snapshot-restart policy.

**Recursion depth** is the number of **branches** between a received branch `B`
and the earliest ancestor branch currently being attached on its behalf. The
counter increments only when the backward walk crosses into an **earlier
branch**; it stays the same across the non-branch sequencer transactions within a
slot. So depth measures *lineage distance* — roughly "how many slots behind" —
not past-cone breadth. It is relative to a fixed `B`: later transactions arriving
over gossip (the future cone of `B`) do not change it.

**Recursion depth cap.** The backward recursion **stops at a maximum depth**, a
**constant fixed by configuration** (tentatively **50 branches** when forward sync
is enabled; **large**, e.g. **1000**, when it is disabled — set at startup, *read*
by the attacher as an opaque number; the attacher does not derive it or know why
it has that value — §4). At the cap the attacher **stops descending and does not
pull the next branch back**; it **polls** (re-checking only its base case, "is
this dependency rooted yet?") and emits the **neutral** at-cap signal. This is
still the normal attachment process — there is no attachment timeout — only held
at the bound. The count of poll-only-at-cap attachers is the node's sole "am I
behind?" signal; the sync-orchestration layer (§4) consumes it to decide between
*await forward sync* (if enabled) and *refuse → newer snapshot* (otherwise). The
attacher neither makes that decision nor names forward sync.

**Orphan attachers are cancelled by the pruner, not by an attachment timeout.** A
poll-only attacher whose tip never gets incorporated into the canonical state
must eventually be abandoned — but *when* cannot be decided from slot position
alone: a transaction whose baseline is at slot `s` may still be legitimately
included in a branch beyond `s`, so "the committed frontier passed slot `s`"
decides nothing. The decision is therefore left to the pruner, which — once the
node is **following the network** — detects a vertex as an **orphan by wall
clock** (ledger time has advanced well past it and it is not in the canonical
state) and **cancels its attacher (BAD)**. So some attachers do end up timed out,
but by *wall-clock orphan detection in the pruner* — a synced-node operation
(§4) — never by the attachment-duration timeout we reject above. This applies to
**any** stalled/orphaned attacher regardless of how it was started — forward
sync, recursive pull, or unsolicited gossip alike. No provenance distinction is
needed: one general "prune stalled/orphaned attachers" mechanism covers them all.

**Far-behind → restart from a younger snapshot.** If the node's **latest
committed slot is far behind** the network — beyond the tolerance **ST**, which is
bounded by the TXID TTL (§5.1) — the node **tries to find a younger snapshot in
the network and start a new DB from it** (§5), instead of trying to recurse across
an enormous gap. This replaces a hopeless deep recursion with a clean cold start
much closer to the present.

**Too old, no snapshot available → refuse to sync.** If the node's state is older
than ST behind a live network and **no suitable younger snapshot** is available on
the network, the node **refuses to sync and shuts down with a clear message**
rather than churning indefinitely (scenario 6b, §5.2). This is a deliberate
give-up: it surfaces the situation to the operator instead of pretending to make
progress. (The distinct case where the *whole network* is stalled at an old state —
the node is not behind a live network — is scenario 7: keep running and bootstrap.)

**State on a divergent lineage → won't heal; restart or refuse.** Sync can reach
the network's canonical lineage only if the node's committed state is *on that
lineage* (or behind it on the same lineage). If the node's state sits on a
**different lineage** than the one the network is evolving — a fork the network
abandoned — neither recursive nor forward sync can bridge it: forward sync builds
forward from the node's tip, but the canonical lineage does not extend from that
tip. The node will not heal by syncing, no matter how long it waits. The only
recoveries are the same two as above — restart from a younger snapshot on the
network's lineage, or refuse — and the condition must be surfaced to the operator
rather than masked as endless catch-up.

These mitigations turn an unbounded process into a bounded one with explicit,
operator-visible failure modes.

---

## 3. Forward sync (optional, off by default)

Forward sync **helps** recursive sync. It is a separate process, **off by default**
and enabled by the operator only as a fallback (below). Activation is governed solely
by whether the top-level `sources` list is configured: with at least one source it
runs; with none it is off. There is no separate on/off flag — configuring `sources`
*is* the opt-in. (This replaces the older `sync.disable` flag.)

**Why it is off by default.** Forward sync was originally on by default as a
catch-up accelerator, but it accreted more complexity and failure modes than it
removed: the gossip-shed dead-zone, the deferred-commit freeze (received branches
never finalized without it), and — worst — the attacher coupling that made
recursive sync *depend* on forward sync's frontier and froze nodes when it was off
(§2). The lesson: **catch-up should not hinge on a second, stateful, directional
subsystem.** Recursive sync (§2) plus a snapshot for a closer baseline (§5) is the
complete and simpler model; forward sync is kept only for the case where no
suitable snapshot is available.

How it works (when enabled):

- Forward sync **builds state starting from the node's current state and moves
  forward**, the opposite direction to recursive sync.
- It **requests branches in batches from sync sources** (a configured set of
  peers/endpoints) — slot by slot, in order, ahead of the node's committed tip —
  and **commits** them (via `ForceCommitBranch`), advancing the node's committed
  state toward the present. It follows the lineage of the specific branch a stuck
  attacher needs (§4), asking sources to walk back from *that branch itself*.
  Because it *commits* (not merely delivers) each branch, those branches become
  rooted — which is precisely what lets the agnostic attacher (§2) terminate its
  recursion on them, **without the attacher knowing forward sync produced them.**

When to enable it:

- Only when a node is **far behind and no suitable newer snapshot is available** to
  adopt a closer baseline (§5). In that situation the operator turns forward sync on
  to grind the gap forward. In the normal case — snapshot available, or gap within
  the recursion depth cap — it stays off and recursive sync handles everything.

**Triggering (no hysteresis).** Forward sync runs **iff at least one attacher is
poll-only at the max depth** — i.e. iff the sync-mode counter (§4) is non-zero.
There is no "slots behind" threshold and no up/down hysteresis. This is safe
because per-branch depth makes the at-cap count a *monotone, draining* quantity:
forward sync commits branches forward, so each waiting attacher's distance to
known state only shrinks, and freshly-gossiped tips sit at the network frontier
(shallow depth), not in the backlog — a monotone quantity crossing a single
threshold cannot flap. The only re-entry is a genuine new fall-behind, which is
exactly when forward sync *should* restart. In normal (synced) operation nothing
polls at the cap, so forward sync is simply off.

**Forward sync is *uncapped*; it hands off to recursive sync.** This is the
load-bearing rule. Recursion is *capped*: it covers the most recent `cap` branches,
`[tip − cap … tip]`; its deepest point is the **recursion frontier**. Forward sync
has **no cap of its own** — it commits branches forward, in order, from the committed
state until **it reaches the frontier where recursive sync stopped (or happens to
be), hands the sync process over to recursive sync, and stops.** It thus covers the
band `[committed … recursion-frontier]`, recursion covers `[recursion-frontier …
tip]`, and because forward sync's stopping point *is* the recursion frontier — not a
fixed window — **no gap can open between the two**. (A fixed forward-sync window was
the 2026-06-20 dead zone: with `minSnapshotAgeSlots (64) > the forward-sync cap (50)`
a freshly-restored node had a band recursion could not reach and forward sync stopped
short of, so the two frontiers never stitched. Uncapping forward sync removes it; with
forward sync off, the larger no-forward-sync cap covers the band directly — as it did
for loc0-seq.)

**Meeting in the middle (same lineage).** Forward sync drives the committed frontier
up the lineage of the **specific branch the waiting attacher needs** — its baseline,
which the attacher publishes (§4) — asking sources to walk back from that branch
itself. The two directions therefore meet **on one lineage by construction**: forward
sync commits exactly the branches below the needed branch, so as the frontier reaches
it the attacher's baseline becomes rooted and its recursion resumes. (Driving toward
an independently chosen lineage lets the two waves advance on divergent lineages and
pass each other — the 2026-06-20 non-stitch.) A tip on a genuinely abandoned lineage
is on no source's chain, so forward sync cannot fetch it and it is eventually reaped
as an orphan by the pruner (§2.1, §4).

---

## 4. Implementation notes

**Needed-branches registry (the signal that also directs).** Maintain a single global
registry of the branches that **poll-only-at-cap** attachers are waiting for: one entry
per such attacher, valued by the branch its recursion is blocked on — its **baseline**,
on the gossiped tip's own lineage. An attacher registers its entry when it goes
poll-only at the cap and removes it when it leaves that state (its baseline becomes
rooted as committed state advances, or it is cancelled). The attacher still references
*nothing* about forward sync — it publishes a fact about its own past cone. The registry
serves two consumers in the **sync-orchestration layer**: its **size** is the node's
sole "am I behind?" signal, and its **lowest-slot entry** is the branch forward sync
drives toward (§3). With it, the layer decides:

- **forward sync enabled** → run forward sync exactly while the registry is non-empty
  (no "slots behind" computation, no hysteresis — §3);
- **forward sync disabled** → a sustained non-empty registry means the node is past
  the recursion cap with no accelerator → **refuse and surface to the operator**
  (restore a newer snapshot, §5, or enable forward sync). Never poll forever.

**Max depth (a config constant the attacher reads opaquely).** Counted in branches
(§2.1). Tentatively **50** when forward sync is enabled; **large**, e.g. **1000**,
when disabled, since recursion is then the only forward mechanism and must reach
further before the orchestration layer gives up. The size is chosen at startup
from config; the attacher reads it as an opaque number and does not know which case
it is in.

**Pruning is a synced-node operation.** Garbage collection / pruning of the
memDAG has no meaning while the node is behind: the past cones held by waiting
attachers are *needed*, not garbage, and must not be pruned. Therefore:

- **While syncing** (counter non-zero): pruning is suspended; the memDAG grows.
  That growth is bounded by catch-up completing. If catch-up *cannot* complete —
  an attacher polls indefinitely because its dependency is genuinely unavailable
  — that is a **sync failure**, surfaced loudly to the operator; the node must not
  pretend to operate normally.
- **While following the network** (caught up): pruning runs and uses the **wall
  clock** to detect orphans. Crucially, wall-clock orphan detection **overrides
  attacher pins**: a stale vertex is cancelled (its attacher set BAD) and pruned
  *regardless of* the attacher still referencing it. This applies to **any**
  stalled/orphaned attacher, regardless of how it was started — forward sync,
  recursive pull, or unsolicited gossip alike. There is no need to distinguish
  attacher provenance: a single general "prune stalled/orphaned attachers"
  mechanism covers all of them.

The deadlock this breaks — and the illegal state to prevent — is the overlap of
the two: *following the network while still holding sync-era stragglers the
pruner refuses to touch*. That was exactly the 2026-06-18 leak: *attacher pins
vertex → vertex not prunable → never detected as orphan → attacher polls forever*,
with the committed frontier thousands of slots past attachers that were never
reaped.

**Overlap is harmless.** Because attachment is idempotent, recursive sync and
forward sync acting on the same transactions do no harm; a small overlap buffer
may even help efficiency. In normal operation they do not overlap, because
nothing polls at the cap.

**Inbound filters during sync.** The attach gate is sync-mode-aware:

- **Always:** solicited (pulled) transactions attach unconditionally — they are
  exactly what some attacher or forward sync asked for.
- **Outside sync mode** (counter == 0): the ordinary rules apply — sequencer txs
  attach subject to the attacher-cap rate control; non-sequencer txs attach only
  if they are tag-along to the local sequencer (its mempool). No "too far ahead"
  shed: far-ahead branches **must** be allowed to attach so their past-cone
  recursion reaches the depth cap and flips the counter — that is the only way the
  node ever enters sync mode. (An earlier far-ahead gossip shed, set below the cap,
  defeated this and was removed.)
- **During sync mode** (counter > 0): attach **only branches** (plus solicited).
  Non-sequencer txs and non-branch sequencer milestones are dropped — they feed
  the sequencer backlog / tippool, which are not needed for catch-up; they remain
  in the txstore and are pulled on demand if a branch's past cone requires them.
  Branches keep attaching because they anchor lineage and advance committed state.

This assumes the local sequencer is not active during sync; an active sequencer's
mempool would need a dedicated path. The node's OWN milestones always attach
(tagged as sequencer-sourced, treated as solicited).

---

## 5. Startup: the DB-state decision and snapshots

At startup, before the node opens its state DB for normal operation, it makes ONE
decision: **start from the existing DB, or replace it from a snapshot.** The
decision is made by inspecting the DB **directly**: read whether it is corrupted,
read its **latest committed slot**, query trusted `sources` for the network's
current slot and newest snapshot, then decide.

This "too old?" check is **distinct from `IsSynced()`**. `IsSynced()` also reads
the DB faithfully, but it answers a *different* question — "is there a recent
healthy branch?" — not "is the committed state too old relative to a fresher
network/snapshot?". A node can be entirely valid yet far behind; detecting that
requires the latest-committed-slot comparison done here.

**The cardinal rule: do not rush to delete the DB.** The DB is deleted (and the
node restored from a snapshot) in **exactly two** cases — corrupted, or
too-old-with-a-young-snapshot. A valid DB lets the node start, with **one
exception**: a DB that is too old (beyond ST) behind a **live** network with no
snapshot to adopt — there the node refuses and shuts down (scenario 6b), but it
still does *not* delete the DB (there is nothing to replace it with). "Refuse to
start" thus applies to a corrupted DB with no snapshot, and to scenario 6b.

### 5.1 "Too old" is relative — and bounded by the TXID TTL

A node is "too old" only relative to a **younger state that actually exists** —
either a younger snapshot, or a live network ahead of it. If the whole network is
(re)starting from an old state — genesis, or everyone coming up on an old DB —
there is no younger state anywhere, so **no node is "too old": they start from the
old state and the sequencer issues the bootstrap transactions** that move the
network forward (scenario 7). "Too old" relative to a snapshot is meaningful only
when a peer offers a materially fresher state; "too old" relative to a live network
is meaningful whenever the node is behind it by more than the tolerance ST.

**The tolerance ST is bounded by the TXID TTL — a correctness bound, not just an
efficiency one.** ST is the maximum the committed state may lag the network's
committed state before the node must replace it from a snapshot (or refuse). It is
**ON by default**, defaulting to **half the TXID TTL** (`TxIDStateTTLSlots`, §
`dag_semantics.md` 2.4) and always kept **strictly below the TTL**. The TTL bound
is load-bearing: once the committed state is older than the TTL, the attacher
**blesses pre-TTL dependencies as in-state without proof** — a relaxation that is
safe only for the single snapshot-boundary slot (`dag_semantics.md` §2.4–§2.5),
not across a multi-slot sync gap, where it would silently accept un-provable
ancestry. So the state must be replaced *well before* it ages past the TTL, and ST
defaults to half for margin. (Recursion reach — the depth cap, §2.1 — is a
*separate, smaller* preference threshold: beyond it a snapshot is the more
efficient tool, but only beyond ST is replacement or refusal *mandatory*.) The
snapshot adopted must itself be **young enough** (within ST of the tip) so the
post-restore remainder is recursively bridgeable.

### 5.2 The startup scenarios (exhaustive)

| # | DB state | Younger snapshot available? | Action                                                                                                                     |
|---|----------|------------------------------|----------------------------------------------------------------------------------------------------------------------------|
| 1 | **missing** (fresh node / genesis) | yes | restore from it (incl. `genesis.snapshot` for a brand-new net)                                                             |
| 2 | **missing** | no | **refuse to start** (no state at all)                                                                                      |
| 3 | **corrupted** / restore-interrupted | yes | delete DB, restore from a suitable snapshot (download or local)                                                            |
| 4 | **corrupted** | no | **refuse to start** (cannot run on a corrupt DB)                                                                           |
| 5 | **valid, recent** (within ST of the network's committed state) | n/a | start from the DB; recursive (and, if enabled, forward) sync bridge the small gap                                          |
| 6 | **valid, too old** (behind the network's committed state by > ST) | **yes**, young enough (newer than DB, within ST of the tip) | delete DB, restore from it; recursion bridges the remainder                                                                |
| 6b | **valid, too old** (behind a **live** network by > ST) | **no** young-enough newer snapshot | **REFUSE to sync and shut down** with a clear message. Do NOT delete — there is nothing to replace it with; the node simply will not start on a state it cannot safely sync from. |
| 7 | **valid, at the network's committed level**, but the network's committed state is itself > ST behind real time (whole network stalled at an old state) | **no** fresher snapshot anywhere | **start from the existing DB** and **force-start the sequencer** (if configured) even though it is not synced. Do NOT delete. |

Scenario 6 is the far-behind / abandoned-lineage recovery (the loc0-seq case):
the node's sync cannot heal an abandoned lineage (§2.1), so the only fix is
adopting a fresher snapshot. Scenario 6b is the same magnitude with **no fresher
state to adopt**: the node is genuinely behind a live network and beyond ST, so
neither sync (the TTL bound, §5.1) nor a snapshot can save it — it refuses rather
than churn forever. Scenario 7 is the **whole-network-from-old-state / bootstrap**
case — distinguished from 6b by the node being *at the network's committed level*
(not behind it) while the whole network is stalled, so the node must run and help
the network advance instead of shutting down.

**Scenario 7 must force the sequencer to start.** With no fresher state to sync to,
the node will *never* become synced — so the sequencer's default wait-for-sync (§
sequencer) would block forever, and the network could never advance. Therefore a
node that determines it is in scenario 7 (too old, sources reachable, *no* younger
snapshot anywhere) sets a runtime **bootstrap-from-old-state** signal that
force-starts its sequencer regardless of `IsSynced()`. This is an automatic
counterpart to the explicit `do_not_wait_for_sync_at_start` config the genesis
bootstrap node sets. **Safety**: it fires only when sources are *reachable and
confirm* no younger snapshot exists (the network really is at the old state) — never
on a node that is merely behind a live network, and never when sources are
unreachable (then it relies on the explicit config and otherwise waits).

**Snapshot selection** (when one is needed): the in-progress-cleanup snapshot from
the state file, else the newest **old-enough** snapshot **downloaded** from
`sources` (the shared trusted endpoint list; preferring one ≥ `minSnapshotAgeSlots`
old so the sequencer does not wait out its start guard), else the newest **local**
snapshot in the snapshot directory. "Suitable" for a too-old replacement also means
*newer than the current DB*.

**Mechanism**: implemented as a branch of `CheckAndRestoreOnStartup` — open DB →
(corrupted? / read latest committed slot) → query sources → if replacing: close the
DB, delete its files, restore from the chosen snapshot, continue startup with the
fresh state. The too-old case is **on by default**: the tolerance ST is
`snapshot_restore.max_state_age_slots`, defaulting to half the TXID TTL and clamped
strictly below it (§5.1). After any restore the node is within recursion
reach, so the sequencer's wait-for-sync (§ sequencer, default) is satisfied quickly
and it starts on the live lineage — not the abandoned one.

### 5.3 Periodic state-cleanup recovery (forced, maintenance — not catch-up)

Separately from the above, a **synced** node may periodically (every
`snapshot_restore.period_slots`, ~24 h, jittered by `window_slots`) **force** a
restart-and-restore from a local snapshot to **compact** the multistate DB — the
`snapshot_restore.enable` mechanism. This is housekeeping, not catch-up: it is
gated on the node being *synced* (it reschedules if not), it restores from a
*recent local* snapshot, and it uses the same self-restart path
(`StartCleanup` → `CleanupRequestedFlag` → `Stop` → restart →
`CheckAndRestoreOnStartup`). It must never be confused with the §5.2 recovery,
which is about being *behind*, runs regardless of `snapshot_restore.enable`, and
prefers a *remote* snapshot.

- The DAG-side semantics of a snapshot (what it is, rootedness against it, the relaxed-determinism
boundary slot) are in `dag_semantics.md` §2.4–§2.5.
