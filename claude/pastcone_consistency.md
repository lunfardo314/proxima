# Past-Cone Consistency Analysis

*Task doc for the 2026-04-20 conflict-cascade investigation. Tracks the
architecture, invariants, failure modes confirmed against the live DB, and
a concrete diagnostic plan for the past-cone / attacher subsystem.*

## 1. Problem statement

On 2026-04-20 the testnet (no load) entered a persistent BAD-conflict cascade.
Two distinct captures were analysed; both show the same class of symptom.

**Capture 1** (earlier session, seq1-acc, baseline `[319821|0br]01a4a2015f97..`):
```
ATTACH [319835|82sq]… -> BAD(conflict [319819|64sq]00825c8f5961..[0] in the past cone)
  #2 S+ [319819|64sq]..  consumers: {0: {[319820|69sq]..}}
  #5 S- [319820|69sq]..  consumers: {}
```

**Capture 2** (later, loc1, baseline `[320755|0br]019dda9fd6db..`) — used as
the anchor from here on because it is verified end-to-end against the DB:
```
ATTACH [320755|79sq]005dbd2364db.. -> BAD(conflict [320753|80sq]003cd1012296..[0] in the past cone)
  #3 S+ [320753|80sq]..  consumers: {0: {[320754|79sq]0086a8b3dfae..}}
  #7 S- [320754|79sq]..  consumers: {0: [320754|88sq]..}
```

`S+` = `FlagPastConeVertexInTheState`. `S-` =
`FlagPastConeVertexCheckedInTheState` without `InTheState`. `_checkVertex`
(past_cone.go:1039) consults the state reader:

```go
inTheState := pc.IsInTheState(vid)              // true for #3
if pc.IsInTheState(consumers[0]) { continue }   // false — consumer marked S-
if inTheState && !stateReader.HasUTXO(wOut.DecodeID()) {
    return &wOut, false                          // → BAD
}
```

### 1.1 Root cause — confirmed against the DB

Using `proxi db txstore get -p` and `proxi db findtx -b <baseline>` on loc0's
node (testnet was stopped; DB retained), the facts of Capture 2 are:

| Tx | Input #0 | Endorsement | In state of `[320755|0br]019dda9fd6db..`? |
|---|---|---|---|
| `[320753|80sq]003cd1012296` | `[320752|12sq][0]` | `[320753|12sq]005989a9843d` | **Yes** |
| `[320753|86sq]000803be6894` | `[320752|12sq][0]` | `[320753|74sq]0040dbe56bb4` | No |
| `[320754|79sq]0086a8b3dfae` | `[320753|80sq][0]` | `[320754|12sq]00df1132e927` | **Yes** |
| `[320754|0br]017828b5dfc1` (won) | `[320753|91sq][0]`, stem | — | — |
| `[320754|0br]01f576b83261` (orphaned) | `[320753|86sq][0]`, stem | — | No |

The conflict reported on loc1 is a **false positive**. State of baseline
`[320755|0br]019dda9fd6db..` contains *both* `[320753|80sq]` *and* its
successor `[320754|79sq]`; `[320754|79sq]` does legitimately consume
`[320753|80sq][0]`. The past cone's consumer and the state's consumer are
the same transaction. No double-spend.

The bug is entirely local to the past-cone flag cache on loc1:
`[320754|79sq]` is present in the past cone with its S- flag set (stale)
and was never upgraded when the baseline it is being checked against moved
to `[320755|0br]019dda9fd6db..`. `_checkVertex` walks into the
`inTheState && !HasUTXO` branch because `pc.IsInTheState([320754|79sq])`
returns false, and issues a phantom conflict. Actual `[320754|79sq]` is in
state; `BranchKnowsTransaction([320755|0br]019dda9fd6db.., [320754|79sq])`
returns true (verified via DB).

### 1.2 Separate observation — two siblings on loc0's chain

`[320753|80sq]` and `[320753|86sq]` are both signed by loc0 and both consume
`[320752|12sq][0]`. They are siblings, not a parent and child. Per the
ledger this is *allowed*: a sequencer may produce multiple conflicting seq
milestones inside a slot and the coverage rule resolves which one wins.
`[320753|80sq]` won (went to state); `[320753|86sq]` was extended only by
loc0's own branch `[320754|0br]01f576b83261` which itself lost the branch
race — whole subtree orphaned. Not a ledger violation, not the cause of
the cascade. Mentioned here only to document that the earlier framing of
this as "equivocation" was incorrect.

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

### 4.1 I2 — positive monotonic / negative may be stale

The flag state-machine `defineInTheStateStatus` (attacher.go:433) encodes:

- **Positive (S+) is monotonic across descendant baselines** — `v in state(A)`
  and A is an ancestor of B ⇒ `v in state(B)`. Once S+ is set, no re-check.
- **Negative (S-) may be stale** — `v not in state(A)` does NOT imply
  `v not in state(B)`. A downstream attacher at baseline B must re-check a
  vertex carrying an S- flag from a pcb produced at baseline A, and upgrade
  to S+ via `UpgradeToInTheState` when `BranchKnowsTransaction(B, v.id)` is
  true.

Both directions exist as failure modes.

#### 4.1.a Negative-direction staleness — **confirmed by DB on 2026-04-20**

A consumer vertex carries an S- flag that is never upgraded when the attacher's
baseline includes the vertex. `_checkVertex` then reads `pc.IsInTheState(consumers[0])
== false`, falls through past the early-`continue`, hits `inTheState && !HasUTXO`
and returns BAD — a phantom conflict.

Confirmed instance: `[320754|79sq]0086a8b3dfae` on loc1 at 10:48:44, baseline
`[320755|0br]019dda9fd6db..`. Past cone dump showed S-, DB confirms the vertex
is committed in the baseline's state. See §1.1.

Upgrade is attempted in two places:

- `MergePastCone` at past_cone.go:761-766 — when a pcb is merged in, any S-
  flag in pcb is upgraded to S+ if the current baseline knows the tx.
- `defineInTheStateStatus` attacher.go:442-448 — when a vertex is
  (re)visited during solidification and CheckedInTheState is already set,
  re-ask `BranchKnowsTransaction`; upgrade to S+ on positive.

Both upgrade paths require the attacher to actually *visit* the vertex
during its current run. If the vertex came in via a pcb that was merged
successfully, and after that merge the attacher never enters
`defineInTheStateStatus` for that vertex (e.g., because `attachVertexNonBranch`
took the short path "status == Good, merge, done" at attacher.go:151-162),
the stale S- persists until `_checkVertex` reads it. That is exactly the
code path traversed by the loc1 case: the consumer vertex was in a Good
dependency's merged pcb, never directly visited, and never re-checked.

#### 4.1.b Positive-direction staleness

A pcb of vertex V is produced while V's attacher has baseline A with flags
`{x: InTheState}`. Later, V is merged into an attacher at baseline B, and
`IsDescendantBranch` approves the merge. The S+ flag on x is copied. If
`Branches`' view of "A is ancestor of B" was wrong at the time V's attacher
ran (stale index, racy commit, etc.), the pcb is already poisoned; future
readers re-apply `IsDescendantBranch` against the live index, which is fine
*only if* the index is internally consistent.

Not observed in the 2026-04-20 data. Lower-priority to investigate than 4.1.a
because the live `Branches` index is append-only and derived from the trie,
so the necessary inconsistency is speculative.

#### 4.1.c Why this passes the existing upgrade path in `MergePastCone`

`MergePastCone` walks `pcb.vertices` and upgrades negatives when the current
baseline knows the tx. But there is an asymmetry: the merge handles flags on
vertices present *in the pcb being merged*. It does not rescan vertices
already in `pc.vertices` that were marked S- earlier. If a consumer vertex
arrived via a different path (earlier merge, direct attach) and was marked
S- then, it stays S- regardless of subsequent merges. The loc1 case fits this
shape: the consumer `[320754|79sq]` was in the pc as S- at some earlier
moment and never re-examined when boot's winning branch's pcb was merged.

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

In `_checkVertex`, when about to return `(&wOut, false)` for the
`inTheState && !HasUTXO` case, the diagnostic hook calls
`BranchKnowsTransaction` on **both** `vid` and `consumers[0]` against the
current baseline, and classifies:

1. `vid` knowsTx=**false** → the S+ flag on vid is lying (positive-staleness
   §4.1.b). Log
   `[past_cone_diag] STALE S+ on vid: baseline=%s vid=%s`.
2. `consumers[0]` knowsTx=**true** → consumer is actually in state but the
   past cone marks it S- (negative-staleness §4.1.a, **confirmed class**).
   Log
   `[past_cone_diag] STALE S- on consumer: baseline=%s vid=%s consumer=%s`.
3. Both knowsTx=true / vid=true, consumer=false → a genuine fork (state
   holds a different consumer from the past cone's). Log
   `[past_cone_diag] REAL conflict: baseline=%s vid=%s pcConsumer=%s`.
4. Both knowsTx=false → inconsistent: the S+ flag on vid was already a lie,
   so `_checkVertex` shouldn't even be in this branch. Log as a bug:
   `[past_cone_diag] INCONSISTENT: vid S+ but branchKnowsTx=false`.

Case (2) is the one the 2026-04-20 cascade maps to. Current implementation
only checks `vid`; extending to also check the consumer surfaces the
confirmed class immediately.

Nothing changes the return value — `_checkVertex` still returns BAD and the
attacher still rejects. The hook annotates only, so that when case (2) shows
up we know the conflict is a phantom caused by stale flag and the attach
should have succeeded.

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

## 8. Status (2026-04-20) and guiding principle

### 8.1 Current state

- §6 diagnostics are in (trace-tag `past_cone_diag`) — behaviour unchanged
  when the tag is off; on testnet they already surfaced the confirmed
  negative-staleness case on `[320754|79sq]` (see §1.1, §4.1.a).
- §6.1 is extended to also check the consumer direction (§4.1.a) — the class
  that matches the observed cascade.
- Next fix: make the milestone attacher re-check an S- consumer flag against
  the current baseline before `_checkVertex` walks into the BAD branch.
  Minimum change: when `_checkVertex` sees `pc.IsInTheState(consumers[0])
  == false`, call `Branches.BranchKnowsTransaction` and `UpgradeToInTheState`
  if positive before returning BAD. Confines the re-check to the rare path
  and keeps the hot loop flag-cached.

### 8.2 The two attachers, and where the bug lives

- **Incremental attacher** (`attacher_incremental.go`): runs inside the
  sequencer during milestone *construction*. Builds the past cone step by
  step as proposers accumulate candidates, and checks conflicts incrementally
  so the tx about to be signed is conflict-free against its own checker.
- **Milestone attacher** (`attacher_milestone.go`): runs on every node when
  a fully-formed sequencer milestone arrives from gossip. Independently
  solidifies the past cone from that node's memDAG and re-checks conflicts
  against its own baseline.

In the 2026-04-20 case the incremental attacher on loc0 did the right
thing — the tx it produced had a valid, conflict-free past cone. The false
positive was raised by the milestone attacher on loc1 because *its* local
past-cone flag cache had a stale S- on `[320754|79sq]`. The bug is entirely
on the downstream side.

### 8.3 Guiding principle — determinism of the conclusion, not of the path

Solidification and attachment are inherently non-deterministic across nodes:
the order in which dependencies resolve, which merges happen first, when GC
kicks in, how many attachers race on overlapping past cones — none of that
is reproducible node-to-node, nor should it be. What must be deterministic
is the **binary answer**: for a given past cone, every correct attacher,
regardless of how it arrived there, must agree on whether the past cone
contains a conflict.

Today that invariant is violated by §4.1: two attachers reading the same
underlying DAG and the same baseline can disagree because one of them holds
a stale S- flag on a consumer and the other does not. Every fix direction
in §7 serves this one invariant. The diagnostics in §6 exist to catch the
cases where the answer diverges from the truth the state trie already
knows.

### 8.4 Ground rule for DAG analysis

Log lines describe what the node *logged*; they do not describe what the
DAG actually *is*. Draw DAG topology conclusions from `proxi db txstore get
-p` or the `/api/tx_detail` / `/api/past_cone` endpoints exposed by
`proxi db txstore dagviz`, not from log reconstructions. The earlier
version of this doc — and the now-removed `nothing-at-stake.md` — reached a
wrong "equivocation" conclusion by inferring successor relationships from
submit-order timestamps; the actual inputs contradicted the story once read
from the DB.
