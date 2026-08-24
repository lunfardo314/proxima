# Delegation freeze-epoch distribution — spec

> **LIVE** — Amount-weighted balancer spreading freeze epochs across the reachable window. Implemented; the conceptual half is queued for `overview/delegation.md`.
> **Binds:** `sequencer/delegationpool`, `sequencer/task/proposal.go`

Status: **IMPLEMENTED** on develop. Refined 2026-06-19, then **2026-07-06**:
continuations now run the same balancer as first-time freezes (§4). The earlier
continuation-to-max rule collapsed all delegations onto one epoch when they were
re-frozen together — a network restart re-froze every delegation of a target in
the same slot — and never recovered; it is removed.

## Implementation notes (as built)

Shipped in `sequencer/delegationpool/delegationpool.go` + wiring in
`sequencer/sequencer.go`, `sequencer/strategy.go`, `sequencer/task/task.go`,
`sequencer/task/proposal.go`. Unit test `sequencer/task/freeze_epoch_test.go`
(`latestArgmin`); end-to-end covered by `tests/` Freeze + Factory suites (pass).
Deviations from the design below, all behaviour-preserving simplifications:

- **Discovery has no separate backlog struct.** The `ListenToControllerAccount`
  callback enrolls new delegations directly into the pool as `Undef` entries
  (`confirmed=false`); `Reconcile` evicts ones still absent from the LRB after
  `BacklogDelegationTTLSlots` (the previously-unused half of `BacklogTTLSlots()`,
  default 20) — same push-discovery + TTL semantics, fewer moving parts.
- **One `Reconcile`, timer-driven.** A 1 s background loop both reconciles
  pending transitions (§5.4.1) and runs the TTL eviction (§5.4.2), instead of a
  branch-edge trigger. Both operate only on small suspect subsets.
- **Bootstrap is nil-safe & best-effort** via `Branches().FindLatestReliableBranch()`
  (not `LatestReliableState()`, which panics on a nil root at early startup).
- **No standalone full rebuild method.** Freeze-state is event-authoritative;
  the only scan is the startup bootstrap.

### Live-validation fixes (2026-06-19)

The first cut spread correctly for a slow trickle but **piled all later freezes
onto one epoch** under sustained delegation. Local-net testing (several dozen
`proxi node delegate amount`) pinned two bugs in the live event path; both fixed
in `delegationpool.go`, after which 34 delegations spread evenly (2 each on the
first 14 reachable epochs, 1 each on the rest — no cliff):

1. **`ApplyMilestone` must walk the own-milestone chain, not just process the
   latest milestone.** `milestoneWatcher` (`strategy_async.go`) reports only the
   *latest* own milestone; when the chain advances by >1 between polls the
   intermediate milestones' freezes were never credited. Fix: walk the sequencer
   predecessor chain from the observed milestone back to the last-applied one,
   applying every milestone's transitions. **Do not stop at a branch** — branches
   are frequently the latest milestone, and the freezes live in the non-branch
   milestones before them; the `lastApplied` bound + a depth cap keep the walk
   short.
2. **`Reconcile` must not void a pending freeze just because its successor output
   isn't in the committed LRB yet.** A freeze lives in a non-branch milestone and
   is not in state until the next branch commits; the original presence-check
   falsely voided recent freezes, reverting the entry to `Undef` and dropping it
   from the load vector `D` → pile-up. Fix: reconcile **by ChainID**
   (`GetChainOutputWithChainID`) — adopt the LRB's state when committed
   (`Frozen`/`OnHold`), **keep** the pending while the delegation is still `Undef`
   in the LRB (freeze not committed yet), and drop only when absent and aged out.
   Self-healing; never false-voids.

---
 Replaces
the buggy `optimalFreezeEpoch` / `selectDelegationsToFreeze` logic in
`sequencer/task/proposal.go`. Diagnosis of the old bug: `claude` memory
`project_delegation_freeze_distribution_bug.md`.

The 2026-06-14 draft proposed a *minimal* write-through cache holding only the
aggregate load vector `D` and kept the per-proposal trie scan for candidate
selection. This revision **replaces both** with a richer per-sequencer
in-memory model, `DelegationPool`, that also eliminates the per-proposal trie
scan. The freeze-epoch selection math (§3, §4, §6–§8) is unchanged from the
agreed draft; only the *sourcing* of the data (§5) is redesigned.

## 1. Where the epoch is chosen

The unfreeze epoch of a delegation is **not** chosen by the delegator. `proxi
node dlg --epochs` only sets the per-output cap `MaxFrozenEpochs` (default `0` →
parsed back as the target's max, see `lock_delegate.go` `MaxFrozenEpochs==0 →
TargetMaxFrozenEpochs`). The **target sequencer** picks the actual freeze
duration each time it freezes a delegation, in `sequencer/task/proposal.go`:

- `selectDelegationsToFreeze()` builds the per-epoch load and assigns a
  `freezeUntilEpoch` to every freezable delegation output of this target.
- `optimalFreezeEpoch()` is the per-delegation selection.
- `txbuilder_seq.FreezeDelegation(o, freezeUntilEpoch)` writes the chosen epoch
  into the successor (`MakeDelegationFreezeOutput`), bounded to
  `[txEpoch, FreezeUntilMax]` (`txbuilder_seq.go:386-394`).

## 2. Goal

Spread the **unfrozen amount** as evenly as possible across the reachable
epochs of a given sequencer, so the coverage released per epoch (and the
matching drop of the sequencer's frozen-coverage) is smooth rather than
arriving in cliffs. "First ~K delegations occupy K distinct epochs before any
collision; thereafter ~1/K of the delegated amount unfreezes per epoch."

The old code balanced the **count** of delegations per epoch. Coverage
fluctuation is proportional to **amount**, not count, so the metric is
amount-weighting. That, plus the two state/clamp bugs (§4, §5), are the
functional changes.

Secondary goal, and the reason for the richer structure: **scalability**. A
busy sequencer may hold thousands of delegations frozen at once.
`selectDelegationsToFreeze` currently does a full `IterateDelegatedOutputs` trie
scan **every proposal** (every ~12 ticks, once per parallel proposer). That
cost grows with the delegation count and is paid even when nothing changed.
`DelegationPool` keeps the model in memory and serves candidates + `D` from RAM,
reducing per-proposal state access from an O(all delegations) scan to O(freezes
actually applied) point reads.

## 3. Definitions

Let the target sequencer's inlined params (read via
`SeqTxBuilder.ChainDelegationParams()`, sourced from the sequencer constraint at
output index 4) be:

- `S = chainEpochSlots`     (epoch length in slots, ∈ [500, 2000])
- `N = chainMaxFrozenEpochs` (∈ [8, 32]; default 20)

For the proposal at timestamp `ts`:

- `txEpoch = EpochFromSlotDirect(targetID, ts.Slot, S)` — current epoch.
- The **reachable window** is the absolute epochs `[txEpoch, txEpoch + N - 1]`,
  indexed by relative position `i ∈ [0, N-1]` (absolute epoch `txEpoch + i`).

Load vector **D** over the window:

```
D[i] = Σ TokenBalance(d)   over every delegation d of this target that is
       currently frozen with LastFrozenEpoch == txEpoch + i
```

"Currently frozen" = `IsInFrozenSlot(ts.Slot)` in the effective state at the
proposal's tip. Candidates being placed in this same proposal are *not* yet in
`D`; they are added as they are assigned (§4).

For a single delegation `d` being (re)frozen, its own reachable cap is

```
K(d) = FreezeUntilMax(ts) = txEpoch + d.MaxFrozenEpochs - 1   (absolute)
```

i.e. relative indices `[0, d.MaxFrozenEpochs - 1] ⊆ [0, N-1]`.

## 4. Selection rule

**One rule for every freeze**, first-time and continuation alike. The delegation's
`DelegateLockState.State` (`Undef = 0`, `Frozen = 1`, `OnHold = 2`) no longer
splits the logic — `Frozen` (a continuation whose window has elapsed) and `Undef`
(a first-time freeze) are treated identically. Pick the **longest freeze that does
not concentrate** `D`: the latest least-loaded epoch within the delegation's cap.

```
choose i* = max { i ∈ [0, d.MaxFrozenEpochs - 1] : D[i] is minimal over that range }
freezeUntilEpoch(d) = txEpoch + i*
then D[i*] += TokenBalance(d)        // so later placements in this pass see it
```

Behaviour of this single rule:

- If any reachable `D[i] == 0`, zero is the minimum, and the `max`-index
  tie-break picks the **farthest empty epoch** — longest freeze, fills the
  window from the far end inward.
- Once no reachable epoch is empty, the minimum is the least-loaded epoch and we
  pick the latest one tied for it (latest `argmin D`).

The `max`-index tie-break (later/longer freeze wins among equal-minimum epochs,
within the cap) is deliberate: a longer freeze is economically preferred by the
delegator — it commits for more epochs and defers the coverage release, and while
unfrozen the delegation earns no inflation. So whenever the load is indifferent
the rule hands out the longest reachable freeze; it shortens a freeze **only** to
avoid piling onto an already-loaded epoch. That is exactly the delegator/sequencer
compromise: longest possible freeze, subject to keeping `D` even.

Selection must be restricted to `i ≤ d.MaxFrozenEpochs - 1` **before** taking the
argmin (never select globally and clamp afterwards — the secondary clamp-after
bug piled several small-cap delegations onto the same boundary epoch while lower
epochs sat empty).

### Why continuations must rebalance too (the change from the earlier design)

The earlier design froze continuations to the fixed maximum `txEpoch +
d.MaxFrozenEpochs - 1` and skipped the balancer, arguing that a constant period
preserves the phase set at the first freeze. That holds **only** if every
delegation is re-frozen promptly and in its own distinct epoch. It isn't: the
maximum is anchored to `txEpoch` (the epoch of the *re-freeze*), not to the
delegation's established phase, so **any delegations re-frozen in the same epoch
collapse onto the same unfreeze epoch** — and, being balancer-blind, never
separate again. This is a one-way ratchet into concentration.

The live trigger (observed on the testnet): the network stalled and, on restart,
**all** delegations of a target became freezable at once and were re-frozen in the
same slot. Every one got `txEpoch + N - 1` → all piled onto a single epoch;
subsequent cycles kept them fused. Two delegations whose first freezes were
correctly one epoch apart (`92` and `93`) were fused onto epoch `121` by one
continuation.

Running the balancer on continuations fixes both directions: it re-spreads a
batch re-frozen together (each takes a distinct least-loaded epoch, filling the
window inward) and it self-heals an existing concentration over the following
cycles, all while still granting the longest freeze the load allows. The phase is
no longer preserved as an invariant; instead an even `D` is maintained directly,
which is the actual goal (smooth coverage release). Mixed caps and non-prompt
re-freeze — the fragile assumptions of the old continuation rule (§7) — no longer
matter.

(`OnHold` outputs are revoked / master-controlled and are never freeze
candidates — `IsUnlockableByTargetForFreezing` already excludes them.)

## 5. `DelegationPool` — the in-memory model (the core redesign)

The selection rule is correct only if `D` reflects **all freezes already
committed in the current slot's milestone chain**, not just the previous slot's
committed baseline branch. The old code reads `p.StateReader()` =
`BaselineSugaredStateReader()` = the previous slot's branch, so within one slot a
later milestone cannot see freezes done by an earlier one, and
`optimalFreezeEpoch` returns the same top epoch every milestone — the spread
advances only ~one epoch per slot. That stale baseline is the dominant cause of
the observed collisions.

The fix is a per-sequencer in-memory structure, `DelegationPool`, that is the
sequencer's live model of the delegations targeted at it. It is the **single
source** the proposer reads for both the candidate set and `D`.

### 5.1 Why a cache here is justified (and the constraints on it)

This is a deliberate, documented exception to the no-cache / no-refcount rule
(`feedback_cache_and_refcount.md`), narrowly scoped:

- **Optimization-only; light, temporary deviations are non-critical.**
  `FreezeDelegation` bounds the chosen epoch to `[txEpoch, FreezeUntilMax]` and
  re-validates `IsUnlockableByTargetForFreezing` and `Target == chainID` at apply
  time; the proposer still runs `IsConsumedInThePastPath` against the real tip. A
  stale or wrong pool yields at worst a missed freeze opportunity (delegation
  frozen one slot later) or a wasted `FreezeDelegation` attempt that returns
  `valid=false`/err and is skipped. It can never produce an invalid tx or a
  consensus divergence — so the pool may safely drift from the exact state for
  short periods.
- **Freeze-state is sequencer-controlled and deterministic; the LRB is not its
  authority.** Every state transition that matters for scheduling is authored by
  the target sequencer: it freezes, it re-freezes, and it performs the
  `→ OnHold` / unfreeze in reaction to a master's askStop request (the sequencer
  could censor an askStop, but that is malicious behaviour, made trustless by the
  ledger-enforced **safe revocation window**). The sequencer knows its own
  decisions before they reach the (slot-lagging) LRB, so the pool's freeze-state
  is maintained **incrementally from the sequencer's own milestones**, not
  rebuilt from the LRB. Two things the sequencer does **not** author and cannot
  know without reading state: a **new delegation** (created by a delegator) and a
  **master reclaim/revoke during the safe revocation window**. The window's
  arrival slot is itself deterministic and known to the pool; what is unknown is
  whether the master actually reclaimed in it — so the LRB is consulted only to
  (a) bootstrap at startup, (b) confirm/void the sequencer's own tentative
  transitions (§5.4), and (c) supply the objective delegation state immediately
  before the sequencer consumes it (§5.5, mandatory). No periodic authoritative
  full rebuild.
- **No reference counting.** Entries are keyed by delegation `ChainID` (stable
  across freeze transitions; the output ID changes each freeze) and are plain
  overwrite/delete. No refcounts, no liveness tracking.

This tension with `feedback_cache_and_refcount.md` is real and must be signed
off explicitly (the rule's author requested this design). The justification is
the optimization-only property above.

### 5.2 Contents

One `DelegationPool` per `Sequencer`, guarded by its own `sync.RWMutex` (never
held across a trie scan — the `ownMilestonesMutex` lock-convoy lesson).

Per delegation, keyed by `ChainID`, a slim record:

```
type delegationEntry struct {
    outputID          base.OutputID // current confirmed UTXO, to fetch + consume at freeze time
    amount            uint64        // TokenBalance — weight in D and the release magnitude
    state             byte          // confirmed (settled) state: Undef / Frozen / OnHold
    lastFrozenEpoch   uint32        // confirmed last-frozen epoch; 0 if never frozen
    maxFrozenEpochs   byte          // the per-output cap K(d)
    freezableFromSlot uint32        // earliest slot at which IsUnlockableByTargetForFreezing is true

    // tentative sequencer-authored transition applied this slot but not yet
    // confirmed in the LRB (§5.3, §5.4). nil once settled. While set the entry is
    // NOT a candidate; a pending freeze counts toward D at pending.untilEpoch.
    pending *pendingTransition
}

type pendingTransition struct {
    kind        byte          // freeze | unfreeze/onHold (sequencer's askStop response)
    slot        uint32        // ledger slot in which the transition was applied
    untilEpoch  uint32        // freeze only: assigned freezeUntilEpoch (its position in D)
    successorID base.OutputID // the produced output — used to confirm/void against the LRB
}
```

A freeze and a sequencer-authored unfreeze/OnHold use the same `pending`
machinery because both are milestone-subjective: the milestone carrying them may
orphan, so neither is believed until confirmed (§5.4).

`freezableFromSlot` is precomputed once, when the full output is in hand, by
evaluating the ledger predicate — not by replicating its logic on slim fields.
The predicate is monotone in slot between transitions (once the frozen window +
safe-revocation window has elapsed it stays freezable until the next freeze), so
a single threshold slot is exact.

The pool does **not** store the parsed `*ledger.DelegationOutput` (heavy). The
full output is fetched by ID only for the handful of candidates actually
selected to freeze (§5.5).

The aggregate `D` is **derived** from the entries on demand for a given
`(txEpoch, N)`: sum `amount` of frozen entries by epoch, where a pending entry
counts at `pending.untilEpoch` and a settled `Frozen` entry counts at
`lastFrozenEpoch`. It is not stored separately, so it cannot drift from the
entries.

### 5.3 The inside-slot race: freezes are tentative until confirmed

The pool is **global** to the sequencer, but a freeze is **local to the
milestone that carried it**. A sequencer issues several milestones per slot;
when freeze time comes it freezes (consumes) a given delegation in whichever
milestones it generates — but only one milestone chain survives into the LRB.
If milestone `M1` froze delegation `d` and `M1` later orphans while sibling `M2`
survives, the global pool must not permanently believe `d` is frozen: on the
surviving lineage `d` is still freezable.

(Candidate-level correctness is already handled per-tip by
`IsConsumedInThePastPath(d.ID, tip)`: a freeze in an orphaned milestone is not in
the surviving tip's past cone, so `d` re-enters the candidate set automatically.
The pool's extra job is only to keep `D` and the entry `state` from being
permanently corrupted by an orphaned freeze.)

So a freeze is recorded **tentatively**:

- Applied on **own-milestone acceptance** — the existing `onMilestoneConfirmed`
  hook (`strategy.go:10`), next to `AddOwnMilestone`. The build loop is gated by
  `pendingSubmit` (`strategy_async.go:203-219`), so the next proposal does not
  start until the just-submitted milestone is observed — the pending freeze is in
  place before it is read.
- For each delegation transition in the accepted tx (derive from its consumed/old
  + produced/new delegation outputs, both present):
  - **freeze**: set `entry.pending = {kind:freeze, slot, untilEpoch, successorID}`.
    The `state`/`lastFrozenEpoch`/`outputID` fields keep their *confirmed* values
    until reconciliation; `D` counts the entry at `pending.untilEpoch`. While
    pending, the entry is not a candidate.
  - **unfreeze / `→ OnHold`** (the sequencer's response to a master askStop):
    set `entry.pending = {kind:unfreeze, slot, successorID}`. Removed from `D`,
    not a candidate. Reconciled the same way (the carrying milestone may orphan).

The safe-revocation-window arrival slot is deterministic; the pool computes it
(it is what `freezableFromSlot` waits out) without any state read. What the pool
cannot know — whether the master reclaimed *during* that window — is resolved
only by the consume-time state read (§5.5).

### 5.4 Reconciliation against the LRB (cheap, targeted — no full rebuild)

Two cheap, bounded reconciliations driven off `onMilestoneConfirmed` /
slot-edge; neither scans all delegations:

1. **Settle/void tentative freezes** of the *previous* slot. For each entry whose
   `pending.slot` is now older than the current slot, do a single point read
   against the LRB for `pending.successorID` (or look up the delegation's current
   output by `ChainID`):
   - present/confirmed → promote: `state=Frozen`, `lastFrozenEpoch=pending.untilEpoch`,
     `outputID=successorID`, recompute `freezableFromSlot`, clear `pending`.
   - absent (the freezing milestone orphaned) → discard `pending`; the entry
     reverts to its prior confirmed state and is freezable again.
   This is O(freezes in the just-ended slot) — a small set — not O(all
   delegations).
2. **Discover new delegations** — the only externally-authored event needing
   proactive discovery, because (unlike a master reclaim) nothing else surfaces
   it: an unknown delegation is never selected, so it is never freeze-attempted.
   A master **reclaim** during the safe window needs *no* discovery — it is
   caught lazily at consume time (§5.5): the point read finds the delegation gone
   or changed, the freeze attempt is skipped, the dead entry dropped.

   Discovery is **push, not scan** — mirroring how the tag-along backlog forms.
   A `ListenToControllerAccount` listener on the sequencer's **delegation-target
   account** (delegation outputs index under their `Target`, distinct from the
   chain-lock account the tag-along backlog listens on) feeds a small
   *new-delegation backlog*: every freshly-seen delegation UTXO targeted at this
   sequencer is enrolled with an arrival time and a **TTL eviction** for ones
   that never confirm (orphaned) — exactly the `TagAlongBacklog` pattern,
   including the depth-based purge of entries absent in the LRB
   (`backlog.go:295-360`). The plumbing is already half-present:
   `BacklogTTLSlots()` returns `(tagAlong, delegation)` and the delegation TTL is
   currently **unused** (`backlog.go:296-297`, `_ = ttlDelegationSlots`) — wire it
   here.

   New `ChainID`s drain from this backlog into the pool as `Undef` entries; the
   consume-time state read (§5.5) verifies them objectively before any freeze, so
   a listener entry that turns out orphaned/invalid costs only a skipped attempt.
   Known entries are never overwritten by discovery (freeze-state stays
   event-authoritative). A periodic state **scan is explicitly rejected**:
   limiting it to a recent slot-prefix is unsafe (a new delegation may arrive
   with an old timestamp), and a full scan is the authoritative rebuild we are
   avoiding.

Startup still does **one** `IterateDelegatedOutputs(target)` scan to bootstrap
the initial set before the first freeze (analogous to `LoadSequencerStartTips`
loading pending tag-along outputs).

### 5.5 Proposer read path

`selectDelegationsToFreeze()` no longer scans the trie. Instead:

1. Take a snapshot of the reachable window from the pool: candidate entries
   (`state != OnHold && pending == nil && currentSlot >= freezableFromSlot`) plus
   `D` over `[txEpoch, txEpoch+N-1]` (settled + pending contributions). Single
   brief RLock; copy out, release.
2. Sort candidates by amount desc (unchanged tie-break by output ts).
3. For each candidate, assign `freezeUntilEpoch` per §4 (first-time vs
   continuation), crediting in-pass assignments into the local `D` copy so
   multiple first-time delegations in one proposal still spread.
4. Fetch the full `*ledger.DelegationOutput` **by ID** from the state reader for
   each selected candidate. This point read is **mandatory, not just an
   optimization**: the sequencer cannot know whether the master reclaimed during
   the (deterministically-timed) safe-revocation window, so the state is the only
   objective source of the delegation's current status immediately before
   consuming it. If the output is gone/changed, skip and drop the pool entry.
   Then run the existing `IsConsumedInThePastPath` filter against the tip and
   `FreezeDelegation`; the apply-time re-validation (§5.1) absorbs any residual
   pool staleness.

This replaces the O(all delegations) per-proposal scan with O(selected) point
reads.

#### Fallback (if the pool is deferred)

The 2026-06-14 minimal form: keep the per-proposal trie scan for candidates,
add only the aggregate `D` write-through cache + amount-weighting + the clamp
fix + in-pass accounting. Cheaper to ship, fixes the *math* bugs, but does not
fix the per-proposal scan cost at scale and reintroduces the stale-baseline `D`
unless the same event discipline is applied to `D`. Floor, not the final form.

## 6. Amount used

`D` and the per-delegation contribution use `Output.TokenBalance()` (the
coverage a delegation contributes when unfrozen, and the magnitude of the
release at its unfreeze epoch). This matches the existing candidate sort key.
Accrued inflation is ignored (second-order).

## 7. Assumptions and known approximations

- **Mixed caps and non-prompt re-freeze are fine.** Because every freeze runs the
  balancer against the current `D` (§4), the distribution no longer depends on all
  delegations sharing one period or on the target re-freezing promptly in the epoch
  right after the window. A batch re-frozen late and together is simply spread
  across the least-loaded epochs; short-cap delegations balance within their own
  reachable range. These were the fragile assumptions of the old
  continuation-to-max rule and are gone.
- **Pool staleness is bounded and harmless.** New delegations and
  master-initiated changes are invisible until the next discovery pass (§5.4);
  a tentative freeze on an orphaned milestone is voided at the next slot edge
  (§5.4). Worst case is a one-slot-late or wasted freeze attempt (§5.1).

## 8. Worked example (fresh sequencer, N=20, equal amounts)

`D` starts all-zero. First-time delegations arrive (whatever the milestone/slot
spread, as long as the pool is current per §5):

| arrival | reachable min | i* (max-index argmin) | freezeUntilEpoch |
|---------|---------------|-----------------------|------------------|
| 1       | 0 everywhere  | 19                    | txEpoch+19       |
| 2       | 0 in [0..18]  | 18                    | txEpoch+18       |
| 3       | 0 in [0..17]  | 17                    | txEpoch+17       |
| …       | …             | …                     | …                |
| 20      | 0 only at 0   | 0                     | txEpoch+0        |
| 21      | all equal     | 19 (latest min)       | txEpoch+19       |

First 20 occupy 20 distinct epochs; #21 lands on the now-least-loaded (all
equal → latest) epoch. With unequal amounts, #21+ track `argmin D`.

Continuations follow the **same** table: a re-frozen delegation is just another
placement against the current `D`. A whole batch re-frozen in one slot (e.g. after
a network restart) fills the window inward exactly like arrivals 1..20 — it does
**not** collapse onto `txEpoch+19` as the old continuation rule did.

## 9. Implementation touch points

- `sequencer/delegation_pool.go` (new) — `DelegationPool` on the `Sequencer`:
  `map[base.ChainID]delegationEntry` + `RWMutex`. Methods:
  - `Bootstrap(rdr, target)` — the single startup `IterateDelegatedOutputs` scan
    that populates the initial set.
  - `ApplyMilestone(tx)` — derive tentative transitions (freeze, unfreeze/OnHold)
    from the accepted tx's consumed+produced delegation outputs; set
    `entry.pending`.
  - `Reconcile(rdr, currentSlot)` — settle/void previous-slot tentative
    transitions via point reads (§5.4.1). Driven off the slot edge.
  - `EnrollNewDelegation(wOut)` — drain a freshly-seen delegation from the
    new-delegation backlog into the pool as an `Undef` entry (§5.4.2).
  - `Snapshot(txEpoch, N, currentSlot)` — return the candidate entries +
    `D` over the reachable window (brief RLock, copy out).
- `sequencer/backlog/backlog.go` (or a sibling) — a new-delegation backlog:
  `ListenToControllerAccount` on the sequencer's **delegation-target** account,
  TTL eviction (wire the currently-unused `ttlDelegationSlots` from
  `BacklogTTLSlots()`, `backlog.go:296-297`) + the existing depth-based
  LRB-absence purge. Mirror `TagAlongBacklog`.
- `sequencer/sequencer.go` — add the `*DelegationPool` field; eager `Bootstrap`
  in `Start()` (after `LatestReliableState()` is available, before the first
  `doSequencerSlot`); start the new-delegation listener.
- `sequencer/strategy.go` `onMilestoneConfirmed` — call `pool.ApplyMilestone(vid
  tx)`; if `vid.IsBranchTransaction()`, kick an async `pool.Reconcile(...)`
  (never scan while holding the lock).
- `sequencer/task/proposal.go`
  - `selectDelegationsToFreeze()` — read from `pool.Snapshot(...)` instead of
    `IterateDelegatedOutputs`; split candidates into first-time vs continuation
    (§4a/§4b); credit in-pass assignments into the local `D`; fetch full output
    by ID for selected candidates; keep the `IsConsumedInThePastPath` filter.
  - `optimalFreezeEpoch()` — rewrite to amount-weighted `D` and restrict argmin
    to `[0, cap]` before selecting (drop the `min(epoch, maxPossible)` clamp).
- `sequencer/txbuilder_seq/txbuilder_seq.go` — `FreezeDelegation` unchanged; it
  already honors a passed `freezeUntilEpoch` in `[txEpoch, FreezeUntilMax]` and
  falls back to max otherwise (matches the continuation default).

The proposer reaches the pool via the sequencer environment; thread the
`*DelegationPool` (or accessor) into `taskData` the same way `SequencerID()` is.

## 10. Open questions

1. **Cache-scope sign-off.** §5.1 is a documented exception to
   `feedback_cache_and_refcount.md`: freeze-state event-authoritative, LRB used
   only for bootstrap + targeted reconciliation + the mandatory consume-time
   read, optimization-only. Confirm, or fall back to §5.5's minimal form.
2. **New-delegation backlog TTL.** Confirm the listener-fed backlog (§5.4.2) is
   the discovery mechanism (scan rejected) and pin the delegation TTL value
   (the slot the `ttlDelegationSlots` half of `BacklogTTLSlots()` should return).
3. **Full-output fetch at freeze time.** Confirm the mandatory by-OutputID
   point read (§5.5 step 4) — needed anyway for objective master-reclaim status,
   so storing parsed outputs in the pool buys nothing. Recommendation: fetch by
   ID, store slim entries.

Resolved (was an open question): **OnHold authorship** — the sequencer authors
the `→ OnHold` / unfreeze as its askStop response, so it is a tentative event
(§5.3), not external discovery; the master-authored reclaim during the safe
window is caught by the consume-time read (§5.5).

Hash-based alternative (assign phase = `hash(chainID) mod N`, re-anchored every
freeze — stateless, no pool) considered and rejected: amount-blind; a few large
delegations hashing to the same epoch reproduce the coverage cliff.

## 11. Test plan

- Unit (sequencer task): feed a synthetic pool snapshot and assert §4a picks
  max-index argmin within the cap; assert the clamp bug is gone (small-cap
  delegations do not pile on the boundary while lower epochs are empty).
- Continuation: a `Frozen`-state, window-elapsed delegation freezes to
  `FreezeUntilMax` and does not perturb `D`.
- Pool events: `ApplyMilestone` sets a `pending` freeze that counts in `D` at
  `untilEpoch` and removes the entry from candidates; a tentative unfreeze/OnHold
  removes it from `D`; `Snapshot` excludes `OnHold`, pending, and
  not-yet-freezable entries.
- Orphan reconciliation (the inside-slot race): a freeze applied by a milestone
  that does **not** confirm in the LRB is voided at the next slot edge
  (`Reconcile` point read finds `successorID` absent) and the delegation returns
  to freezable; a confirmed one is promoted to settled `Frozen`.
- Slot-accuracy: two freezes in the same slot (two milestones) land on distinct
  epochs (regression for the stale-baseline bug — now covered by the event path
  + pendingSubmit gating).
- New-delegation backlog: a delegation UTXO seen via the listener enrolls as
  `Undef` and becomes a candidate; an orphaned one is TTL-evicted and never
  freeze-attempted (or, if attempted, the consume-time read skips it).
- Bootstrap: the startup `IterateDelegatedOutputs` scan reconstructs the initial
  entries + `D`.
- Scale: with thousands of synthetic entries, a proposal does O(selected) state
  reads, not O(all) (assert no per-proposal `IterateDelegatedOutputs`).
- Local 3-node network (node0 boot / node1 2nd-seq / node2 access on the laptop,
  per `reference_local_multinode_deploy`): drive **several hundred delegations**
  and a mix of operations via `proxi node dlg` (create/delegate, askStop,
  withdraw/reclaim, vary `--epochs` caps) against the running sequencers. Then
  pull `delegateTarget` outputs per sequencer, parse each
  `delegateLockState.LastFrozenEpoch`, and confirm the unfreeze epochs spread
  across the window with the amount per epoch balanced (no cliffs) — at scale and
  under the orphan/race conditions a live multi-proposer sequencer produces.
- Testnet: same `delegateTarget` spread/balance check on the deployed sequencers.
```
