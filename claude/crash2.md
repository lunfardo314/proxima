# Crash Analysis: Token Conservation Invariant Violation & Conflict Detection

## Session Context

Branch: `develop07-seq-improvement` (Phase 2: event-driven sequencer with plateau detection)

Date: 2025-04-09

After deploying Phase 2 changes, the testnet crashed with all 8 nodes hitting the same fatal assertion during branch commit. A separate conflict detection issue was also observed on access nodes.

---

## Issue 1: Token Conservation Crash (All Nodes)

### Symptom

All 8 nodes crashed simultaneously on the same branch:

```
FATAL: _commitPendingBranchUnlocked([230327|0br]01c8cc156afc..)
  baseline=[230326|0br]0172651c14fe..
  -> updateTrie: major inconsistency.
  Mismatch input amount(1_005_023_327_296_036) + inflation(84_912_659)
  != output amount(1_006_696_011_243_129).
  Diff: 1_672_599_034_434
```

### Mutation Dump

The branch commit delta between baseline `[230326|0br]0172651c14fe..` (boot's slot 326 branch) and new branch `[230327|0br]01c8cc156afc..`:

```
DEL   [230326|0br]0172651c14fe..[0]
DEL   [230326|0br]0172651c14fe..[1]
ADD   [230326|15sq]00596b984fdf..[0] (1_672_621_119_947, inflation 94_278)
ADD   [230326|44sq]003fa5a31213..[0] (1_568_309_779_832, inflation 90_873)
ADD   [230326|47sq]00832487ed50..[0] (1_637_513_048_140, inflation 93_129)
ADD   [230327|0br]01c8cc156afc..[0] (1_001_817_567_295_210, inflation 62_643_144)
ADD   [230327|0br]01c8cc156afc..[1] (0, inflation 0)
ADDTX [230326|15sq]00596b984fdf.. : unspent [0]
ADDTX [230326|44sq]003fa5a31213.. : unspent [0]
ADDTX [230326|47sq]00832487ed50.. : unspent [0]
ADDTX [230326|56sq]00986ab96f2c.. : unspent []
ADDTX [230327|0br]01c8cc156afc.. : unspent [0 1]
```

### Key Observations

1. **Only 2 DELs**: Both from boot's baseline branch outputs (chain + stem). No DELs from any other source.

2. **3 other-sequencer txs as ADDs**: `[230326|15sq]`, `[230326|44sq]`, `[230326|47sq]` are from other sequencers. Their output [0] (chain output) is unspent. These txs consume their predecessor chain outputs, but those consumed outputs are NOT in the DEL list.

3. **Missing branches**: The other sequencers' slot 326 branches (which are the predecessors of those 3 txs) do NOT appear in the ADDTX list at all. They should be either:
   - In the state (InTheState) → their consumed outputs should be DELs
   - In the delta (not in state) → they should be ADDTXs

   They are **neither** — they're absent from the mutation set entirely.

4. **Boot's seq tx**: `[230326|56sq]` has `unspent []` (all outputs consumed by the branch). This is boot's own seq tx that endorsed 2 other sequencer txs (`endorse: 2` in log).

5. **Excess matches one tx's output**: The diff 1,672,599,034,434 is close to `[230326|15sq]`'s output (1,672,621,119,947). The difference (22,085,513) roughly corresponds to inflation accumulated along the chain.

### Timeline

```
19:35:20.326  SUBMIT BRANCH [230326|0br]0172651c14fe.. (boot's baseline branch)
19:35:24.584  BRANCH COMMIT [230326|0br]015dc8d51941.. 'loc1' (6 seq + 0 non-seq)
19:35:25.677  BRANCH COMMIT [230326|0br]0172651c14fe.. 'boot' (4 seq + 0 non-seq)
19:35:26.485  SUBMIT SEQ TX [230326|56sq]00986ab96f2c.. endorse: 2
19:35:30.497  [memdag GC] detached: 1, deleted: 0
19:35:30.568  SUBMIT BRANCH [230327|0br]01c8cc156afc..
19:35:33.050  purged 1 own milestones
19:35:34.383  FATAL assertion
```

### Root Cause Analysis (Revised)

**Previous analysis (incorrect)** blamed the branch short-circuit in `attachOutput` (lines 607-611). That analysis assumed the endorsement chain was `[230326|47sq] → [230326|44sq] → [230326|15sq]` extending through loc1's branch. The dagviz shows a different structure, and the branch short-circuit is correct by design — branches are committed state boundaries that should not be traversed behind.

#### Corrected DAG structure (from dagviz)

```
[230327|0br] (boot's new branch, baseline: [230326|0br]boot)
  extends [230326|56sq] (boot's seq tx, endorse: 2)
    endorses [230326|47sq]
      extends [230325|47sq] (cross-slot predecessor, slot 325)
      endorses [230326|15sq] (HAS EXPLICIT BASELINE)
    endorses [230326|44sq] (another sequencer's chain)
```

Key fact: `[230326|15sq]` has an **explicit baseline** (boot proposal pattern). Since `[230326|47sq]` is cross-slot non-branch, its `BaselineDirection` = first endorsement = `[230326|15sq]`. So `[230326|47sq]`'s baseline is derived from the explicit baseline of `[230326|15sq]` — a slot 325 branch.

#### The actual bug: nil PastConeBase + premature Defined marking

The bug is in `attachVertexNonBranch` (attacher.go, the GOOD+nil handler at lines 150-178).

When `[230327|0br]`'s attacher processes the endorsement `[230326|47sq]`:

1. `attachEndorsementDependency([230326|47sq])` → `refreshDependencyStatus` → `defineInTheStateStatus`
   - BKT([230326|0br]boot, [230326|47sq]) → 326 ≤ 326 → false → NOT InTheState
2. `attachVertexNonBranch([230326|47sq])` → GOOD → `vid.GetPastConeNoLock()` → **nil** (GC'd)
3. Nil PastConeBase handler (lines 162-176): baseline compatibility check passes
4. `ok = true; defined = true` → **[230326|47sq] marked Defined**

The problem: [230326|47sq]'s PastConeBase was nil (lost to GC — `ConvertToDetached` sets `vid.pastCone = nil`). The nil handler marks the vertex as **Defined** without its subtree. Since it's Defined, `IsKnownDefined` at line 121 returns true on any future encounter — [230326|47sq]'s inputs (including `[230325|47sq]`) are **never processed**.

Result: `[230325|47sq]` and its entire subtree are **absent from `pc.vertices`**. No DEL mutations are generated for their consumed outputs. Token conservation fails.

#### Why [230325|47sq]'s PastConeBase is nil

`[230325|47sq]` is from slot 325 (the previous slot). The timeline shows GC at 19:35:30, right before `[230327|0br]`'s submission. Branch vertices are detached immediately after commit (`ConvertToDetached` at `attacher_milestone.go:159`), but non-branch GOOD vertices are also detached by memdag GC. On the second `ConvertToDetached` call, `vid.pastCone = nil` (line 110). Since `[230325|47sq]` is old enough to be GC'd across all nodes, the crash is deterministic — all 8 nodes have the same incomplete past cone.

Actually, more precisely: it is `[230326|47sq]`'s PastConeBase that is nil, not `[230325|47sq]`'s. `[230326|47sq]` was GC'd, so when the attacher encounters it as GOOD with nil PastConeBase, the merge is skipped and `[230325|47sq]` never enters the past cone.

#### The explicit baseline connection

The explicit baseline in `[230326|15sq]` is relevant in two ways:

1. **It determines `[230326|47sq]`'s baseline** via `BaselineDirection` → first endorsement. This creates a cross-slot dependency structure where `[230325|47sq]` enters through `[230326|47sq]` — a vertex whose PastConeBase may be nil.

2. **Baseline incompatibility masking**: If `[230326|47sq]`'s PastConeBase were non-nil, the merge of `[230325|47sq]`'s PastConeBase might fail due to incompatible baselines (the explicit baseline's slot 325 branch vs `[230325|47sq]`'s own slot 325 branch, if different). The nil PastConeBase silently skips this check — the merge is never attempted.

#### Relationship to Issue 2 fix

The Issue 2 fix (re-checking stale not-in-state flags in `defineInTheStateStatus`) is necessary but not sufficient:
- If `[230325|47sq]` WERE in the past cone, the re-check against boot's baseline would correctly upgrade it to InTheState (BKT would find it in boot's trie). As InTheState, no PastConeBase needed — state boundary.
- But `[230325|47sq]` never enters the past cone because `[230326|47sq]` is marked Defined with nil PastConeBase. The Issue 2 fix can't help vertices that are absent from `pc.vertices`.

#### Why this wasn't triggered before Phase 2

Phase 2's plateau detection accumulates more endorsements per milestone. Boot's `[230326|56sq]` had `endorse: 2`, pulling in cross-slot dependency chains. Combined with the explicit baseline (boot proposal pattern, which fires more often in Phase 2 due to longer pauses), this creates past cones that span multiple sequencer chains with cross-slot dependencies — exactly the topology that exposes the nil-PastConeBase bug.

### Proposed Fix: In-Place Reattachment

#### Mechanism clarification: DetachedVertex vs nil PastConeBase

When `ConvertToDetached()` runs, it atomically changes the vertex type from `_vertex` to `_detachedVertex` AND sets `pastCone = nil` — both under the same write lock (vid.go:98-106). A GC'd non-branch vertex is always `_detachedVertex`, never `_vertex` with nil PastConeBase.

In `attachVertexNonBranch`, Unwrap dispatches by **type**, not status. A `_detachedVertex` hits the `DetachedVertex` handler (line 187-189), which calls `GracefulShutdown` — it **never reaches** the `Vertex` → `Good` → nil PastConeBase handler at line 150-176. The only production path creating `_vertex` + Good + nil PastConeBase is `SetTxStatusGood(nil, 0)` in the snapshot paths (attach.go:91,99,147), which doesn't apply to recent-slot transactions.

Therefore the fix must address the `DetachedVertex` handler path.

#### Root cause summary

The fundamental problem is that memDAG GC (ConvertToDetached) destroys information that active attachers may later need. A detached vertex is GOOD (was fully solidified once), but its `_detachedVertex` type + nil PastConeBase makes it unusable for past cone traversal. The transaction data (`*transaction.Transaction`) inside the DetachedVertex is **immutable** — this is the key fact that enables safe reattachment.

Patching the detached DAG with special-case logic (mutation reconstruction, flag workarounds) leads to increasing complexity and heterogeneous code paths. The solid approach: treat a DetachedVertex as a vertex that needs re-solidification, using the immutable transaction data that is already available.

#### Design: in-place reattachment via `AttachTransaction`

**Core idea**: When an attacher encounters a DetachedVertex, it triggers `go AttachTransaction(v.Transaction, env)` which re-solidifies the vertex in-place on the same `*WrappedTx`. This preserves pointer identity — no aliasing in PastCone maps, `consumed` maps, or `Vertex.Inputs` references.

**RUnwrap safety**: The existing RWMutex on `*WrappedTx` guarantees safety. `Unwrap` (write lock) for the in-place type change is mutually exclusive with `RUnwrap` (read lock) readers. After the write lock releases, all readers see the new `_vertex` type with cleared flags. The flag-check-then-RUnwrap gap at attacher.go:129 is also safe: after reattachment clears `FlagVertexConstraintsValid`, no attacher enters the RUnwrap path; if an attacher checked the flag before the clear, the RUnwrap's `FlagsUpNoLock` re-check inside the lock sees false → not marked Defined → poke registered.

**`consumed` map**: Stays on the same `*WrappedTx` — no split, no aliasing. Existing consumer tracking remains valid.

**Flag reset during reattachment**: Clear ALL mutable flags (`FlagVertexDefined`, `FlagVertexConstraintsValid`, `FlagVertexTxAttachmentFinished`, `FlagVertexIgnoreAbsenceOfPastCone`), then set `FlagVertexTxAttachmentStarted`. Clear `err`, reset `coverage` to nil. The vertex starts clean as Undefined.

#### Milestone attacher vs incremental attacher behavior

The `attacher` struct is shared between `milestoneAttacher` and `IncrementalAttacher`. They must behave differently on DetachedVertex:

| | Milestone attacher | Incremental attacher |
|---|---|---|
| **On DetachedVertex** | Trigger reattachment, wait via poke | Return error, abandon proposal |
| **Why** | Must solidify this specific tx (received from peer/self) | Building a proposal — can pick different dependencies |
| **Mechanism** | `go AttachTransaction(v.Transaction, env)` + ok=true | `setError(...)` + ok=false |

The incremental attacher (sequencer) should NOT trigger reattachment. A detached dependency means the proposal is stale — the sequencer should abandon it and try a different extend/endorse target. The detached vertex will eventually be reattached by a milestone attacher when the transaction arrives via gossip or pull. Future incremental attacher proposals can then use it.

**Distinction mechanism**: Add `onDetachedVertex` callback to the `attacher` struct:

```go
attacher struct {
    // ... existing fields ...
    onDetachedVertex func(vid *vertex.WrappedTx, tx *transaction.Transaction)
}
```

- milestoneAttacher sets: `func(vid, tx) { go AttachTransaction(tx, env) }`
- IncrementalAttacher leaves nil (default)

DetachedVertex handler in `attachVertexNonBranch`:
```go
DetachedVertex: func(v *vertex.DetachedVertex) {
    if a.onDetachedVertex != nil {
        a.onDetachedVertex(vid, v.Transaction)
        ok = true  // wait for reattachment via poke
    } else {
        a.setError(fmt.Errorf("detached vertex %s: dependency unavailable",
            vid.IDShortString()))
    }
}
```

Similarly in `attachVertexNonBranchSolid`:
```go
DetachedVertex: func(v *vertex.DetachedVertex) {
    if a.onDetachedVertex != nil {
        a.onDetachedVertex(vid, v.Transaction)
        needFallback = true  // retry via poke
    } else {
        a.setError(fmt.Errorf("detached vertex %s: dependency unavailable (solid path)",
            vid.IDShortString()))
    }
}
```

#### Re-solidification flow

When `go AttachTransaction(v.Transaction, env)` runs:

1. `AttachTxID(txid)` → finds existing vid in memDAG (same `*WrappedTx`)
2. Snapshot check → false (recent tx)
3. **New DetachedVertex handler** in `AttachTransaction` (parallel to existing VirtualTx handler):
   - Check `FlagVertexTxAttachmentStarted` → if set, reattachment already in progress, skip
   - Reset flags: `vid.flags = FlagVertexTxAttachmentStarted`
   - Clear err, reset coverage to nil
   - In-place conversion: `vid._put(_vertex{vertex.NewVertex(tx)})`
   - For seq tx: start milestoneAttacher goroutine (same as existing VirtualTx path)
   - For non-seq tx: `env.PokeAllWith(vid)` — encountering attachers will process inline
4. milestoneAttacher builds past cone from scratch, populating `Vertex.Inputs` and `Vertex.Endorsements` via `AttachTxID` for each dependency
5. On completion: `SetTxStatusGood(pastConeBase, coverage)` → `PokeAllWith(vid)`
6. All waiting attachers retry → `MergePastCone` with non-nil PastConeBase → subtree imported

#### Recursive reattachment (entire past cone detached)

If the reattached vertex's milestoneAttacher encounters MORE DetachedVertex dependencies, the same mechanism triggers recursively:

```
milestoneAttacher(branch) → encounters DetachedVertex seq tx A
  → go AttachTransaction(A.Transaction) → starts milestoneAttacher(A)
    → milestoneAttacher(A) encounters DetachedVertex seq tx B
      → go AttachTransaction(B.Transaction) → starts milestoneAttacher(B)
        → ... bottoms out at InTheState or alive vertices
      ← B becomes Good → PokeAllWith(B)
    ← A's attacher retries, B now Good → merge B's PastCone
  ← A becomes Good → PokeAllWith(A)
← branch attacher retries, A now Good → merge A's PastCone
```

Each level spawns a goroutine. The chain terminates at:
- **InTheState vertices**: BKT marks them Defined in `defineInTheStateStatus` before `attachVertexNonBranch` is reached. No traversal needed.
- **Alive (non-detached) Good vertices**: Normal `MergePastCone`.
- **VirtualTx**: Normal pull from txstore/peers.

The recursive case is bounded by TTL — with proper TTL tuning, most ancestors of a detached vertex are either still alive or InTheState. The double solidification effort is rare and doesn't cause slowdown.

#### InTheState vertices — no reattachment needed

For vertices where `BKT(baseline, vid) = true`: `defineInTheStateStatus` marks them `InTheState + Defined` at line 447 **before** `attachVertexNonBranch` is reached. `attachOutput` sees Defined at line 603 → returns true. The DetachedVertex handler never fires. This covers all vertices from slots before the baseline branch — the common case.

### Location of the invariant check

File: `ledger/multistate/mutate.go`, lines 547-551:

```go
if addAmount != delAmount+inflation[0] {
    err = fmt.Errorf("updateTrie: major inconsistency. Mismatch input amount(%s) + inflation(%s) != output amount(%s). Diff: %s",
        util.Th(delAmount), util.Th(inflation[0]), util.Th(addAmount), util.Th(int(addAmount)-int(delAmount+inflation[0])))
}
```

### Implementation Plan

#### Phase 1: In-place reattachment mechanism

**Step 1.1: `core/vertex/vid.go` — Add `ReattachVertexNoLock` method**

Replace the disabled `ReattachDetachedVertexNoLock` with a clean implementation:

```go
// ReattachVertexNoLock converts a DetachedVertex back to a fresh Vertex.
// Resets all mutable flags and status. The vertex becomes Undefined with clean state.
// Safe because *transaction.Transaction inside DetachedVertex is immutable.
// Must be called under write lock (inside Unwrap).
func (vid *WrappedTx) ReattachVertexNoLock(tx *transaction.Transaction) {
    vid._put(_vertex{NewVertex(tx)})
    vid.flags = FlagVertexTxAttachmentStarted
    vid.err = nil
    vid.pastCone = nil  // already nil, explicit for clarity
    vid.coverage.Store(nil)
}
```

**Step 1.2: `core/attacher/types.go` — Add `onDetachedVertex` callback**

Add field to the `attacher` struct:

```go
attacher struct {
    // ... existing fields ...
    // onDetachedVertex is called when the attacher encounters a DetachedVertex.
    // milestoneAttacher: triggers reattachment. IncrementalAttacher: nil (returns error).
    onDetachedVertex func(vid *vertex.WrappedTx, tx *transaction.Transaction)
}
```

**Step 1.3: `core/attacher/attacher_milestone.go` — Set callback for milestone attacher**

In `newMilestoneAttacher`, after setting `pokeMe`:

```go
ret.attacher.onDetachedVertex = func(vid *vertex.WrappedTx, tx *transaction.Transaction) {
    go AttachTransaction(tx, env)
}
```

IncrementalAttacher: no change needed — `onDetachedVertex` stays nil (default).

#### Phase 2: DetachedVertex handlers in attachment paths

**Step 2.1: `core/attacher/attacher.go` — `attachVertexNonBranch` DetachedVertex handler**

Replace GracefulShutdown with reattachment-or-error:

```go
DetachedVertex: func(v *vertex.DetachedVertex) {
    if a.onDetachedVertex != nil {
        a.onDetachedVertex(vid, v.Transaction)
        ok = true  // not defined — poke registered at line 202
    } else {
        a.setError(fmt.Errorf("detached vertex %s: dependency unavailable",
            vid.IDShortString()))
    }
},
```

**Step 2.2: `core/attacher/attacher.go` — `attachVertexNonBranchSolid` DetachedVertex handler**

Same pattern for the RUnwrap path:

```go
DetachedVertex: func(v *vertex.DetachedVertex) {
    if a.onDetachedVertex != nil {
        a.onDetachedVertex(vid, v.Transaction)
        needFallback = true
    } else {
        a.setError(fmt.Errorf("detached vertex %s: dependency unavailable (solid path)",
            vid.IDShortString()))
    }
},
```

#### Phase 3: `AttachTransaction` DetachedVertex support

**Step 3.1: `core/attacher/attach.go` — Add DetachedVertex handler in `AttachTransaction`**

After the existing VirtualTx handler (line ~212), add:

```go
reattached := false
vid.Unwrap(vertex.UnwrapOptions{
    DetachedVertex: func(v *vertex.DetachedVertex) {
        if vid.FlagsUpNoLock(vertex.FlagVertexTxAttachmentStarted) {
            return // reattachment already in progress
        }
        vid.ReattachVertexNoLock(v.Transaction)
        env.LogTx(time.Now(), "REATTACH START", txid)

        if vid.IsSequencerTransaction() {
            n := numAttachers.Add(1)
            go func() {
                defer numAttachers.Add(-1)
                env.IncCounter("att")
                defer env.DecCounter("att")
                env.MarkWorkProcessStarted(vid.IDShortString() + "_reattach")
                runMilestoneAttacher(vid, nil, nil, env, nil)
                env.MarkWorkProcessStopped(vid.IDShortString() + "_reattach")
            }()
        }
        reattached = true
    },
})
if reattached {
    env.PokeAllWith(vid)
}
```

**Step 3.2: Remove or update the existing DetachedVertex warning log** (attach.go:130-134) — no longer just a warning, reattachment is handled.

#### Phase 4: Cleanup and safeguards

**Step 4.1: Remove the disabled `ReattachDetachedVertexNoLock`** (vid.go:85-91) — replaced by the new `ReattachVertexNoLock`.

**Step 4.2: Add reattachment counter metric** — `proxima_reattach_counter` to monitor frequency. If reattachments are frequent, TTL tuning is needed.

**Step 4.3: Logging** — Log reattachment events at Info level for operational visibility: vertex ID, which attacher triggered it, depth of recursive reattachment chain.

#### Testing strategy

1. **Unit test**: Create a vertex, mark Good, ConvertToDetached, then ReattachVertexNoLock. Verify flags are clean, type is _vertex, consumed map preserved.
2. **Integration test**: In a sequencer test, force GC of a mid-slot dependency before the branch attacher runs. Verify the branch commits successfully with correct mutations.
3. **Incremental attacher test**: Verify that IncrementalAttacher encountering a DetachedVertex returns an error (not crash, not reattachment).
4. **Recursive reattachment test**: Force GC of multiple levels of dependencies. Verify the chain of reattachments completes and the top-level attacher succeeds.

### Files involved

| File | Relevance |
|------|-----------|
| `core/vertex/vid.go:85-91` | Disabled `ReattachDetachedVertexNoLock` → replace with `ReattachVertexNoLock` |
| `core/vertex/vid.go:93-116` | `ConvertToDetached` — the GC mechanism that creates the problem |
| `core/attacher/types.go:71-89` | `attacher` struct → add `onDetachedVertex` callback |
| `core/attacher/attacher.go:118-206` | `attachVertexNonBranch` → change DetachedVertex handler |
| `core/attacher/attacher.go:212-252` | `attachVertexNonBranchSolid` → change DetachedVertex handler |
| `core/attacher/attacher_milestone.go:78-110` | `newMilestoneAttacher` → set `onDetachedVertex` callback |
| `core/attacher/attach.go:111-224` | `AttachTransaction` → add DetachedVertex reattachment path |
| `core/attacher/attacher.go:414-457` | `defineInTheStateStatus` — handles InTheState DetachedVertex (no change needed) |
| `core/vertex/past_cone.go:625-689` | `Mutations()` — generates DEL/ADD mutation set (no change needed) |
| `ledger/multistate/mutate.go:547-551` | Token conservation assertion (no change needed) |

---

## Issue 2: Conflict in Past Cone (boot-acc and loc0-acc)

### Symptom

One conflict was detected on boot-acc and loc0-acc (same transaction propagated via gossip):

```
ATTACH [230239|71sq]0043ec811f2a.. (baseline: [230239|0br]01e96bea1fec..)
  -> BAD(conflict [230238|0br]010aea43e400..[0] in the past cone:
```

The transaction `[230239|71sq]` was rejected because output `[230238|0br]010aea43e400..[0]` appears to be double-spent in its past cone.

### Full Past Cone Dump

```
    ------ past cone: '[230239|71sq]0043ec811f2a..'
    ------ baseline: [230239|0br]01e96bea1fec..
    ------ tip: [230239|71sq]0043ec811f2a..

    #0  S+ [230233|27sq]008044959046.. consumers: {0: {[230239|46sq]0090062f6c7b..}}
        flags: known: true, defined: true, inTheState: (true,true),
        endorsementsOk: false, inputsOk: false

    #1  S+ [230233|27sq]00f1bb27a810.. consumers: {0: {[230238|79sq]002740dbf846..}}
        flags: known: true, defined: true, inTheState: (true,true),
        endorsementsOk: false, inputsOk: false

    #2  S+ [230238|0br]010aea43e400.. consumers: {0: {[230238|88sq]0065b0816716..}, 1: {[230239|0br]01e96bea1fec..}}
        flags: known: true, defined: true, inTheState: (true,true),
        endorsementsOk: false, inputsOk: false

    #3  S+ [230238|79sq]002740dbf846.. consumers: {0: {[230238|96sq]002b2ed601d6..}}
        flags: known: true, defined: true, inTheState: (true,true),
        endorsementsOk: true, inputsOk: true

    #4  S- [230238|88sq]0065b0816716.. consumers: {0: {[230239|0br]01e96bea1fec..}}
        flags: known: true, defined: true, inTheState: (true,false),
        endorsementsOk: true, inputsOk: true

    #5  S- [230238|96sq]002b2ed601d6.. consumers: {0: {[230239|55sq]00ae27cefeb0..}}
        flags: known: true, defined: true, inTheState: (true,false),
        endorsementsOk: true, inputsOk: true

    #6  S+ [230239|0br]01e96bea1fec.. consumers: {0: {[230239|63sq]0031ab78da0b..}}
        flags: known: true, defined: true, inTheState: (true,true),
        endorsementsOk: false, inputsOk: false

    #7  S- [230239|46sq]0090062f6c7b.. consumers: {}
        flags: known: true, defined: true, inTheState: (true,false),
        endorsementsOk: true, inputsOk: true

    #8  S- [230239|55sq]00ae27cefeb0.. consumers: {0: {[230239|71sq]0043ec811f2a..}}
        flags: known: true, defined: true, inTheState: (true,false),
        endorsementsOk: true, inputsOk: true

    #9  S- [230239|63sq]0031ab78da0b.. consumers: {}
        flags: known: true, defined: true, inTheState: (true,false),
        endorsementsOk: true, inputsOk: true, poke: true

    #10 S- [230239|71sq]0043ec811f2a.. consumers: {}
        flags: known: true, defined: false, inTheState: (true,false),
        endorsementsOk: true, inputsOk: true
```

### Analysis

The conflict is on `[230238|0br]010aea43e400..[0]` — output 0 of a branch from slot 230238.

Looking at entry #2:
```
[230238|0br]010aea43e400.. consumers: {0: {[230238|88sq]0065b0816716..}, 1: {[230239|0br]01e96bea1fec..}}
```

This is a **slot 238 branch** (InTheState=true). Its outputs:
- Output 0: consumed by `[230238|88sq]` (a sequencer tx from same slot)
- Output 1: consumed by `[230239|0br]01e96bea1fec..` (the slot 239 branch = the baseline)

These are **different outputs** (indices 0 and 1). There's no double-spend here at first glance.

But the conflict detection says `conflict [230238|0br]010aea43e400..[0]`. This means output 0 specifically is the problem. Output 0 is consumed by `[230238|88sq]`.

#### Chain reconstruction

The past cone shows two parallel chains converging:

**Chain A (boot's chain):**
```
[230238|0br]010aea43e400.. (state, slot 238 branch)
  output 0 → [230238|88sq]0065b0816716.. (delta, extends boot's chain)
    output 0 → [230239|0br]01e96bea1fec.. (state, baseline = boot's 239 branch)
      output 0 → [230239|63sq]0031ab78da0b.. (delta, boot's seq tx)
```

**Chain B (another sequencer's chain):**
```
[230233|27sq]008044959046.. (state, very old tx from slot 233)
  output 0 → [230239|46sq]0090062f6c7b.. (delta)

[230233|27sq]00f1bb27a810.. (state, another old tx from slot 233)
  output 0 → [230238|79sq]002740dbf846.. (state, another seq's tx)
    output 0 → [230238|96sq]002b2ed601d6.. (delta)
      output 0 → [230239|55sq]00ae27cefeb0.. (delta)
        output 0 → [230239|71sq]0043ec811f2a.. (tip, being attached)
```

The tip `[230239|71sq]` extends `[230239|55sq]` which extends `[230238|96sq]` which extends `[230238|79sq]`.

#### The conflict scenario

The conflict report says `[230238|0br]010aea43e400..[0]` is conflicted. Output 0 is consumed by `[230238|88sq]`. But the tip `[230239|71sq]` doesn't consume this output — it consumes `[230239|55sq]`'s output.

The conflict is detected because `[230238|88sq]` consumes `[230238|0br]...[0]`, and this consumption appears in the past cone. The conflict checker likely found that the same output is referenced as consumed by a transaction that is NOT in the tip's causal past, creating a conflict.

**Key question**: Is `[230238|88sq]` in the past cone of `[230239|71sq]`? Looking at the dump:
- `[230238|88sq]` (entry #4) IS in the past cone (S-, not in state)
- Its consumer is `{0: {[230239|0br]01e96bea1fec..}}` — the baseline branch
- The baseline `[230239|0br]` (entry #6) is InTheState

So the chain is:
```
[230238|0br]...[0] → consumed by [230238|88sq]...[0] → consumed by [230239|0br] (baseline)
```

Both `[230238|0br]` and `[230239|0br]` are InTheState. `[230238|88sq]` is NOT in state but IS in the past cone.

The conflict might be: output `[230238|0br]...[0]` is consumed by `[230238|88sq]`, but `[230238|88sq]` is NOT in the baseline state. If the baseline state has `[230238|0br]...[0]` as an unspent output (because `[230238|88sq]` wasn't committed in the baseline), then having `[230238|88sq]` consume it creates a conflict with the baseline's view.

Wait — that can't be right either. The baseline `[230239|0br]` consumes `[230238|0br]...[1]` (stem output), and `[230238|88sq]` consumes `[230238|0br]...[0]` (chain output). Both are in the past cone. The baseline was built on top of `[230238|88sq]` (since the baseline branch extends boot's chain through `[230238|88sq]`).

#### Key insight: explicit baseline from boot strategy

The conflict is clearly related to the **explicit baseline** set in transaction `[230239|46sq]0090062f6c7b..` (entry #7). This transaction consumes a very old output from slot 233 (`[230233|27sq]008044959046..`), which is 6 slots behind. This pattern — consuming an output many slots behind with an explicit baseline — is the signature of the **boot proposer** (`tryBootProposal` in `task/proposer_boot.go`).

The boot proposer creates transactions with an explicit baseline (the LRB) when the sequencer's own milestone is more than 1 slot behind. It fires when `extend.VID.Slot()+1 < targetTs.Slot`. However, in this context there was **no reason for the boot strategy to be active** — the network had active branches and sequencer transactions, the sequencer was not stale, and normal factory/base strategies should have been used instead.

**The boot strategy kicked in incorrectly.** This likely happened because `OwnLatestMilestoneOutput()` returned a stale milestone (from the tippool gap after branch submission), making the boot condition `extend.VID.Slot()+1 < targetTs.Slot` evaluate to true when it shouldn't have.

The explicit baseline introduced by the boot strategy creates a transaction whose past cone merges two independent state views — the explicit baseline (LRB) and the endorsement chain — which can have conflicting consumers for the same outputs.

#### Investigation needed

1. **Why the boot strategy fired**: Trace the conditions that led to `tryBootProposal` returning a proposal when normal strategies should have worked. Likely related to the tippool gap (branch not yet processed, OwnLatestMilestoneOutput returning stale data).

2. **Past cone reaction is inadequate**: The past cone logic's response to this kind of incorrectness (an explicit baseline that conflicts with the endorsement chain) should be investigated. Rather than producing a transaction that gets rejected as BAD with a conflict, the attacher or proposal validation should detect this incompatibility earlier and reject the proposal before submission. The fact that it was submitted, gossiped, and then rejected on access nodes means the conflict detection happens too late in the pipeline.

3. **Conflict detection specifics**: Trace the exact conflict detection logic to understand what condition `[230238|0br]010aea43e400..[0]` violates — whether it's a legitimate double-spend from merging incompatible state views, or a false positive from incomplete past cone traversal.

---

## Relationship Between the Two Issues

Both issues stem from Phase 2 changes creating scenarios that weren't common before:

- **Issue 1**: Non-baseline branches' inputs are not traversed → missing DEL mutations → token conservation violation. Root cause: branch short-circuit in `attachOutput` (line 591-594) that skips input traversal for non-state branch dependencies.
- **Issue 2**: Boot strategy fires incorrectly due to stale `OwnLatestMilestoneOutput()` (tippool gap after branch submission) → creates a transaction with explicit baseline that merges incompatible state views → conflict detected too late (after submission and gossip). Root cause: boot proposer condition check doesn't account for the tippool gap, and the past cone logic doesn't fail-fast on incompatible explicit baselines.

Both are triggered by the new timing characteristics of Phase 2 (plateau detection, longer intervals between submissions, tippool gap from fire-and-forget branch submission).

---

## Phase 2 Status

### What's deployed (testnet was running before crash)

Commits on `develop07-seq-improvement`:
1. `796ebe5a` — Phase 2 rewrite: event-driven sequencer with plateau detection
2. `5cdf8982` — fix: concurrent map access crash in txinput_queue
3. `db8f7ff7` — fix: deadline too tight and tight loop on failed branches
4. `3a8b425e` — fix: budget drain from repeated no-proposals in drain loop
5. `0f57adf1` — fix: allow base extend to fire on plateau when factory has no skeleton

### Uncommitted local changes (not deployed)

1. **Remove `adjustBudget(false)` from plateau path** — "no proposals" is not an overload signal, budget should only decrease on branch failures
2. **Increase `finalizationTicks` from 3 to 12** — the 3-tick (240ms) deadline was too short for `task.Run` to solidify and finalize, causing "context deadline exceeded" on most proposals

### Known issues to address (in order of priority)

1. **[CRITICAL] Token conservation crash** — Fix the branch short-circuit in `attachOutput`. This blocks all testnet operation.
2. **`finalizationTicks` too short** — Increase from `plateauHoldTicks` (3) to a separate `finalizationTicks` (12) constant. Without this, most proposals fail with context deadline exceeded.
3. **Budget drain** — Remove `adjustBudget(false)` from plateau detection path. "No proposals" on plateau is normal idle behavior, not overload.
4. **Alternating dead/active slots** — Even with fixes 2 and 3, the pattern may persist. The ~4 second branch processing time creates a gap where `OwnLatestMilestoneOutput()` returns stale data. Base extend can't work until the branch appears in the tippool.
5. **`branch: NONE` in SLOT STATS** — The branch submission is not tracked in slotData because the branch is for the NEXT slot but slotData is for the current slot. Cosmetic but confusing.
