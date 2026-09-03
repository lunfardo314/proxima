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

### Enforcement invariants (both attacher types)

The enforcement is **identical** for the milestone and incremental attachers — it lives in the shared
`attacher` type (`checkAttachmentCostBudget`), so the same code decides "within budget" or "exceeded" for
every node:

1. **Equal logic.** The check is always `pastConeCost + seqTxCost > effectiveBudget`. Both attachers compute
   `pastConeCost` the same way (directly-reachable non-seq txs) and set `seqTxCost` to the cost of the
   sequencer tx being attached/built. The milestone attacher sets it once (from the finished tx); the
   incremental attacher sets it before each optional input's descent to the builder cost after applying that
   input (`SeqTxBuilder.AttachmentCost() + cmd.AttachmentCostDelta()`), so the early check sees the same total
   the milestone attacher will later compute for the finished tx.
2. **Early detection.** The check runs in `refreshDependencyStatus`, the single choke point every dependency
   (input / endorsement / extended output) passes through, immediately after that dependency's cost is added
   in `MarkVertexNotInTheState`. So it fails fast at the exact traversal step the addition crosses the budget —
   never a post-walk check.
3. **Determinism.** `pastConeCost` and `seqTxCost` are pure functions of the transaction and its baseline, so
   the verdict is identical on every node. `effectiveBudget` for the *milestone* attacher is always the ledger
   constant, so consensus-wide validity is deterministic.
4. **Only the reaction differs.** On `ErrAttachmentBudgetExceeded`: the milestone attacher marks the
   transaction Bad (invalid on all nodes); the incremental attacher rolls the just-added input's delta (past
   cone + seqTxCost) back to the previous error-free state and skips that input, retrying it on a later tick.
5. **Throttling via reduced budget.** The sequencer may enforce a budget ≤ the ledger constant
   (`SetEffectiveCostBudget`): the tag-along phase uses a pressure-scaled fraction (`2/3 × constant` at full
   pressure), delegations use the full constant. Because the incremental attacher only ever accepts a tx whose
   total ≤ its (≤ constant) budget, the milestone attacher — checking against the full constant — always accepts it.

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

* Adds tag-along / delegation inputs one by one via `InsertInput()`.
* Each `InsertInput()` call uses the past-cone delta pattern for atomicity.
* **Signature**: `InsertInput(wOut, seqTxCost int, atomicCheck func() (bool, error))`.
* The caller parses the input's command first (pure, lock-free), computes
  `seqTxCost = SeqTxBuilder.AttachmentCost() + cmd.AttachmentCostDelta()`, and passes it in. `InsertInput`
  installs it as the attacher's `seqTxCost` before the descent, so the **shared** `checkAttachmentCostBudget`
  enforces the same `pastConeCost + seqTxCost > effectiveBudget` the milestone attacher uses — there is no
  separate budget arithmetic in the sequencer layer.
* `atomicCheck` only *applies* the command (`cmd.Apply` / `FreezeDelegation`); the budget is enforced during
  the descent, not inside the callback.
* On budget-exceeded (or any other descent error) the delta and `seqTxCost` are rolled back and the input is
  skipped (retried later). The resulting transaction always has total attachment cost ≤ the effective budget.

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

### `InsertInput()` signature

```go
// InsertInput inserts a tag-along or delegation input.
// seqTxCost is the builder cost AFTER applying this input; it is installed as the attacher's seqTxCost so the
// shared budget check enforces the same total during the descent. atomicCheck only applies the command.
func (a *IncrementalAttacher) InsertInput(wOut vertex.WrappedOutput, seqTxCost int,
    atomicCheck func() (bool, error)) (valid bool, err error)
```

### Effective-budget throttling

```go
// lower the incremental attacher's effective budget below the ledger constant (never above)
func (a *IncrementalAttacher) SetEffectiveCostBudget(budget int)
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

## Unified enforcement (resolved)

The earlier split — incremental attacher used `seqTxCost = 0` in the shared check and did a *separate*,
post-walk budget check (with a `2/3` fraction) inside the `proposal.go` `atomicCheck` callback — has been
removed. Enforcement is now unified in the shared `attacher` per the invariants above:

- `checkAttachmentCostBudget` enforces `pastConeCost + seqTxCost > effectiveBudget` for **both** attacher
  types, fired early in `refreshDependencyStatus`; the error is the named `ErrAttachmentBudgetExceeded`.
- The incremental attacher receives a real `seqTxCost` per input (`InsertInput(wOut, seqTxCost, …)`) and a
  reduced effective budget per phase (`SetEffectiveCostBudget`) — the only two things that differ from the
  milestone attacher are the budget *value* and the *reaction* (rollback vs. mark-Bad).
- Removed a double-count bug in the delegation path (`pastConeCost` was added twice).

Key files: `core/attacher/attacher.go` (`checkAttachmentCostBudget`), `core/attacher/types.go`
(`costBudget`, `ErrAttachmentBudgetExceeded`), `core/attacher/attacher_incremental.go` (`InsertInput`,
`SetEffectiveCostBudget`), `sequencer/task/proposal.go` (`insertTagAlongInputs`, `insertDelegations`).

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
