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
We introduce a new ledger constant `constAttachmentCostBudget` that replaces the depth constant.

### Definitions

* **Attachment cost of a transaction** = `numInputs + numProducedOutputs`. It roughly approximates the validation cost.

* **Directly reachable transaction** = a non-sequencer transaction that can be reached in the DAG from the attacher's
  sequencer transaction without passing through any other sequencer transaction. In other words, only transactions
  that are directly attached by this sequencer count. Transactions merged from other attachers' past cones via
  `MergePastCone()` do NOT count (unless they were already directly reachable).

* **Past cone attachment cost** = sum of attachment costs of all directly reachable non-sequencer transactions
  that are not in the baseline state. Tracked incrementally via `FlagPastConeDirectCost` flag.

* **Sequencer transaction cost** = attachment cost of the sequencer transaction being built/attached.
  For incremental attacher: computed from `SeqTxBuilder` state (`len(ConsumedOutputs) + len(Outputs) + baseOutputs`).
  For milestone attacher: `tx.NumInputs() + tx.NumProducedOutputs()`.

* **Total attachment cost** = `pastConeCost + seqTxCost`. This is what gets checked against the budget.

### Why include sequencer transaction cost?

Tag-along outputs can contain commands that result in different numbers of additional inputs/outputs:

| Command | Additional Inputs | Additional Outputs |
|---------|------------------|-------------------|
| Noop (simple tag-along) | +1 | 0 |
| WithdrawFromSeq | +1 | +1 |
| FreezeDelegation | +1 | +1 |
| AskStopDelegation | +2 | +1 |

Future request types may have different counts. By including sequencer tx cost in the total, heavy sequencer
transactions (many inputs/outputs) have less budget remaining for tag-along past cones.

---

## Behavior

### Milestone attacher

* During attachment, the `pastCone` is filled step by step with transactions.
* When a **non-sequencer** transaction is determined to be **not in the baseline state** AND is **directly reachable**,
  the `FlagPastConeDirectCost` flag is set and its attachment cost is added to `pastConeCost`.
* Transactions merged via `MergePastCone()` do NOT get `FlagPastConeDirectCost` set (it's masked out during merge).
* Budget check: `pastConeCost + seqTxCost > budget` → attacher invalidated, transaction marked Bad.
* `seqTxCost` is obtained from the transaction being attached: `tx.NumInputs() + tx.NumProducedOutputs()`.

The process must be strictly deterministic: transaction is valid on all nodes, or invalid on all nodes.

### Incremental attacher

* Adds tag-along outputs one by one via `InsertInput()`.
* Each `InsertInput()` call uses delta pattern for atomicity.
* **New signature**: `InsertInput(wOut, seqTxCost int, atomicCheck func(pastConeCost, seqTxCost int) (bool, error))`
* The `seqTxCost` is computed by the sequencer from `SeqTxBuilder` state before calling `InsertInput()`.
* The `atomicCheck` callback receives both costs and checks: `pastConeCost + seqTxCost > budget`.
* If budget exceeded, delta is rolled back automatically.
* The resulting transaction always has total attachment cost below the budget.

---

## Fail-fast budget checking

**Critical requirement**: The budget check must happen **immediately** after each non-sequencer transaction
is added to the past cone during recursive DAG traversal. The attacher must be invalidated right away if
the budget is exceeded.

### Why fail-fast is essential

Without fail-fast checking, an attacker could construct a malicious past cone that:
1. Forces the node to traverse the entire (potentially huge) past cone
2. Only discovers the budget was exceeded at the very end
3. Wastes significant computational resources before failing

### Fail-fast flow

1. During recursive traversal, `MustMarkVertexNotInTheState()` is called for a non-sequencer transaction
2. Cost is added to `pastConeCost` and `FlagPastConeDirectCost` is set
3. **Immediately after**: check `pastConeCost + seqTxCost > budget`
4. If exceeded → invalidate attacher immediately, stop traversal
5. No further DAG exploration occurs

### Implementation location

The budget check should occur right after `MustMarkVertexNotInTheState()` returns, in the calling code
(attacher functions). This ensures every addition is checked before proceeding with further traversal.

```go
// Example pattern in attacher
pc.MustMarkVertexNotInTheState(vid)
if pc.AttachmentCost() + seqTxCost > budget {
    a.setError(fmt.Errorf("attachment cost budget exceeded"))
    return false  // Stop immediately
}
// Continue only if within budget
```

---

## Implementation Details

### New flag: `FlagPastConeDirectCost`

Add to `past_cone.go`:
```go
FlagPastConeDirectCost = FlagsPastCone(0b10000000) // vertex contributes to direct attachment cost
```

### Update `MustMarkVertexNotInTheState()`

Set the direct cost flag when adding cost:
```go
func (pc *PastCone) MustMarkVertexNotInTheState(vid *WrappedTx) {
    pc.Assertf(!pc.IsInTheState(vid), "!pc.IsInTheState(vid)")
    pc.SetFlagsUp(vid, FlagPastConeVertexKnown|FlagPastConeVertexCheckedInTheState)
    pc.Assertf(pc.isNotInTheState(vid), "pc.isNotInTheState(vid)")
    if !vid.IsSequencerTransaction() {
        pc.addToAttachmentCost(vid.AttachmentCost())
        pc.SetFlagsUp(vid, FlagPastConeDirectCost)  // mark as directly contributing
    }
}
```

### Update `MergePastCone()`

Mask out `FlagPastConeDirectCost` when merging (line ~688):
```go
// Merged transactions don't contribute to direct cost unless already flagged
pc.markVertexWithFlags(vid, flags & ^FlagPastConeVertexAskedForPoke & ^FlagPastConeDirectCost)
```

### Update `AttachmentCostDirect()`

Only count transactions with the direct cost flag:
```go
func (pc *PastCone) AttachmentCostDirect() (ret int) {
    pc.forAllVertices(func(vid *WrappedTx) bool {
        if pc.Flags(vid).FlagsUp(FlagPastConeDirectCost) {
            ret += vid.AttachmentCost()
        }
        return true
    })
    return
}
```

### Update `InsertInput()` signature

```go
// InsertInput inserts tag along or delegation input.
// seqTxCost is the current cost of the sequencer transaction being built.
// atomicCheck callback receives pastConeCost and seqTxCost to verify budget.
func (a *IncrementalAttacher) InsertInput(wOut vertex.WrappedOutput, seqTxCost int,
    atomicCheck func(pastConeCost, seqTxCost int) (bool, error)) (valid bool, err error)
```

### Milestone attacher budget check

Replace depth check with cost check (fail-fast after each addition):
```go
// Check total attachment cost (past cone + sequencer tx)
seqTxCost := a.tip.AttachmentCost()  // or however the seq tx is accessed
if a.pastCone.AttachmentCost() + seqTxCost > a.AttachmentCostBudget {
    a.setError(fmt.Errorf("attachment cost budget %d exceeded (pastCone=%d, seqTx=%d) in %s",
        a.AttachmentCostBudget, a.pastCone.AttachmentCost(), seqTxCost, vid.IDShortString()))
    return false
}
```

---

## Clarifications

### Budget value
The `constAttachmentCostBudget` should be approximately **550-600**. This allows for one maximum-sized tag-along
transaction (256 inputs + 256 outputs = 512) in the past cone of a sequencer transaction with some headroom.

### What to remove
- The `depth int` parameter passed through recursive attacher functions
- The `WithAttachmentDepth` option
- The ledger constant `constAttachmentRecursionDepthBase` (replaced with `constAttachmentCostBudget`)

### What to add
- `FlagPastConeDirectCost` flag
- `WithAttachmentCostBudget` option (replacing `WithAttachmentRecursionDepthBase`)
- `seqTxCost` parameter to `InsertInput()`

### What NOT to touch
- `attachmentDepth` field in `WrappedTx` (types.go:64) - used for syncing, misleading name
- `SetAttachmentDepthNoLock()` and `GetAttachmentDepthNoLock()` functions - used for syncing

### Budget enforcement
- When budget is exceeded, transaction is marked as `Bad` with error message
- In practice, this should rarely happen because sequencers use incremental attacher
- The enforcement is a safety mechanism against malicious attacks

---

## Current Progress

### Already implemented (commit 1e011cbc)
- `AttachmentCost()` method on `PastCone` - returns incremental attachment cost
- `AttachmentCostDirect()` method on `PastCone` - calculates by iterating all vertices (needs update for flag)
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

**Note**: Existing tests will need updates for `FlagPastConeDirectCost` behavior.

---

## Implementation Plan

### 1. Add FlagPastConeDirectCost and update PastCone
Files: `core/vertex/past_cone.go`

- Add `FlagPastConeDirectCost` constant
- Update `MustMarkVertexNotInTheState()` to set the flag
- Update `MergePastCone()` to mask out the flag
- Update `AttachmentCostDirect()` to only count flagged vertices
- Update `String()` method for flag display

### 2. Update InsertInput signature
Files: `core/attacher/attacher_incremental.go`

- Add `seqTxCost int` parameter
- Update `atomicCheck` callback signature to `func(pastConeCost, seqTxCost int) (bool, error)`
- Pass both costs to the callback

### 3. Replace ledger constant
Files: `ledger/def_constants0.go`, `ledger/lib_singleton.go`, `ledger/constants.go`

- Replace `AttachmentRecursionDepthBase` with `AttachmentCostBudget`
- Change default value from 10 to ~550-600
- Replace `WithAttachmentRecursionDepthBase()` with `WithAttachmentCostBudget()`
- Update YAML template

### 4. Update attacher functions
Files: `core/attacher/attacher.go`, `core/attacher/types.go`, `core/attacher/attach.go`

- Remove `depth int` parameter from recursive functions
- Remove `WithAttachmentDepth` option
- Add fail-fast budget check after each `MustMarkVertexNotInTheState()` call
- Use total cost formula: `pastConeCost + seqTxCost`

### 5. Update sequencer to pass seqTxCost
Files: `sequencer/` (find callers of InsertInput)

- Compute seqTxCost from SeqTxBuilder state
- Pass to InsertInput calls

### 6. Update tests
Files: `core/vertex/past_cone_test.go`, `tests/init.go`, `tests/attach_test.go`

- Add tests for FlagPastConeDirectCost behavior
- Add tests for merge not setting the flag
- Add tests for fail-fast behavior
- Update existing tests for new InsertInput signature
- Replace `WithAttachmentRecursionDepthBase` with `WithAttachmentCostBudget`

---

## Key Code Locations

| Location | Purpose |
|----------|---------|
| `core/vertex/past_cone.go:48-56` | Flag constants (add FlagPastConeDirectCost) |
| `core/vertex/past_cone.go:165` | `AttachmentCost()` method |
| `core/vertex/past_cone.go:180` | `AttachmentCostDirect()` method (update for flag) |
| `core/vertex/past_cone.go:335` | `MustMarkVertexNotInTheState()` (add flag setting) |
| `core/vertex/past_cone.go:650` | `MergePastCone()` (mask out flag) |
| `core/attacher/attacher_incremental.go:236` | `InsertInput()` (update signature) |
| `core/attacher/attacher.go:195` | Depth check (replace with fail-fast cost check) |
| `ledger/def_constants0.go:20` | `InitParameters` struct |
| `ledger/lib_singleton.go:429` | `WithAttachmentRecursionDepthBase()` option |
| `sequencer/txbuilder_seq/txbuilder_seq.go` | SeqTxBuilder for computing seqTxCost |
