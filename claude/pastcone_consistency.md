# Past-Cone Consistency Analysis

*Task doc for the 2026-04-20 conflict-cascade investigation. Tracks the architecture, invariants, failure modes, and a concrete diagnostic plan for the past-cone / attacher subsystem.*

## 1. Problem statement

On 2026-04-20 the testnet (no load) entered a persistent BAD-conflict cascade on
seq1-acc where every newly-arriving milestone in slots 319835+ was rejected as:

```
ATTACH [319835|82sq]… (baseline [319821|0br]01a4a2015f97..)
  -> BAD(conflict [319819|64sq]00825c8f5961..[0] in the past cone: …)
```

The past-cone dump (`past_cone.go:497` `VertexLine`) shows the node's own
internal view at the moment of detection:

```
#2 S+ [319819|64sq]00825c8f5961..   consumers: {0: {[319820|69sq]00dda93aeb38..}}
#5 S- [319820|69sq]00dda93aeb38..   consumers: {}
```

`S+` = `FlagPastConeVertexInTheState`. `S-` = `FlagPastConeVertexCheckedInTheState` without `InTheState`.

Given those flags, `_checkVertex` (past_cone.go:1039) consults the baseline state reader:

```go
inTheState := pc.IsInTheState(vid)    // true for #2
...
if pc.IsInTheState(consumers[0]) { continue }   // false for [319820|69sq]
...
if inTheState && !stateReader.HasUTXO(wOut.DecodeID()) {
    return &wOut, false               // → BAD
}
```

So the node is saying: `[319819|64sq]` is committed in my baseline state, but
`[319819|64sq][0]` is **not an unspent UTXO** in that state — therefore some
other tx (not in the past cone) has consumed it.

There are three non-exclusive possibilities for why this reached BAD:

  1. **Real fork** — the sequencer that owns `[319819|64sq]` really did issue two
     successors; the baseline committed one, the past cone extends via the other.
  2. **Stale `InTheState` flag** — `#2` was tagged `S+` by an attacher whose
     baseline disagrees with the current baseline; the current baseline was
     never re-verified.
  3. **Stale consumer tracking** — `vid.consumed` at `#2` is missing the state's
     actual consumer (it was GC'd / the state consumer was detached) so the past
     cone's view of "who consumes this output" differs from what the state knows.

This document maps the subsystem, the invariants it *tries* to maintain, the
places those invariants can break, and a concrete diagnostic plan to tell the
three hypotheses apart on the testnet.

## 2. Architecture summary

### 2.1 Types

- **`PastConeBase`** (`past_cone.go:42`): baseline branch ID + vertex flag map +
  virtuallyConsumed. Serializable-style value: after an attacher goes Good, a
  `CloneImmutable()` is pinned to its `*WrappedTx` and shared read-only across
  future attachers.
- **`PastCone`** (`past_cone.go:32`): `PastConeBase` + tip + target ts + optional
  `delta *PastConeBase` for transactional writes (BeginDelta/CommitDelta).

### 2.2 Flags (`past_cone.go:50`)

| Flag | Semantics | Set by |
|---|---|---|
| `Known` | vertex is a member of the past cone | `MarkVertexKnown`, merge |
| `Defined` | validity fully checked | after endorsements+inputs solid |
| `CheckedInTheState` | state-membership query ran | `MarkVertexNotInTheState`, merge upgrade, `defineInTheStateStatus` |
| `InTheState` | vertex is committed in baseline's state | `defineInTheStateStatus`, `UpgradeToInTheState`, merge |
| `EndorsementsSolid` / `InputsSolid` | deps resolved | `attachEndorsements`, `attachInputs` |
| `AskedForPoke` | | transient |
| `DirectCost` | contributes to local attachment-cost budget | `MarkVertexNotInTheState` |

`SetFlagsUp` is monotonic except for `AskedForPoke`/`DirectCost`.  Once `InTheState`
is set it is never cleared.

### 2.3 Consumer lookup

`consumersByOutputIndex(vid)` (past_cone.go:566) is **read** from
`vid.consumed` at query time, then filtered to past-cone members via
`_filterConsumingVertices` (past_cone.go:534). So consumer tracking is not
cached in the PastCone itself — it is read live from the memdag. This is
important: consistency depends on `vid.consumed` being a complete view of who
consumes `vid`'s outputs in this attacher's world.

## 3. Invariants it tries to maintain

| # | Invariant | Where it is assumed |
|---|---|---|
| I1 | `IsInTheState(v)` ⇒ `v` is committed in `baseline`'s state | `_checkVertex` (past_cone.go:1054), `Mutations`, `CheckFinalPastCone` |
| I2 | `IsInTheState(v)` is monotonic for descendant baselines | `defineInTheStateStatus` (attacher.go:433), `MergePastCone` (past_cone.go:761) |
| I3 | `MergePastCone(pcb)` only succeeds if `pcb.baseline` and current baseline are on the same chain | `MergePastCone` via `IsDescendantBranch` |
| I4 | For every `v` in the past cone, `v.consumed` contains all state consumers of `v`'s outputs that are relevant to the past cone | `_checkVertex` second branch (`!stateReader.HasUTXO` only fires when past-cone consumer differs from state consumer) |
| I5 | A Good vertex either has a pinned `pastCone *PastConeBase` or `FlagVertexIgnoreAbsenceOfPastCone` | `GetTxStatusNoLock` asserts, attacher uses as safety net |
| I6 | `txidMayHaveExpired(baseline, txid)` ⇒ txid is committed in baseline's state | `defineInTheStateStatus` line 446, 457 |

I2 and I6 are the two "positive is monotonic, so don't re-check" contracts that
make the rest of the system cheap; they also concentrate most of the risk.

## 4. Where each invariant can break

### 4.1 I2 — "positive InTheState is monotonic"

Safe only when the attacher's baseline is always a descendant of every pcb
baseline it merged in. That is enforced at merge time by `IsDescendantBranch`,
which in turn calls `BranchKnowsTransaction` on the live `Branches` index.

**Breakage surface:**
- `Branches` is a node-local cache of committed branch data, populated by
  `forward_sync` and per-branch commits. It is not proven transactionally
  consistent with the attacher's read of `pc.baselineBranchID`.
- After `MergePastCone` succeeds, nothing re-validates the flag if the attacher
  later calls `setBaseline` via needsBaselineSwap inside the same
  `MergePastCone`. The swap assumes the new baseline is a descendant of the old
  one; but that check is re-done via `IsDescendantBranch` **at the time of
  merge** only.
- The attacher's baseline is set once at milestone construction
  (`attacher_milestone.go:293`) and later can only swap forward via merge.
  If the node's notion of "which branch is ancestor of which" later *changes*
  (e.g. a branch gets re-ranked during LRB promotion, or a slot is re-committed
  from sync), flags frozen into an immutable pcb may become stale **for readers
  of that pcb**. The pcb itself is immutable, so any attacher reading it
  re-applies its own `IsDescendantBranch` check, which is fine *if* `Branches`
  is internally consistent with the current baseline selection.
- **Cascade effect**: a pcb of vertex V is produced while V's attacher has
  baseline A with flags `{x: InTheState}`. Later, V is merged into an attacher
  at baseline B which is a descendant of A. The flag is copied. `defineInTheStateStatus`
  of that attacher may also encounter `x` via another path, sees `InTheState`
  already set, returns early without re-checking against B.  If `Branches`'
  view of "A is ancestor of B" was wrong at the time `V`'s attacher ran, the
  pcb is already poisoned; once in it, no later check catches it.

### 4.2 I6 — TxID TTL assumption

`txidMayHaveExpired(baseline, txid)` returns true if
`baselineSlot - txSlot > TxIDStateTTLSlots` (default 8640 ≈ 24 h).
In that case `defineInTheStateStatus` (attacher.go:457) and
`branches.SnapshotKnowsTransaction` (branches.go:708) treat the txid as
*definitely committed* — without consulting the state trie at all.

**Correct framing of the invariant.** Every committed tx is provably included
in the state of every descendant branch: the state trie (a Merkle tree) holds
a txid record for each committed transaction, so inclusion can be verified
cheaply by a single trie read (Merkle membership). The purpose of the txid
record is exactly to enable this fast proof without needing to re-load the
transaction itself. The TTL (`TxIDStateTTLSlots`) is a **space optimisation**:
after that window, the txid record is pruned so the trie does not grow
without bound. Beyond TTL, the tx is *still* provably committed — but the
proof now requires walking the actual past cone of txs behind the pruning
point, which is orders of magnitude more expensive than a trie read.

**Which references to a pruned txid are actually risky?**

- **Consuming a UTXO of a pruned tx** → not a concern. The UTXO itself must
  exist and be unspent in the current state; its presence in the state trie
  is a cryptographic witness that whatever tx produced it was committed. The
  txid record being pruned is orthogonal.
- **Replaying a pruned tx** → not a concern. For the replay to attach, its
  inputs must be unspent in the current state; but those inputs were
  consumed when the tx was first committed, so they are definitively absent
  from the current state. UTXO conservation rules out replay
  cryptographically, independently of the txid record.
- **Endorsing a pruned tx** → not a concern. Endorsements are intra-slot
  only, so they cannot reach back into pruned-age territory.
- **Naming a pruned branch tx as an explicit baseline** → this is the real
  risk. A sequencer/branch tx can carry an explicit baseline reference. If
  that branch's txid has been pruned, the node's cheap check
  (`BranchKnowsTransaction` via the state trie) returns false, and the
  current code silently blesses the reference via `txidMayHaveExpired` — no
  proof required. A malicious or buggy actor can name any arbitrary old
  branch ID (real or fabricated past the TTL horizon) as the baseline; the
  past-cone logic then inherits whatever flags that phantom baseline
  implies.

**Fix direction.** Narrow the problem to the one real surface: explicit
baseline naming. Retain branch-txid records indefinitely (or much longer
than TTL) for branches on the canonical chain — branch txs are one per slot,
so the space cost is O(slots since genesis) × ~32 B, negligible compared to
the output and account state that dominates trie size. Everything else can
be pruned as today. With branch txids permanent, `BranchKnowsTransaction` on
a legitimate branch will always hit the trie; the only way it returns false
is for a fabricated or truly non-existent branch, and the attacher can then
reject cleanly instead of blessing the phantom.

### 4.3 I4 — `vid.consumed` completeness

`_checkVertex` conflict detection depends on
`consumers := pc.consumersByOutputIndex(vid)` returning all past-cone
consumers of `vid`'s outputs. That set comes from `vid.consumed` under
`vid.mutexDescendants.RLock()`.

**Breakage surface:**
- Consumers may be GC'd from `vid.consumed` via weak-ref collection or
  `ConvertToDetached`. If a consumer was in the past cone but its `*WrappedTx`
  was GC'd between the time the past cone was built and the time
  `CheckConflicts` runs, `_filterConsumingVertices` drops it silently, and the
  conflict-checker sees an incomplete consumer set. Whether that hides a
  conflict or reveals a false positive depends on which side is dropped; in
  either direction the result is nondeterministic.
- The baseline branch is added as a consumer synthetically
  (`_filterConsumingVertices` past_cone.go:545) because branches consume their
  predecessor's stem. Prior fix `dc4af830` reminds that this synthetic
  consumer is easy to lose track of.
- Merged pcb vertices bring along `*WrappedTx` pointers that were alive at
  merge time; over the lifetime of the attacher those pointers can be
  invalidated (detached / reattached) while still present in `pc.vertices`.

### 4.4 Conflict-detection logic itself

`_checkVertex` only fires the BAD branch on:
- `len(consumers) != 1` — past cone has multiple consumers of the same output, or
- `inTheState && !HasUTXO` — vertex is flagged S+ and its output is consumed in state but the past-cone consumer is not in state.

The second branch is precisely what the seq1-acc log shows. But it does not
distinguish between the three hypotheses listed in §1:
- real fork → `HasUTXO` returns false because the state's consumer is some
  third tx X, correctly.
- stale S+ flag → vertex is actually **not** in state; `HasUTXO` returns
  false because the output was never in state. False positive.
- stale consumer tracking → state's consumer is actually the same
  `[319820|69sq]` from the past cone, but was missed by the filter.

None of those three hypotheses is logged; the operator sees a flat "BAD conflict".

## 5. Failure-mode catalog

### 5.1 Cross-branch endorsement pollution

A tx T attaches with baseline A (a committed branch); its pcb flags T's
endorsement targets as S+ or S- against A. T is endorsed by later tx U whose
attacher has baseline B on a competing fork. `MergePastCone` rejects because
A and B are not same-lineage → U becomes BAD. This is the *correct* outcome
and is visible as "conflicting baselines … and …" in the attacher error log.
No inconsistency.

### 5.2 Baseline swap forward

pcb baseline A, current baseline B. A is ancestor of B. Merge keeps baseline
= B. pcb's S+ flags for vertices `v in state(A)` are trivially still valid
(state(A) ⊆ state(B)). pcb's S- flags for vertices **not in state(A)** may be
wrong for B; `MergePastCone` at past_cone.go:761-766 handles the upgrade and
lifts them to S+ when `BranchKnowsTransaction(B, v.id)` is true. **Safe.**

### 5.3 pcb's S+ flag carried through a stale Branches index

Attacher R produces a pcb for V with baseline A, flags `{x: S+}`. The
`IsInTheState(x)` decision inside R consulted
`Branches.BranchKnowsTransaction(A, x.id)`, which returned true **based on the
state of the `Branches` index at that time**.

Later, a concurrent branch commit re-indexes branches. If `Branches` has any
case where a query returns different answers at different times for the same
pair (it shouldn't — `knowsTxCache` is persistent and both the trie and the
pending layer are append-only), nothing protects future readers of V's pcb.
**Not obviously unsafe; depends on `branchKnowsTransactionCompute`
determinism**, which needs verification.

### 5.4 GC-stranded consumer

State has [319819|64sq][0] consumed by X (some sequencer tx). X was in memdag
but got detached via `ConvertToDetached` (happens on branch vertex or memory
pressure GC). Past cone of new milestone N built later — `vid.consumed` at
[319819|64sq] still records X, but X is a DetachedVertex. `_filterConsumingVertices`
keeps it iff `pc.IsKnown(X)`. If N never traversed via X's subtree, X isn't in
the pc, so it's filtered out. The pc then only has [319820|69sq] as consumer,
and HasUTXO returns false for the state output → conflict fires. **This is
likely the subtle case seq1-acc hit.** The conflict is "real" from N's past
cone's perspective (the state has a consumer it can't see), but it is not a
double-spend — it's a GC-induced visibility gap.

### 5.5 `txidMayHaveExpired` mis-blessing via explicit baseline

A tx names branch `B_old` as its explicit baseline. `B_old.Slot()` is past
the TTL window relative to the current baseline (either legitimately old, or
fabricated to sit past the TTL horizon). `BranchKnowsTransaction` on the
state trie returns false because the branch's txid record has been pruned.
`defineInTheStateStatus` / `SnapshotKnowsTransaction` then falls through to
`txidMayHaveExpired` and silently flags `B_old` as in-state. From that point
the attacher operates with a phantom baseline whose state is never queried
— coverage, InTheState flags, conflict checks all derive from a premise
that was never validated. Consuming UTXOs, endorsements, and replay cannot
exploit this path (see §4.2); explicit-baseline naming is the only reachable
surface.

## 6. Diagnostics plan

The goal is to catch §4 / §5 cases on the testnet **without perturbing
correctness**. All instrumentation is:
- guarded by a trace tag / config switch (off by default),
- performed at exactly the moment a BAD(conflict …) is about to be returned,
- emits a single structured log line per event; no spam.

### 6.1 At-conflict cross-check

In `_checkVertex`, when we are about to return `(&wOut, false)` for the
`inTheState && !HasUTXO` case, call an additional diagnostic hook that:

1. Looks up `Branches().BranchKnowsTransaction(baseline, vid.id)`. If **false**,
   the S+ flag is lying — this is a stale-flag bug. Log
   `[past_cone_diag] STALE S+ flag: baseline=%s vid=%s knowsTx=false`.
2. If true, ask the baseline state reader for the actual consumer of
   `wOut.DecodeID()`:  walk `rdr.KnowsCommittedTransaction` + look at consumed-outputs index if available, or call the existing `multistate` API for a given UTXO's metadata. Produces either "unspent" (contradicts !HasUTXO → bug), a single consumer txid, or "not indexed".
3. Cross-check that consumer txid against the past-cone consumers (`consumers[0]`). If equal, `vid.consumed` is missing a pointer that the state has → this is §5.4 (GC-stranded consumer) or §4.3 (completeness). Log
   `[past_cone_diag] STATE_CONSUMER match pc_consumer=%s: consumer tracking gap`.
4. If different, this is either §5.1 or a real fork. Log
   `[past_cone_diag] REAL conflict: state_consumer=%s pc_consumer=%s`.
5. Dump the past-cone flags for `vid` and the immediate ancestor of the
   past-cone consumer (so we can see how the consumer got into the past cone).

Nothing on this hook changes the return value — `_checkVertex` still returns
`(&wOut, false)` and the node still rejects the milestone. We just annotate
the event.

### 6.2 At-merge cross-check

In `MergePastCone`, when `IsDescendantBranch` returns `compatible=true`, walk
pcb's S+ vertices and (for a random sample, to bound cost) verify
`Branches.BranchKnowsTransaction(pc.baseline, vid.id)` returns true. If any
disagree, log
`[past_cone_diag] MERGE inconsistent S+: pcb.baseline=%s pc.baseline=%s vid=%s`.
This catches §5.3.

### 6.3 TxID-TTL shadow log

In `defineInTheStateStatus` (both call sites lines 446 and 457), when we rely
on `txidMayHaveExpired` to return "in-the-state" without a real check, emit
a trace-level log `[past_cone_diag] TTL bless: baseline=%s slot=%d txid=%s`.
Not an error, but lets us see on testnet how often this path fires and
whether it correlates with conflicts.

### 6.4 GC visibility hook

At the moment `ConvertToDetached` actually nil's `vid.pastCone` (vid.go:119),
log `[past_cone_diag] DETACH: vid=%s had pastCone size=%d` when size > 0.
Cross-referenced with conflict events this isolates §5.4.

### 6.5 Runtime switch

All five hooks gated by the single trace tag `past_cone_diag`. Enabled via the
existing `Tracef` mechanism; runnable on testnet without rebuild.

## 7. Proposed follow-up work (not in this patch)

The flag cache exists for performance; any fix must preserve O(1) lookups on
the conflict-check hot path. The proposals below add invariants at
boundaries, not in loops.

1. **Retain branch txids permanently; drop the TTL-bless shortcut.**
   Per §4.2, the only reachable unsafe reference is explicit-baseline
   naming. Keep branch-txid records on the canonical chain out of pruning
   scope and remove `txidMayHaveExpired` from `defineInTheStateStatus` /
   `SnapshotKnowsTransaction`. Non-branch txids continue to be pruned; no
   other reference type was relying on the shortcut. Hot-path cost
   unchanged (still a trie read).
2. **Tie consumer tracking to the past cone, not the global memdag**, for
   the subset of consumers that matter for conflict detection. Detached /
   GC'd consumers should not silently drop from the conflict checker's
   view (§4.3, §5.4). Cost: one extra set copy at past-cone build time,
   not per conflict check.
3. **Tighten the flag-propagation boundary**, not the hot path.
   `MergePastCone` already checks `IsDescendantBranch` once. Strengthen it
   so the merge either produces a past cone with invariant-valid flags or
   rejects, and never silently ORs a suspect flag in. Concretely: the
   merge should fail (not degrade to "provisional not-in-state") whenever
   it cannot determine the same-lineage relationship deterministically.
   The conflict loop keeps trusting flags as today; the trust gets
   established at the single merge boundary.
4. **Introduce a "pastCone digest" pinned per Good vertex** that records
   the `(baseline, flag-set)` the pcb was produced under. On merge, compare
   digests rather than walking all flags. Hot-path cost unchanged.

Principle: **verify at construction/merge, trust on read**. The current bug
surface is that merges can admit flags whose construction-time invariant is
unverified (phantom baseline via TTL bless, non-deterministic
BranchKnowsTransaction in §5.3); fixing those inputs keeps the
flag-based fast path intact.

## 8. Scope of this patch

This patch only adds the §6 diagnostics behind trace-tag `past_cone_diag`.
No behaviour change. Deployable on testnet to narrow the cascade next time it
reproduces.
