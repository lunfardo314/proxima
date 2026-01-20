# Add attachment cost and attachment budget

## Current solution and the problem

Unbounded chains of transactions in the past cone is an attack vector. Attacker may create a chain of transaction with
unlimited length and post it with tag-along output in the tip of the chain. That may become a kind of denial-of-service attack.

To prevent it we introduced limits on how deep can be the depth of recursion in the attacher. 
When recursion depth reaches constant `constAttachmentRecursionDepthBase`, attacher is invalidated. 
It is a deterministic process because constant is the same across nodes. 

The recursion depth limits works also in the incremental attacher during transaction construction in the sequencer: 
transactions is constructed by adding new tag-along inputs. Once `constAttachmentRecursionDepthBase` is reached, newly added 
tag-along input is invalidated and the incremental attacher is rolled back.

Current solution works but is not perfect: depth may be not very big, however an attacker can create very wide past cone with exponentially many transactions.

## Goal
The goal of this task is to introduce concept of _attachment cost_ that would replace the _attachmentDepth_.
We introduce a new ledger constant `constAttachmentCostBudget` that replaces the depth constant

* the _attachment cost of particular transaction_ is sum of number of inputs and produced outputs. It roughly approximates
validation cost of the transaction
* the _attachment cost of the past cone of the sequencer transaction_ is sum of attachment costs of non-sequencer transactions
in the past cone of it back to the baseline. It is represented by the `PastCone` data structure and respective functions.

We want the following behaviour:

### Milestone attacher
* during attachment process of the sequencer transaction in the attacher, the `pastCone` is filled up step by step with the transactions.
This is the job of the attacher's goroutine. 
* Upon determining the status of a vertex in the past cone, when becomes clear that **non-sequencer** transaction **is not in the baseline state** 
(i.e. it is just in the DAG and not committed to any branch), the attachment cost of the sequencer transaction/attacher/past cone increases.
* whenever during the attachment process we detect that accumulated attachment cost reaches the _attachment cost budget_, the attacher (and the sequencer transaction) is invalidated. 
This way we enforce limited amount of complexity for the attacher. 

The process must be strictly deterministic: if transaction is valid on all nodes, or it is invalid on all nodes.

### Incremental attacher
The incremental attacher is adding tag-along outputs one by one to the attacher.
We must detect the moment when adding output results in exceeded attachment budget and roll back the attacher.
The resulting past cone of the transaction must always have attachment budget below the limit.

---

## Clarifications

### Budget value
The `constAttachmentCostBudget` should be approximately **550-600**. This allows for one maximum-sized tag-along
transaction (256 inputs + 256 outputs = 512) in the past cone of a sequencer transaction with some headroom.

### What to remove
- The `depth int` parameter passed through recursive attacher functions (`attachVertexUnwrapped`, `attachEndorsements`,
  `attachInput`, etc.) should be removed
- The `WithAttachmentDepth` option should be removed
- The ledger constant `constAttachmentRecursionDepthBase` should be replaced with `constAttachmentCostBudget`

### What to add
- The `WithAttachmentCostBudget` option should be added (replacing `WithAttachmentRecursionDepthBase`)

### What NOT to touch
- `attachmentDepth` field in `WrappedTx` (types.go:64) - used for syncing, misleading name
- `SetAttachmentDepthNoLock()` and `GetAttachmentDepthNoLock()` functions - used for syncing

### Budget enforcement
- When budget is exceeded, transaction is marked as `Bad` with error message (same as other validation failures)
- In practice, this should never happen because sequencers use incremental attacher which always produces valid transactions
- The enforcement is a safety mechanism against malicious attacks

### Incremental attacher behavior
- Can be invalidated during recursive traversal (same as milestone attacher) when attachment cost exceeds budget
- Additionally, should check budget before `CommitDelta()` as a safety measure

---

## Current Progress

### Already implemented (commit 1e011cbc)
- `AttachmentCost()` method on `PastCone` - returns incremental attachment cost
- `AttachmentCostDirect()` method on `PastCone` - calculates by iterating all vertices (for verification)
- `addToAttachmentCost()` - adds to delta or base attachment cost
- Incremental tracking in `MustMarkVertexNotInTheState()` - adds cost for non-sequencer transactions
- Delta commit/rollback correctly handles attachment cost
- Comprehensive tests verifying incremental == direct calculation

### Tests added
- `TestAttachmentCostBasic` - empty past cone, vertex states
- `TestAttachmentCostSequencerExcluded` - sequencer transactions don't contribute
- `TestAttachmentCostDeltaCommit` / `TestAttachmentCostDeltaRollback` - delta operations
- `TestAttachmentCostMultipleDeltaCycles` - multiple begin/commit/rollback cycles
- `TestAttachmentCostWithRealTransaction` - tests with real transactions (non-zero cost)
- `TestAttachmentCostComplexScenario` - complex multi-delta scenario
- And more (17 tests total)

---

## Implementation Plan

### 1. Replace ledger constant
Files: `ledger/def_constants0.go`, `ledger/lib_singleton.go`, `ledger/constants.go`

- Replace `AttachmentRecursionDepthBase` with `AttachmentCostBudget` in `InitParameters` struct
- Change default value from 10 to ~550-600
- Replace `WithAttachmentRecursionDepthBase()` option with `WithAttachmentCostBudget()`
- Update YAML template: `constAttachmentRecursionDepthBase` -> `constAttachmentCostBudget`
- Update `Constants` struct and loading code

### 2. Update attacher functions
Files: `core/attacher/attacher.go`, `core/attacher/types.go`, `core/attacher/attach.go`

Remove `depth int` parameter from:
- `attachVertexNonBranch(vid *vertex.WrappedTx, depth int)`
- `attachVertexUnwrapped(v *vertex.Vertex, vidUnwrapped *vertex.WrappedTx, depth int)`
- `attachEndorsements(v *vertex.Vertex, vid *vertex.WrappedTx, depth int)`
- `attachEndorsement(v *vertex.Vertex, vidUnwrapped *vertex.WrappedTx, index byte, depth int)`
- `attachEndorsementDependency(vidEndorsed *vertex.WrappedTx, depth int)`
- `attachInput(v *vertex.Vertex, vidUnwrapped *vertex.WrappedTx, inputIdx byte, depth int)`
- `attachInputs(v *vertex.Vertex, vidUnwrapped *vertex.WrappedTx, depth int)`
- `attachOutput(wOut vertex.WrappedOutput, depth int)`

Remove `WithAttachmentDepth` option from `types.go`

Replace depth check (attacher.go:195):
```go
// OLD
if depth > a.AttachmentRecursionDepthBase {
    a.setError(fmt.Errorf("maximum attachment recursion depth %d reached in %s", ...))
    return false
}

// NEW
if a.pastCone.AttachmentCost() > a.AttachmentCostBudget {
    a.setError(fmt.Errorf("attachment cost budget %d exceeded in %s", ...))
    return false
}
```

### 3. Update incremental attacher
File: `core/attacher/attacher_incremental.go`

- Check attachment cost during recursive traversal (same mechanism as milestone attacher)
- Before `CommitDelta()`, verify `pastCone.AttachmentCost()` does not exceed budget
- If exceeded, rollback and return appropriate error

### 4. Update tests
Files: `tests/init.go`, `tests/attach_test.go`

- Replace `WithAttachmentRecursionDepthBase(100)` with `WithAttachmentCostBudget(600)` or similar
- Update test that logs `AttachmentRecursionDepthBase`

---

## Key Code Locations

| Location | Purpose |
|----------|---------|
| `ledger/def_constants0.go:20` | `InitParameters` struct with constant |
| `ledger/def_constants0.go:37` | Default value |
| `ledger/def_constants0.go:133` | YAML template for constant |
| `ledger/lib_singleton.go:429` | `WithAttachmentRecursionDepthBase()` option |
| `ledger/constants.go:61` | `Constants` struct field |
| `ledger/constants.go:132` | Loading constant from EasyFL |
| `core/attacher/attacher.go:195` | Depth check (to be replaced with cost check) |
| `core/attacher/types.go:150` | `WithAttachmentDepth` option (to be removed) |
| `core/vertex/past_cone.go:165` | `AttachmentCost()` method |
| `core/vertex/past_cone.go:180` | `AttachmentCostDirect()` method |
| `core/vertex/past_cone.go:340` | `MustMarkVertexNotInTheState()` adds cost |
