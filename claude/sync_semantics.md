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

- **Attacher agnosticism (the central principle).** The attacher must know
  *nothing* about forward sync, the LRB, the network frontier, or any other
  synchronisation concept. It does exactly one thing: recurse the past cone via
  pulls, bounded by a **depth cap that is a pure constant given the
  configuration**. Today it is coupled — `isDepthCapped()` consults
  `LatestForwardSyncedTimestamp()` (it "knows" forward sync) — and that coupling
  is the 2026-06-20 freeze (§2, §6.1). The fix removes the frontier term entirely;
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
  is available. Config flips from `sync.disable` (default `false` = on) to
  `sync.enabled` (default `false` = off) — see §3.
- **Refuse, don't wait, beyond the cap.** A node whose recursion reaches the depth
  cap is too far behind for recursive sync alone. It must **refuse to sync** and
  surface that to the operator (who restores a newer snapshot, or enables forward
  sync), rather than poll-waiting for a forward sync that may be off (§2.1, §4).
  This decision lives in the sync-orchestration layer, never in the attacher.
- **General prune of stalled/orphaned attachers** (§2.1, §4) — wall-clock orphan
  detection that overrides attacher pins, regardless of attacher origin. Not yet
  present; its absence is exactly the 2026-06-18 leak.
- **Snapshot-selection criteria** (§5) are still a placeholder to be filled in.
- **Age-decayed coverage in LRB / fork choice** (§6). Today LRB selection is pure
  biggest-coverage; the intended rule discounts a branch's coverage by the age of
  its tip (halving-weighted, the token-coverage analogue of longest-chain depth),
  so a node cannot pin itself to a high-coverage *dead* ancestor. Not implemented —
  its absence is the coverage-monotonicity wedge (§6).

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
forward-sync frontier, not the LRB, not whether forward sync is even running. Its
only base case for terminating the recursion is the one in §2 above — **the
dependency is already in committed (rooted) state.** Any coupling of the cap to a
synchronisation concept (today: `depTs.After(LatestForwardSyncedTimestamp())`) is a
bug — it is exactly the 2026-06-20 freeze (§6.1), where a node with the branches in
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

## 3. Forward sync (optional, off by default)

Forward sync **helps** recursive sync. It is a separate process, **off by default**
and enabled by the operator only as a fallback (below). Config: `sync.enabled`,
default `false`. (This replaces the older `sync.disable` default-`false`/on flag —
the polarity flips so the default is *off*.)

**Why it is off by default.** Forward sync was originally on by default as a
catch-up accelerator, but it accreted more complexity and failure modes than it
removed: the gossip-shed dead-zone, the deferred-commit freeze (received branches
never finalized without it), and — worst — the attacher coupling that made
recursive sync *depend* on forward sync's frontier and froze nodes when it was off
(§6.1). The lesson: **catch-up should not hinge on a second, stateful, directional
subsystem.** Recursive sync (§2) plus a snapshot for a closer baseline (§5) is the
complete and simpler model; forward sync is kept only for the case where no
suitable snapshot is available.

How it works (when enabled):

- Forward sync **builds state starting from the node's current state and moves
  forward**, the opposite direction to recursive sync.
- It **requests branches in batches from sync sources** (a configured set of
  peers/endpoints) — slot by slot, in order, ahead of the node's committed tip —
  and **commits** them (via `ForceCommitBranch`), advancing the node's committed
  state toward the present. It follows the heaviest live lineage the sources
  advertise. Because it *commits* (not merely delivers) each branch, those branches
  become rooted — which is precisely what lets the agnostic attacher (§2) terminate
  its recursion on them, **without the attacher knowing forward sync produced them.**

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

**Meeting in the middle.** Forward sync follows the network's canonical LRB
lineage, not whatever branch any individual waiting attacher happens to want. The
two directions meet **by frontier coverage, not by branch matching**: as the
committed frontier advances along the canonical lineage, a waiting attacher
either solidifies (its baseline turned out to be on the now-committed lineage) or
is eventually reaped as an orphan by the pruner (§2.1, §4). This needs no
per-attacher lineage coordination.

---

## 4. Implementation notes

**At-cap counter (the neutral signal).** Maintain a single global atomic counter
of attachers that are **poll-only at the depth cap** — mirroring the existing
running-attacher counter. An attacher increments it when it goes poll-only at the
cap and decrements it when it leaves that state (its dependency becomes rooted as
committed state advances, or it is cancelled). The attacher touches *only this
counter* — a generic "I am blocked at the cap" integer; it does **not** reference
forward sync. The counter is the node's sole "am I behind?" signal, **consumed by
the sync-orchestration layer**, which decides:

- **forward sync enabled** → run forward sync exactly while the counter is non-zero
  (no "slots behind" computation, no hysteresis — §3);
- **forward sync disabled** → a sustained non-zero counter means the node is past
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
  node ever enters sync mode. (An earlier `maxGossipSlotsAheadOfLRB` shed, set
  below the cap, defeated this and was removed.)
- **During sync mode** (counter > 0): attach **only branches** (plus solicited).
  Non-sequencer txs and non-branch sequencer milestones are dropped — they feed
  the sequencer backlog / tippool, which are not needed for catch-up; they remain
  in the txstore and are pulled on demand if a branch's past cone requires them.
  Branches keep attaching because they anchor lineage and advance committed state.

This assumes the local sequencer is not active during sync; an active sequencer's
mempool would need a dedicated path. The node's OWN milestones always attach
(tagged as sequencer-sourced, treated as solicited).

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

---

## 6. LRB selection and the coverage-monotonicity wedge

Everything above presupposes a working answer to: **which branch is the latest
*reliable* branch?** Forward sync follows the LRB lineage advertised by sources
(§3); recursive sync, the state the node serves, and the local sequencer all build
on the node's own LRB. This section states the selection rule and a failure mode
*in the selection itself* — one that no amount of catch-up transport can fix,
because the transport is working and the node is declining what it receives.

**The rule (current).** Among candidate branches, the LRB is the one with the
**biggest ledger coverage** (`coverage_delta` — token coverage consolidated in the
branch's past cone over the sliding window), subject to the health floor
(`coverage_delta ≥ 2/3 · supply`). This is the node-local realization of the
biggest-coverage consensus rule.

**The missing term (intended, not implemented): recency / liveness.** Biggest
coverage correctly compares **competing chains at the frontier**. It has no notion
of "this branch is stale — nobody is extending it," so it can select a **dead
ancestor** of the live chain. The intended invariant: the LRB must be **both
highest-coverage and live**. Coverage must compare **across** competing chains, and
must **never** be used to prefer an **earlier point backward along one chain**.

**Realizing it: age-decayed coverage (the preferred model).** Rather than a hard
"within N slots" cutoff, fold recency into the *one* metric by discounting a
branch's coverage by the age of its tip — the token-coverage analogue of how
longest-chain weights *depth*. Compare branches by

```
W(B) = coverage_delta(B) · 2^(−age(B)/H)        age(B) = frontier − slot(tip(B))
```

where `H` is a halving period (tens of slots) and **`frontier` is the maximum tip
slot among the branches being compared — not wall-clock "now."** Keying the
discount to the candidate set (not the clock) keeps the comparison **objective**:
it is well-defined from the branches alone, and as nodes' candidate sets converge
via gossip they agree. LRB remains node-subjective (axiomatic), but convergence
still holds in the limit because `2^(−age/H) → 0` makes a frozen branch lose to
*any* live chain for *any* `H` — eventually, and equally for every observer.

Properties:
- **Dissolves the wedge (§6.1) with no cliff.** A frozen ancestor's weight decays
  below the live tip after only a few stale slots (in §6.2, the 7.3% coverage
  deficit is overcome once `age/H > 0.11`), and the trade-off "slightly stale but
  heavy" vs "fresh but light" is smooth instead of threshold-gated.
- **Orthogonal to the existing window.** `coverage_delta` is already a
  *within-branch* sliding-window sum; this adds a *cross-branch tip-age* discount
  on top. They do not conflict.
- **Node-local, no hardfork (to verify).** `coverage_delta` stays the on-chain,
  cross-checked value; the decay is applied only at *comparison* time when a node
  chooses its head. LRB selection has no consensus-validated binding, so this is a
  fork-choice algorithm change, not a ledger change — deployable without a
  hardfork. (Confirm before relying on it.)

### 6.1 The wedge

Distinct from the divergent-lineage case (§2.1): there the node is on a fork the
network abandoned and genuinely cannot bridge. **Here the node is on the *same*
lineage as the network** — the same sequencer chain — and still will not move
forward, because coverage **decreased** along that chain.

Coverage along a single sequencer's chain is normally non-decreasing. It **drops**
when a contributing sequencer stops being included — its milestones no longer fit
the proposer's consolidation window, typically a lagging, low-capital, high-RTT
peer. With a contributor of mass `m` falling out, `coverage_delta` steps down by
≈ `m` at that slot. From then on:

- the node holding the **pre-drop ancestor** (coverage `C`) sees every **live
  newer branch** on the same chain at coverage `C − m < C`;
- biggest-coverage selection therefore **keeps the frozen ancestor** and refuses
  to advance to the live continuation of its own chain;
- the live chain can **never re-attain `C`** when the excluded contributor is the
  very node stuck behind the ancestor — the mass that would lift coverage back to
  `C` is the node that won't join. **Self-reinforcing and permanent.**

So "a node on the wrong branch auto-reverts to the biggest coverage" **fails
exactly here**: the biggest-coverage branch *is* the node's own — it is simply
dead; there is nothing larger to revert to. The node also has no reason to pull
(it believes it holds the best branch) and, not being flooded, never trips the
forward-sync trigger (§4) — none of the catch-up machinery engages.

**This is a fork-choice bug, not a transport bug — which locates the fix.** The
two sync directions play different roles here:

- **Recursive sync (§2) is immune.** It makes no fork-choice decision — it is
  demand-driven backward from received tips and simply attaches whatever it pulls.
  In §6.2 it had already done its job: loc0 *has* the live branches in its store.
  Receiving was never the problem.
- **The fork-choice metric is consumed by (a) the head/LRB the node selects** (what
  it serves and what its own sequencer builds on) **and (b) forward sync's
  *direction*** (which lineage it requests from sources). These are where a wrong
  metric bites: with raw biggest-coverage, the node commits the live branches yet
  still refuses to *select* them, and forward sync would be steered along the dead
  lineage.

So the locus is the **fork-choice metric** (consulted by head-selection and
forward sync), not recursive sync. Fixing the metric (age-decayed coverage, above) requires
no change to recursive sync. One caveat: recursive sync stays immune only while the
node keeps pulling the frontier; a node that has gone quiet on pulls because it
"believes it is synced" is also helped by the decayed metric, since it would no
longer rank its stale branch best and would resume chasing the frontier.

**Two layered bugs — both must be fixed.** The "recursive sync is immune" statement
above holds for the observed state *with forward sync on* (loc0 had received and
committed the live branches; only selection rejected them). Disabling forward sync
to probe further (2026-06-20) exposed a **second, independent** bug: recursive sync
*cannot even commit* the live branches on its own, because the attacher's depth cap
was coupled to the forward-sync frontier (§2) — with forward sync off the frontier
never advances, so every beyond-cap dependency stays capped and 151 attachers froze
with **zero** branch commits. So:

1. **Transport/attacher freeze (§2)** — remove the forward-sync coupling; cap is a
   pure config constant; recursive sync commits the gap from the local txstore. This
   is what lets a node *reach and commit* the live branches without forward sync.
2. **Fork-choice (§6.3)** — age-decayed coverage, so once the node *has* the live
   branches it actually *selects* them over its dead higher-coverage ancestor.

Fix (1) makes the branches available to choose; fix (2) makes the node choose
right. Neither alone resolves the loc0 wedge.

### 6.2 Worked example (loc0, 2026-06-20) — established topology

Three sequencers: big `9d2c` (890T), small `85c3` (10.4T), and loc0's own `bda1`
(71.3T — smallest, on the highest-RTT box).

- loc0's LRB and the network's LRB are **both branches of the same sequencer
  `9d2c`** ⇒ one chain (a sequencer cannot validly fork its own chain — ChainID
  preservation), so loc0's LRB is an **ancestor** of the live tip.
- loc0's LRB: `num_seq = 3` (includes loc0), `coverage_delta = 971.7T`, **frozen**
  (unchanged 30+ min). Network LRB: `num_seq = 2` (loc0 excluded),
  `coverage_delta = 900.4T` (= `971.7 − 71.3`, exactly loc0's mass), **live**.
- loc0 **has** the big sequencer's newer txs in its store, yet
  `check_txid_in_lrb` → depth −1 (present, not on its LRB lineage); `nonseq_drop =
  0`, `pullRequestsOut = 0`, connected to both peers running `9d2c` and `85c3`.

With forward sync **on**, reception was not the blocker: loc0 received *and
committed* the live branches and merely **declined to select them** — that part of
the wedge is §6's selection rule. Disabling forward sync to probe (configs set
`sync.disable=true`, both loc0 nodes restarted) then exposed the layered transport
bug: over 2 minutes the **LRB stayed frozen at 49969 while current_slot climbed
50237→50247** (behind 268→278), with **151 attachers piled up and `branch_mutations
= 0`** — recursive sync could not commit a single branch because the depth cap was
chained to the now-frozen forward-sync frontier (§2). Both bugs are real and
independent (§6.1).

### 6.3 Fix directions (intended; none implemented — do not hack)

- **(T) Decouple the attacher from forward sync (§2) — prerequisite.** Make the
  depth cap a pure config constant and drop the `LatestForwardSyncedTimestamp`
  term, so recursive sync can reach and commit the live branches from the local
  txstore without forward sync. Without this the node cannot even *obtain* a
  committed live branch to choose; it is the fix for bug #1 (§6.1).
- **(A) Age-decayed coverage fork-choice — selection.** Compare branches by
  age-discounted coverage `W(B)` (above), applied wherever the node ranks
  branches — head/LRB selection and forward sync's direction. Smooth, cliff-free
  realization of the §6 invariant: the frozen ancestor's weight decays below the
  live tip within a few stale slots. Subsumes the earlier hard "recency-gate" and
  "follow-the-chain-tip" sketches — both are special cases of decaying the stale
  branch's weight. This is the fix for bug #2 (§6.1).
- **(C) Don't manufacture the trap (complementary, proposer-side).** A more
  forgiving inclusion window for a lagging-but-present-and-reachable sequencer
  keeps the chain's coverage monotone, so the dead high-coverage ancestor is never
  created in the first place.

(T) is the prerequisite (the node must be able to commit the live branches); (A)
then makes it select them; (C) prevents most exclusions up front. The only recovery
available today is a snapshot
restart on the live lineage (§5) — it works but does **not** fix the gap: the node
re-diverges the next time it lags.

*(Diagnostic aside: the network-mapping overlay — connectivity map / distance
matrix / `/netviz` — is read-only and not implicated in any sync wedge; hboot and
hloc0 run the identical binary and stay synced. It was only useful here as a
diagnostic, since masked names made it trivial to see which node held which
branch.)*
