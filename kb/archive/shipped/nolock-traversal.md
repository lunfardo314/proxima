# No-Lock Past Cone Traversal

## Problem

The milestone attacher's `solidifyPastCone` and `solidifyBaseline` hold the **write lock on the tip vertex** for the entire duration of each traversal iteration. Inside the Unwrap callback, the attacher processes dependencies by calling `attachVertexNonBranch(dep)`, which itself calls `dep.Unwrap(...)` — acquiring write locks on each dependency.

This creates a lock chain: **WLock(tip) → WLock(dep1) → WLock(dep2) → ...**

When two milestone attachers run concurrently with mutually referencing vertices, the lock chain creates a **deadlock cycle**:

```
Attacher A: WLock(X) → processes deps → Unwrap(Y) → needs WLock(Y) → BLOCKED
Attacher B: WLock(Y) → solidifyBaseline → GetTxStatus(X) → needs RLock(X) → BLOCKED
```

### How it manifests

This was exposed by the reattachment mechanism: `go AttachTransaction` starts concurrent milestone attachers for recently-GC'd vertices. Two reattachment attachers for nearby vertices (X in Y's past cone, or X is Y's baseline) deadlock.

The deadlock was detected by the 30-second deadlock checker in `lazyRepeat`:
```
FATAL [SEQ:seq1] >>>>>>>> DEADLOCK suspected in the sequencer loop
```

Stack traces confirm: one attacher in `solidifyPastCone` holds WLock(X) and blocks on WLock(Y); another in `solidifyBaseline` holds WLock(Y) and blocks on RLock(X).

### Broader implications

Even without reattachment, the write-lock-during-traversal pattern causes **contention** between any concurrent attachers with overlapping past cones:

- Attacher A holds WLock on its tip → processes dependencies → any other goroutine trying to read A's tip (RUnwrap, GetTxStatus, FlagsUp) blocks
- Multiple attachers running in parallel create chains of blocked goroutines waiting for tips to be released
- High TPS amplifies this: more transactions → more concurrent attachers → more contention

The current design effectively serializes all attachers that share any vertex in their past cones, even though the traversal is **read-only on the tip**.

## Analysis: what locks actually protect

### The tip vertex's write lock

During `solidifyPastCone`, the Unwrap callback does:

```go
a.vid.Unwrap(vertex.UnwrapOptions{
    Vertex: func(v *vertex.Vertex) {
        // 1. Check status (read-only on tip)
        a.Assertf(a.vid.GetTxStatusNoLock() == vertex.Undefined, ...)
        
        // 2. Traverse past cone (reads tip's Inputs/Endorsements, writes to DEPS not tip)
        ok = a.attachVertexUnwrapped(v, a.vid)
        
        // 3. Validate (writes to v.Transaction via SetFullContext, not to WrappedTx)
        ok, finalSuccess = a.validateSequencerTxUnwrapped(v)
    },
})
```

The write lock on `a.vid` protects:
- `_genericVertex` — prevents type change (e.g., ConvertToDetached) during access
- `flags`, `err`, `pastCone` — prevents concurrent modification

But during the traversal:
- **Step 1**: Read-only. RLock would suffice.
- **Step 2**: Reads `v.Inputs` and `v.Endorsements` (Vertex struct fields). Writes to dependency vertices (their own locks). Does NOT write to the tip's WrappedTx fields.
- **Step 3**: Writes to `v.Transaction` (SetFullContext). This modifies the Transaction object, which is accessed through the Vertex pointer — NOT through WrappedTx's lock-protected fields.

**The WrappedTx write lock on the tip is not needed during steps 1-3.** The Vertex pointer obtained from the Unwrap is stable because:
- `FlagVertexTxAttachmentStarted` is set → prevents concurrent `ConvertToDetached` (GC checks `IsVertexReferencedBySequencer`)
- Only one milestone attacher runs per sequencer tip → no concurrent modification of the tip's Inputs/Endorsements
- The `pastCone` field on WrappedTx is nil during solidification (set at the end via `SetTxStatusGood`)

### Dependency vertices: write lock IS needed for Undefined non-sequencers

While only one milestone attacher runs per *sequencer tip*, the same non-sequencer vertex
can be touched by **multiple attacher goroutines concurrently** because past cones overlap.

In `attachVertexNonBranch`, the Unwrap (write lock) on a dependency vertex protects:

1. **`v.ReferenceEndorsement(index, vid)`** (line 515) and **`v.ReferenceInput(inputIdx, vid)`** (line 550) — write to `v.Endorsements[i]` / `v.Inputs[i]`. These are idempotent (nil → pointer), but two attachers racing on the same slot is a data race without the write lock.

2. **`finalTouchNonSequencer`** (lines 350-385) — calls `validateVertex` then `SetFlagsUpNoLock(FlagVertexConstraintsValid)`. Two attachers could both enter this block before the flag is set.

3. **`v.UnReferenceDependencies()`** (line 367) on validation failure — destructive, clears references other attachers may be reading.

**Conclusion**: the write lock on each dependency during the Undefined case is legitimately needed. The refactoring must preserve it. What we remove is the **tip's** write lock during the entire traversal.

### What truly needs the write lock on the tip

- `SetTxStatusGood(pastConeBase, coverage)` — sets flags, pastCone, coverage. Called AFTER solidification, not during.
- `SetTxStatusBad(err)` — sets error. Called on failure.
- `ConvertToDetached()` — changes type. Prevented by `FlagVertexTxAttachmentStarted`.

## Proposed refactoring

### Core idea: two changes

1. **Tip vertex**: Replace the `Unwrap`-with-long-callback pattern in `solidifyPastCone` and `solidifyBaseline` with a brief lock to obtain the Vertex pointer, followed by lock-free processing.

2. **Dependency vertices**: Unify the two traversal paths (`attachVertexNonBranch` + `attachVertexNonBranchSolid`) into a single path: **RUnwrap first**, escalate to **Unwrap only when write access is needed** (Undefined non-sequencer case).

### Change 1: New WrappedTx method for tip access

```go
// GetVertex returns the Vertex pointer under a brief read lock.
// Returns nil if the underlying type is not _vertex (DetachedVertex or VirtualTx).
// The returned pointer is safe to use after the lock is released when the caller
// guarantees no concurrent type change (FlagVertexTxAttachmentStarted is set).
func (vid *WrappedTx) GetVertex() *Vertex {
    vid.mutex.RLock()
    defer vid.mutex.RUnlock()
    if v, ok := vid._genericVertex.(_vertex); ok {
        return v.Vertex
    }
    return nil
}
```

### Change 1: Refactored solidifyPastCone

```go
func (a *milestoneAttacher) solidifyPastCone() vertex.Status {
    return a.lazyRepeat("past cone solidification", func() (status vertex.Status) {
        v := a.vid.GetVertex()
        if v == nil {
            // vertex was converted to DetachedVertex or VirtualTx — shouldn't happen
            // with FlagVertexTxAttachmentStarted set
            a.setError(fmt.Errorf("solidifyPastCone: vertex %s is not a Vertex", a.vid.IDShortString()))
            return vertex.Bad
        }
        
        // Status check under brief read lock
        if a.vid.GetTxStatus() != vertex.Undefined {
            a.setError(fmt.Errorf("solidifyPastCone: unexpected status for %s", a.vid.IDShortString()))
            return vertex.Bad
        }
        
        // Past cone traversal WITHOUT holding tip's lock
        if ok := a.attachVertexUnwrapped(v, a.vid); !ok {
            a.Assertf(a.err != nil, "a.err != nil")
            return vertex.Bad
        }
        
        ok, finalSuccess := a.validateSequencerTxUnwrapped(v)
        if !ok {
            a.Assertf(a.err != nil, "a.err != nil")
            return vertex.Bad
        }
        
        if finalSuccess {
            return vertex.Good
        }
        return vertex.Undefined
    })
}
```

### Change 1: Refactored solidifyBaseline

Same pattern: `GetVertex()` under brief lock, then `solidifyBaselineUnwrapped(v, a.vid)` without holding the tip's lock.

### Change 2: Unified `attachVertexNonBranch` — RUnwrap first, escalate if needed

Currently there are two functions with duplicated logic:
- `attachVertexNonBranch` — Unwrap (write lock) for all cases
- `attachVertexNonBranchSolid` — RUnwrap (read lock) for validated non-seq vertices

The `DetachedVertex` handling, `defined` flag logic, and `pokeMe` fallback are duplicated.
The TODO at line 197-199 already notes this.

**Unified approach** — one function, one traversal path:

```go
func (a *attacher) attachVertexNonBranch(vid *vertex.WrappedTx) (ok bool) {
    if a.pastCone.IsKnownDefined(vid) {
        return true
    }

    needWriteLock := false
    defined := false

    // Step 1: RUnwrap — read lock first for all cases
    vid.RUnwrap(vertex.UnwrapOptions{
        Vertex: func(v *vertex.Vertex) {
            switch vid.GetTxStatusNoLock() {
            case vertex.Undefined:
                if vid.IsSequencerTransaction() {
                    ok = true // don't go deeper for undefined sequencers
                    return
                }
                if vid.FlagsUpNoLock(vertex.FlagVertexConstraintsValid) {
                    // Already validated by another attacher — read-only traversal
                    ok = a.attachVertexUnwrapped(v, vid)
                    if ok && a.pastCone.Flags(vid).FlagsUp(
                        vertex.FlagPastConeVertexInputsSolid|vertex.FlagPastConeVertexEndorsementsSolid) {
                        defined = true
                    }
                } else {
                    // Needs write access for referencing deps + validation
                    needWriteLock = true
                    ok = true
                }

            case vertex.Good:
                // Only sequencer transactions become Good.
                // Merge PastConeBase or handle InTheState/detached — same as current code
                // ... (read-only on vid, writes to attacher's pastCone)
                // [existing Good-case logic here]

            case vertex.Bad:
                a.setError(vid.GetErrorNoLock())
            }
        },
        DetachedVertex: func(v *vertex.DetachedVertex) {
            if a.onDetachedVertex != nil {
                a.onDetachedVertex(vid, v.Transaction)
                ok = true
            } else {
                a.setError(fmt.Errorf("attacher %s: detached vertex %s: dependency unavailable",
                    a.name, vid.IDShortString()))
            }
        },
        VirtualTx: func(_ *vertex.VirtualTransaction) {
            ok = true
        },
    })

    if !ok {
        return
    }

    // Step 2: Escalate to write lock only for Undefined non-seq that needs mutation
    if needWriteLock {
        vid.Unwrap(vertex.UnwrapOptions{
            Vertex: func(v *vertex.Vertex) {
                // Re-check: another attacher may have validated between RUnwrap release and Unwrap acquire
                if vid.FlagsUpNoLock(vertex.FlagVertexConstraintsValid) {
                    // Another attacher beat us — read-only traversal now
                    ok = a.attachVertexUnwrapped(v, vid)
                    if ok && a.pastCone.Flags(vid).FlagsUp(
                        vertex.FlagPastConeVertexInputsSolid|vertex.FlagPastConeVertexEndorsementsSolid) {
                        defined = true
                    }
                    return
                }
                // Still Undefined — do the write work
                ok = a.attachVertexUnwrapped(v, vid)
                if ok && vid.FlagsUpNoLock(vertex.FlagVertexConstraintsValid) &&
                    a.pastCone.Flags(vid).FlagsUp(
                        vertex.FlagPastConeVertexInputsSolid|vertex.FlagPastConeVertexEndorsementsSolid) {
                    defined = true
                }
            },
            DetachedVertex: func(v *vertex.DetachedVertex) {
                // Race: converted between RUnwrap and Unwrap
                if a.onDetachedVertex != nil {
                    a.onDetachedVertex(vid, v.Transaction)
                    ok = true
                } else {
                    a.setError(fmt.Errorf("attacher %s: detached vertex %s: dependency unavailable",
                        a.name, vid.IDShortString()))
                    ok = false
                }
            },
            VirtualTx: func(_ *vertex.VirtualTransaction) {
                ok = true
            },
        })
        if !ok {
            return
        }
    }

    if defined {
        a.pastCone.SetFlagsUp(vid, vertex.FlagPastConeVertexDefined)
    } else if a.pokeMe != nil {
        a.pokeMe(vid)
    }
    return
}
```

**Key properties of the unified path:**
- Every vertex is first touched with **RUnwrap** (read lock)
- Only Undefined non-sequencer vertices without `FlagVertexConstraintsValid` escalate to **Unwrap** (write lock)
- The re-check after escalation handles the race where another attacher validates the vertex between RUnwrap release and Unwrap acquire
- `attachVertexNonBranchSolid` is eliminated — no more duplicated DetachedVertex/pokeMe logic
- Good/Bad sequencer cases, VirtualTx, DetachedVertex — all handled under read lock only

## Safety analysis

### Change 1 safety: tip vertex pointer stays valid

1. **FlagVertexTxAttachmentStarted** is set before the milestone attacher starts. GC's `ConvertToDetached` checks `IsVertexReferencedBySequencer` which returns true when this flag is set (and attachment not finished). So `_genericVertex` won't change to `_detachedVertex`.

2. **Only one milestone attacher per sequencer tip**. The flag check in `AttachTransaction` prevents duplicate attachers. No concurrent modification of the tip's Inputs/Endorsements.

3. **The Go GC keeps the Vertex alive**. The milestone attacher holds `a.vid` (strong pointer to WrappedTx), which contains the `_vertex{Vertex}`. The Vertex won't be garbage collected.

#### What could go wrong with the tip

- **If `FlagVertexTxAttachmentStarted` is not set**: the GC could convert the vertex. But this flag is always set before `runMilestoneAttacher` is called.

- **If two milestone attachers run for the same vertex**: Inputs/Endorsements could be modified concurrently. But the flag check prevents this.

- **If someone calls `SetTxStatusGood` while traversal is in progress**: this writes to `flags` and `pastCone`. But `SetTxStatusGood` is called by the milestone attacher itself (after `solidifyPastCone` returns Good), not by anyone else.

- **Race with `attachVertexNonBranch` from other attachers**: other attachers may call `vid.Unwrap(...)` on the tip vertex (for their own traversal, if the tip is in their past cone). With the refactoring, the tip's lock is NOT held during traversal, so other attachers can freely Unwrap/RUnwrap the tip. No contention.

#### Verify: does `attachVertexUnwrapped` write to the tip?

`attachVertexUnwrapped(v, vid)` processes `v.Inputs` and `v.Endorsements`. It calls `attachInput`/`attachEndorsement` for each dependency. These functions:
- Call `refreshDependencyStatus(dep)` — modifies the attacher's pastCone (NOT the tip's WrappedTx)
- Call `attachOutput(wOut)` → `attachVertexNonBranch(dep)` — locks DEP, not tip
- Populate `v.Inputs[i]` if nil — writes to the Vertex struct directly (NOT through WrappedTx lock)

The Vertex struct writes (`v.Inputs[i] = ...`) are safe because only one milestone attacher processes this sequencer tip (flag guarantee). No lock on the tip is needed.

#### Verify: does `validateSequencerTxUnwrapped` write to the tip?

`validateSequencerTxUnwrapped(v)`:
- Calls `v.ValidateConstraints()` → `tx.SetFullContext(inputLoader)` — writes to `v.Transaction` (the Transaction object, not WrappedTx)
- Calls `a.pastCone.CheckAndClean(...)` — reads pastCone structure

Same analysis: writes to the Transaction object are safe because only one attacher processes the sequencer tip.

### Change 2 safety: unified RUnwrap-first traversal of dependencies

#### Concurrent access to non-sequencer vertices

Non-sequencer vertices in overlapping past cones can be touched by **multiple attacher goroutines concurrently**. This is not limited to milestone attachers — any attacher traversing a past cone that includes the vertex will access it.

The write lock on a dependency vertex protects three operations during the Undefined case:

1. **`v.ReferenceEndorsement(index, vid)` / `v.ReferenceInput(inputIdx, vid)`** — write nil → pointer into `v.Endorsements[i]` / `v.Inputs[i]`. Idempotent but still a data race without synchronization.

2. **`finalTouchNonSequencer`** — calls `validateVertex` then `SetFlagsUpNoLock(FlagVertexConstraintsValid)`. Without the lock, two attachers could both enter validation before the flag is set.

3. **`v.UnReferenceDependencies()`** on validation failure — destructive, clears references that other attachers may be reading.

**The unified path preserves these guarantees**: the Unwrap (write lock) is still acquired for Undefined non-sequencer vertices. Only the initial probe uses RUnwrap.

#### The RUnwrap → Unwrap gap

Between releasing the RUnwrap and acquiring the Unwrap, the vertex state can change:
- Another attacher may validate it → `FlagVertexConstraintsValid` gets set
- GC may convert it to DetachedVertex

The re-check inside the Unwrap callback handles both cases:
- If `FlagVertexConstraintsValid` is now set → treat as solid (read-only traversal)
- If DetachedVertex → trigger reattachment (same as current code)

`FlagVertexConstraintsValid` is **monotonic** (once set, never cleared), so the re-check is safe: if it was false during RUnwrap and true during Unwrap, the vertex was validated by another attacher and is now immutable.

#### Already-validated vertices under RUnwrap

When `FlagVertexConstraintsValid` is set, `attachVertexUnwrapped` under read lock is safe:
- `v.Endorsements[i]` and `v.Inputs[i]` are already populated (non-nil) — no writes
- `finalTouchNonSequencer` sees the flag and skips validation — no writes
- Only the attacher's own `pastCone` is modified — not protected by the vertex lock

This is the same logic as the current `attachVertexNonBranchSolid`, but now integrated into the single path instead of being a separate function.

## Impact on contention

### Before (current)

```
Tip vertex:
  Attacher A: WLock(tipA) held for ~10ms (entire traversal)
    → Any goroutine needing tipA (RUnwrap, GetTxStatus, FlagsUp) blocks
    → Including: other milestone attachers, incremental attachers, GC checks

Dependency vertices:
  Every dependency: WLock(dep) even when already validated (read-only case)
    → Serializes all attachers traversing the same validated vertex
  Two separate code paths with duplicated logic
```

### After (proposed)

```
Tip vertex:
  Attacher A: RLock(tipA) held for ~1μs (GetVertex), then released
    → Past cone traversal runs WITHOUT tipA lock
    → Other goroutines can freely access tipA concurrently
    → Deadlock between concurrent attachers is impossible

Dependency vertices:
  Already validated: RLock(dep) — concurrent readers, no blocking
  Undefined non-seq: RLock(dep) probe → WLock(dep) only when writes needed
  Re-check after escalation handles race with concurrent validators
  Single code path, no duplication
```

### Performance implications

- **Eliminates deadlock** between reattachment milestone attachers
- **Reduces contention on tip** between any concurrent attachers with overlapping past cones
- **Reduces contention on dependencies** — validated vertices use read lock only, allowing concurrent traversal
- **Enables higher parallelism** for milestone attachment under high TPS
- **Eliminates code duplication** between `attachVertexNonBranch` and `attachVertexNonBranchSolid`
- **No new risks**: the safety invariants (FlagVertexTxAttachmentStarted, single attacher per sequencer tip, write lock for Undefined mutations) are all preserved

## Implementation plan

### Step 1: Add `GetVertex()` method to WrappedTx

Add `GetVertex() *Vertex` (brief RLock, returns pointer) and `GetVertexNoLock() *Vertex` (for use inside existing Unwrap callbacks).

### Step 2: Unify `attachVertexNonBranch` — RUnwrap first, escalate if needed

Replace the two functions (`attachVertexNonBranch` + `attachVertexNonBranchSolid`) with a single function:
- RUnwrap first for all cases
- Escalate to Unwrap only for Undefined non-seq without `FlagVertexConstraintsValid`
- Re-check status after escalation (handles race with concurrent validators)
- Delete `attachVertexNonBranchSolid`

### Step 3: Refactor `solidifyPastCone`

Replace `a.vid.Unwrap(...)` with `v := a.vid.GetVertex()` + status check + lock-free traversal. Handle nil (DetachedVertex/VirtualTx) as error.

### Step 4: Refactor `solidifyBaseline`

Same pattern. The `v.BaselineBranchID` write (`a.setBaseline`) doesn't need the tip's lock — it writes to the attacher's pastCone, not to the WrappedTx.

### Step 5: Audit other Unwrap sites in milestone attacher

Check `newMilestoneAttacher` (line 99), `wrapUpAttacher`, `checkConsistencyBeforeWrapUp` — these may also hold the lock longer than needed.

### Step 6: Test

- Existing tests (all should pass — behavior unchanged)
- Stress test with forced reattachment (concurrent milestone attachers for nearby vertices)
- Race detector: `go test -race ./...` to verify no data races on Vertex fields

## Files involved

| File | Change |
|------|--------|
| `core/vertex/vid.go` | Add `GetVertex()`, `GetVertexNoLock()` methods |
| `core/vertex/types.go` | No change (Vertex struct is already public) |
| `core/attacher/attacher.go` | Unify `attachVertexNonBranch` + `attachVertexNonBranchSolid` into single RUnwrap-first path. Delete `attachVertexNonBranchSolid` |
| `core/attacher/attacher_milestone.go` | Refactor `solidifyPastCone`, `solidifyBaseline` to use `GetVertex()` |

## Open questions

1. **Should the Vertex pointer be cached in the milestone attacher?** Instead of calling `GetVertex()` on every lazyRepeat iteration, the attacher could grab it once during construction. Safe because the type won't change (flag guarantee).

2. **Should `solidifyBaseline` use a separate lock-free approach?** Its `solidifyBaselineUnwrapped` accesses the baseline chain, which may involve locking branch vertices. The same deadlock pattern could occur if two attachers traverse overlapping baseline chains.
