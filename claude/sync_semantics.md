# Sync semantics — how a node catches up with the network

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

- **Per-branch recursion depth** (§2.1). Today the attacher counts a per-*vertex*
  attachment depth; the intended metric counts *branches* (lineage distance). This
  is the central change — it makes "at the cap" mean "genuinely far behind" and
  eliminates the false-cap-at-the-tip that caused the 2026-06-18 leak.
- **Counter-based, no-hysteresis trigger** (§3, §4). Today forward sync is gated
  on a "slots behind" up/down threshold; the intended trigger is the global
  poll-only-at-max-depth attacher counter.
- **No attachment timeout; pull-only deadline** (§2). The current code parks
  depth-capped dependencies with neither pull nor deadline; the intended split is
  attachment-never-times-out + per-pull deadline.
- **General prune of stalled/orphaned attachers** (§2.1, §4) — wall-clock orphan
  detection that overrides attacher pins, regardless of attacher origin. Not yet
  present; its absence is exactly the 2026-06-18 leak.
- **Snapshot-selection criteria** (§5) are still a placeholder to be filled in.

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
  issuing milestones and branches, slot after slot, so the latest reliable branch
  (LRB) the network agrees on is always moving forward.
- **Syncing is catching up with that advancing state.** A node is *synced* when
  its own state tracks the network's consensus state within a small, steady-state
  lag (a slot or a few); it is *behind* otherwise.
- Whenever a node **starts**, or **disconnects** and reconnects, its state is
  usually behind the consensus state — by anything **from one or a few slots to
  arbitrarily many (unbounded)**, depending on how long it was absent.
- Closing that gap is **normal node operation**, not an exceptional mode. The
  machinery below runs continuously; "being in sync" is just the case where the
  gap it is closing happens to be small.

Two complementary mechanisms close the gap:

1. **Recursive sync** (§2) — always-on, demand-driven, walks the past *backward*
   from received tips until it meets state the node already has.
2. **Forward sync** (§3) — optional, batch-driven, builds the state *forward* from
   what the node already has by requesting branches in order from sync sources.

They work toward each other and **meet in the middle** (§3–§4).

Starting a fresh node from a **snapshot** is a third, distinct path and gets its
own chapter (§5), to be written later.

Note that it is not guaranteed that node can sync with the network always: it can happen
that node starts from the snapshot state that is not in the lineage of states currently network
is evolving on. In that case process will fail or stall indefinitely.

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

The depth cap (§2.1) sits between these two. A dependency **beyond** the cap is
**not pulled at all**, so it has no pull deadline — the attacher just polls and
waits for forward sync to deliver it. A dependency **within** the cap **is**
pulled and is subject to the pull deadline. The switch is automatic: as forward
sync advances and the awaited branch arrives, a previously-capped dependency
drops below the cap, becomes eligible to pull, and from then on is governed by
the pull deadline like any other.

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

**Recursion depth cap.** The backward recursion **stops at a maximum depth**
(tentatively **50 branches** when forward sync is enabled; larger when it is
disabled — see §4). At the cap the attacher **stops descending and does not pull
the next branch back**; it simply **polls and waits** for that branch to arrive
and be committed (normally delivered by forward sync, §3). This is still the
normal attachment process — there is no attachment timeout — only held on hold by
the cap. An attacher in this state is *poll-only*; the count of such attachers is
the node's sole "am I behind?" signal and the trigger for forward sync (§4).

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
committed slot is far behind** the network (beyond a configurable constant), the
node **tries to find a younger snapshot in the network and start a new DB from
it** (§5), instead of trying to recurse across an enormous gap. This replaces a
hopeless deep recursion with a clean cold start much closer to the present.

**Too old, no snapshot available → refuse to sync.** If the node's state is old
and **no suitable younger snapshot** is available on the network, the node
**refuses to sync** rather than churning indefinitely. This is a deliberate
give-up: it surfaces the situation to the operator instead of pretending to make progress.

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

## 3. Forward sync (optional, enabled by default)

Forward sync **helps** recursive sync. It is a separate process, **enabled by
default but can be disabled**.

How it works:

- Forward sync **builds state starting from the node's current state and moves
  forward**, the opposite direction to recursive sync.
- It **requests branches in batches from sync sources** (a configured set of
  peers/endpoints) — slot by slot, in order, ahead of the node's committed tip —
  and commits them, advancing the node's state toward the present. It follows the
  **LRB (latest reliable branch) lineage advertised by those sources** — the
  heaviest lineage the network is converging on.

Why it exists:

- Pure recursive sync is demand-driven and backward; over a large gap it can be
  slow and bursty. Forward sync supplies an **orderly, batched, forward-moving**
  stream of branches that the node commits in lineage order, which is far more
  efficient for closing a large, mostly-empty or steadily-advancing gap.
- Being optional, it can be turned off on nodes where recursive sync alone is
  acceptable, or for diagnosis.

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

**Meeting in the middle.** Forward sync follows the network's canonical LRB
lineage, not whatever branch any individual waiting attacher happens to want. The
two directions meet **by frontier coverage, not by branch matching**: as the
committed frontier advances along the canonical lineage, a waiting attacher
either solidifies (its baseline turned out to be on the now-committed lineage) or
is eventually reaped as an orphan by the pruner (§2.1, §4). This needs no
per-attacher lineage coordination.

---

## 4. Implementation notes

**Sync-mode counter.** Maintain a single global atomic counter of attachers that
have reached the max depth and are **poll-only** (waiting, not pulling) —
mirroring the existing running-attacher counter. An attacher increments it when
it goes poll-only at the cap and decrements it when it leaves that state
(solidifies, un-caps as the frontier advances, or is cancelled). **The node is in
sync mode iff this counter is non-zero**, and forward sync (if enabled) runs
exactly while it is non-zero. That is the whole trigger — no "slots behind"
computation, no hysteresis (§3).

**Max depth.** Counted in branches (§2.1). Tentatively **50** when forward sync is
enabled; larger (e.g. **1000**) when it is disabled, since recursion is then the
only forward mechanism and must reach further before giving up.

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

**Inbound filters during sync.** Pulled sequencer transactions should be exempt
from inbound transaction filters during sync; otherwise the normal incoming flow
may be dropped and then pulled again. This part needs careful evaluation.

---

## 5. Starting from a snapshot

Cold start from fresh snapshot should happen: 
- when multi-state database is absent or corrupted
- when current state is too old, say older than 8000 slots.

Snapshot is requested from trusted sources available in the config or locally. 
Criteria how snapshot is selected are already implemented <must be listed here>.

If snapshot is needed:
- node restores snapshot, probably replacing the old database, and runs as usual. That will trigger
sync processes automatically
- if snapshot is not available, node refuses to start

- The DAG-side semantics of a snapshot (what it is, rootedness against it, the relaxed-determinism
boundary slot) are in `dag_semantics.md` §2.4–§2.5.
