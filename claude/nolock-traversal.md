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

## Analysis: what the tip's write lock actually protects

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

**The WrappedTx write lock is not needed during steps 1-3.** The Vertex pointer obtained from the Unwrap is stable because:
- `FlagVertexTxAttachmentStarted` is set → prevents concurrent `ConvertToDetached` (GC checks `IsVertexReferencedBySequencer`)
- Only one milestone attacher runs per vertex → no concurrent modification of the Vertex's Inputs/Endorsements
- The `pastCone` field on WrappedTx is nil during solidification (set at the end via `SetTxStatusGood`)

### What truly needs the write lock

- `SetTxStatusGood(pastConeBase, coverage)` — sets flags, pastCone, coverage. Called AFTER solidification, not during.
- `SetTxStatusBad(err)` — sets error. Called on failure.
- `ConvertToDetached()` — changes type. Prevented by `FlagVertexTxAttachmentStarted`.

## Proposed refactoring

### Core idea

Replace the `Unwrap`-with-long-callback pattern in `solidifyPastCone` and `solidifyBaseline` with a brief lock to obtain the Vertex pointer, followed by lock-free processing.

### New WrappedTx method

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

### Refactored solidifyPastCone

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

### Refactored solidifyBaseline

Same pattern: `GetVertex()` under brief lock, then `solidifyBaselineUnwrapped(v, a.vid)` without holding the tip's lock.

## Safety analysis

### Why the Vertex pointer stays valid

1. **FlagVertexTxAttachmentStarted** is set before the milestone attacher starts. GC's `ConvertToDetached` checks `IsVertexReferencedBySequencer` which returns true when this flag is set (and attachment not finished). So `_genericVertex` won't change to `_detachedVertex`.

2. **Only one milestone attacher per vertex**. The flag check in `AttachTransaction` prevents duplicate attachers. No concurrent modification of Inputs/Endorsements.

3. **The Go GC keeps the Vertex alive**. The milestone attacher holds `a.vid` (strong pointer to WrappedTx), which contains the `_vertex{Vertex}`. The Vertex won't be garbage collected.

### What could go wrong

- **If `FlagVertexTxAttachmentStarted` is not set**: the GC could convert the vertex. But this flag is always set before `runMilestoneAttacher` is called.

- **If two milestone attachers run for the same vertex**: Inputs/Endorsements could be modified concurrently. But the flag check prevents this.

- **If someone calls `SetTxStatusGood` while traversal is in progress**: this writes to `flags` and `pastCone`. But `SetTxStatusGood` is called by the milestone attacher itself (after `solidifyPastCone` returns Good), not by anyone else.

- **Race with `attachVertexNonBranch` from other attachers**: other attachers may call `vid.Unwrap(...)` on the tip vertex (for their own traversal, if the tip is in their past cone). With the refactoring, the tip's lock is NOT held during traversal, so other attachers can freely Unwrap/RUnwrap the tip. No contention.

### Verify: does `attachVertexUnwrapped` write to the tip?

`attachVertexUnwrapped(v, vid)` processes `v.Inputs` and `v.Endorsements`. It calls `attachInput`/`attachEndorsement` for each dependency. These functions:
- Call `refreshDependencyStatus(dep)` — modifies the attacher's pastCone (NOT the tip's WrappedTx)
- Call `attachOutput(wOut)` → `attachVertexNonBranch(dep)` — locks DEP, not tip
- Populate `v.Inputs[i]` if nil — writes to the Vertex struct directly (NOT through WrappedTx lock)

The Vertex struct writes (`v.Inputs[i] = ...`) are safe because only one milestone attacher processes this vertex (flag guarantee). No lock on the tip is needed.

### Verify: does `validateSequencerTxUnwrapped` write to the tip?

`validateSequencerTxUnwrapped(v)`:
- Calls `v.ValidateConstraints()` → `tx.SetFullContext(inputLoader)` — writes to `v.Transaction` (the Transaction object, not WrappedTx)
- Calls `a.pastCone.CheckAndClean(...)` — reads pastCone structure

Same analysis: writes to the Transaction object are safe because only one attacher processes it.

## Impact on contention

### Before (current)

```
Attacher A: WLock(tipA) held for ~10ms (entire traversal)
  → Any goroutine needing tipA (RUnwrap, GetTxStatus, FlagsUp) blocks
  → Including: other milestone attachers, incremental attachers, GC checks
```

### After (proposed)

```
Attacher A: RLock(tipA) held for ~1μs (GetVertex), then released
  → Past cone traversal runs WITHOUT tipA lock
  → Other goroutines can freely access tipA concurrently
  → Deadlock between concurrent attachers is impossible
```

### Performance implications

- **Eliminates deadlock** between reattachment milestone attachers
- **Reduces contention** between any concurrent attachers with overlapping past cones
- **Enables higher parallelism** for milestone attachment under high TPS
- **No new risks**: the safety invariants (FlagVertexTxAttachmentStarted, single attacher per vertex) are already enforced

## Implementation plan

### Step 1: Add `GetVertex()` method to WrappedTx

Add `GetVertex() *Vertex` (brief RLock, returns pointer) and `GetVertexNoLock() *Vertex` (for use inside existing Unwrap callbacks).

### Step 2: Refactor `solidifyPastCone`

Replace `a.vid.Unwrap(...)` with `v := a.vid.GetVertex()` + status check + lock-free traversal. Handle nil (DetachedVertex/VirtualTx) as error.

### Step 3: Refactor `solidifyBaseline`

Same pattern. The `v.BaselineBranchID` write (`a.setBaseline`) doesn't need the tip's lock — it writes to the attacher's pastCone, not to the WrappedTx.

### Step 4: Audit other Unwrap sites in milestone attacher

Check `newMilestoneAttacher` (line 99), `wrapUpAttacher`, `checkConsistencyBeforeWrapUp` — these may also hold the lock longer than needed.

### Step 5: Test

- Existing tests (all should pass — behavior unchanged)
- Stress test with forced reattachment (concurrent milestone attachers for nearby vertices)
- Race detector: `go test -race ./...` to verify no data races on Vertex fields

### Step 6: Consider `attachVertexNonBranch` locking

Currently uses Unwrap (write lock) for ALL cases. The Good case and DetachedVertex case are read-only on the dependency. Consider using RUnwrap for these cases (the TODO already noted in the code). This is a natural follow-up that further reduces contention.

## Files involved

| File | Change |
|------|--------|
| `core/vertex/vid.go` | Add `GetVertex()`, `GetVertexNoLock()` methods |
| `core/vertex/types.go` | No change (Vertex struct is already public) |
| `core/attacher/attacher_milestone.go` | Refactor `solidifyPastCone`, `solidifyBaseline` |
| `core/attacher/attacher.go` | (Step 6) Consider RUnwrap for `attachVertexNonBranch` Good/DetachedVertex cases |

## Open questions

1. **Should `attachVertexNonBranch` also be refactored?** It holds WLock on each dependency during processing. With the tip lock removed, the dependency locks are the remaining contention point. Step 6 addresses this partially.

2. **Should the Vertex pointer be cached in the milestone attacher?** Instead of calling `GetVertex()` on every lazyRepeat iteration, the attacher could grab it once during construction. Safe because the type won't change (flag guarantee).

3. **Should `solidifyBaseline` use a separate lock-free approach?** Its `solidifyBaselineUnwrapped` accesses the baseline chain, which may involve locking branch vertices. The same deadlock pattern could occur if two attachers traverse overlapping baseline chains.
