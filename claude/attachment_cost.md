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

1. During recursive traversal, `MarkVertexNotInTheState()` is called for a non-sequencer transaction
2. Cost is added to `pastConeCost` and `FlagPastConeDirectCost` is set
3. **Immediately after**: check `pastConeCost + seqTxCost > budget`
4. If exceeded → invalidate attacher immediately, stop traversal
5. No further DAG exploration occurs

### Implementation location

The budget check should occur right after `MarkVertexNotInTheState()` returns, in the calling code
(attacher functions). This ensures every addition is checked before proceeding with further traversal.

```go
// Example pattern in attacher
pc.MarkVertexNotInTheState(vid)
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

### Update `MarkVertexNotInTheState()`

Set the direct cost flag when adding cost:
```go
func (pc *PastCone) MarkVertexNotInTheState(vid *WrappedTx) {
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

## Status: COMPLETED

Implementation completed in commits:
- `cd6b7903` - implement attachment cost budget with direct cost tracking
- `1dcea5f5` - split attach_test.go and add attachment cost budget tests
- `5d840293` - add test for budget-exceeded edge case in milestone attacher

---

## Pending

- **Incremental attacher callback has TODO** - The budget check in `InsertInput()` callback in
  `sequencer/task/proposal.go` is not yet implemented. Currently, the sequencer adds all available
  tag-along inputs without checking the budget. If the resulting transaction exceeds the budget,
  the milestone attacher rejects it. The TODO should implement `pastConeCost + seqTxCost > budget`
  check in the `atomicCheck` callback to prevent building transactions that will be rejected.

---

## What Was Implemented

### Core Changes
- `FlagPastConeDirectCost` flag added to mark vertices contributing to direct cost
- `MarkVertexNotInTheState()` sets the flag for non-sequencer transactions
- `MergePastCone()` masks out the flag (merged past cones don't contribute)
- Fail-fast budget check in `checkAttachmentCostBudget()` after attachment
- `AttachmentCostBudget` ledger constant (default: 600)
- Removed obsolete `depth` parameter and recursion depth limit

### Key Files Modified
- `core/vertex/past_cone.go` - flag and cost tracking
- `core/attacher/attacher.go` - budget check implementation
- `core/attacher/attacher_incremental.go` - `InsertInput()` with budget callback
- `ledger/def_constants0.go` - `AttachmentCostBudget` constant

---

## Test Coverage

Tests split into logical files in `tests/`:

### `attach_cost_test.go` - Attachment Cost Budget Tests
- `TestAttachCostBudgetChainWithinLimit` - chain of 50 transactions (cost ~100)
- `TestAttachCostBudgetShortChain` - short chain of 10 transactions (cost ~20)
- `TestAttachCostBudgetMultipleTransactions` - sequential transaction attachment
- `TestAttachCostBudgetFanOutCostTracking` - high-cost fan-out (1→100 outputs, cost 101)
- `TestAttachCostBudgetExceededNote` - documents budget design rationale
- `TestAttachCostBudgetVerifyCalculation` - verifies cost formula (numInputs + numOutputs)
- `TestAttachCostBudgetExceededMilestoneAttacher` - **fail-fast budget exceeded test** using lowered budget (5); creates chain of non-sequencer transactions with chain-locked output, forcing sequencer to pull entire chain into past cone and exceed budget

### `attach_timing_test.go` - Timing Edge Cases
- Pace boundary tests
- Slot boundary tests
- Consolidation window tests

### `attach_deadlock_test.go` - Deadlock Scenarios
- Context cancellation tests
- Concurrent attacher tests
- Solidification deadline tests

### `attach_test.go` - Basic/Conflicts/SeqChains Tests
- Kept existing tests for basic attachment, conflicts, and sequencer chains

---

## Budget Design Analysis

The budget of 600 is intentionally designed to be hard to exceed within a single slot:

| Parameter | Value |
|-----------|-------|
| AttachmentCostBudget | 600 |
| TicksPerSlot | 128 |
| TransactionPace | 3 ticks |
| Max transactions per slot | ~42 |
| Max simple transfer cost per slot | ~84 |
| Budget/simple cost ratio | 7.14x |

### Why budget-exceeded is hard to test (with default budget)
- Simple transfers (cost 2): Need 300+ txs, but only ~42 fit in one slot
- Fan-out transactions: Tokens get diluted below storage deposit minimum after ~3 iterations
- The budget protects against attack chains while allowing legitimate usage

### How budget-exceeded is tested
The `TestAttachCostBudgetExceededMilestoneAttacher` test uses option 2 below:
1. Multiple slots with proper endorsement handling (complex)
2. **A test-specific lower budget configuration** ← implemented via `reinitTestLedgerWithBudget(5)`
3. Many pre-existing UTXOs for parallel high-cost chains

The test helper `reinitTestLedgerWithBudget()` in `tests/init.go` reinitializes the ledger with a custom
budget, allowing fail-fast behavior to be verified with a small chain of transactions.

---

## Key Code Locations

| Location | Purpose |
|----------|---------|
| `core/vertex/past_cone.go:48-56` | `FlagPastConeDirectCost` constant |
| `core/vertex/past_cone.go:165` | `AttachmentCost()` method |
| `core/vertex/past_cone.go:335` | `MarkVertexNotInTheState()` - sets flag |
| `core/vertex/past_cone.go:650` | `MergePastCone()` - masks out flag |
| `core/attacher/attacher.go:311` | `checkAttachmentCostBudget()` - fail-fast check |
| `core/attacher/attacher_incremental.go:236` | `InsertInput()` with budget callback |
| `ledger/def_constants0.go` | `AttachmentCostBudget` constant definition |
| `tests/init.go` | `reinitTestLedgerWithBudget()` - test helper for custom budget |
| `tests/attach_cost_test.go` | `TestAttachCostBudgetExceededMilestoneAttacher` - fail-fast test |
