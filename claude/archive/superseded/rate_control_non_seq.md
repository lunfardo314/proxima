# Rate Control: Non-Sequencer Transaction Filtering

## Problem

Non-sequencer transactions make up the bulk of transaction traffic. Most of them are not relevant
to the local sequencer — they target other sequencers on the network. Attaching all of them to the
memDAG wastes CPU (lock parsing, validation), memory (vertex allocation), and creates GC pressure.

The key insight: a non-sequencer transaction is only useful in the memDAG if it will appear in the
past cone of a branch produced by the **local** sequencer. That happens only when the transaction
has a lock targeting this sequencer (chainLock, tagAlong, or delegateLock).

## Strategy (implemented 2025-03-23)

For each non-sequencer transaction arriving at the `nonseq_attach` queue:

- **Pulled transactions always pass** — they are needed for solidification of in-flight attachers.
- **Non-pulled, non-seq transactions**: only pass if they have at least one produced output with
  a lock targeting the local sequencer (`chainLock(seqID)`, `tagAlong(target=seqID, ...)`, or
  `delegateLock(target=seqID, ...)`).
- **If no local sequencer is configured**: all non-pulled non-seq transactions are dropped.
  Access nodes without sequencers don't need non-seq transactions in their memDAG — they only
  need sequencer transactions for tracking the branch DAG.

Dropped transactions remain in **txstore** (they were persisted before reaching the attach queue)
and can be pulled later by any attacher that needs them for solidification.

## Why this is safe

1. **Nothing is lost**: txstore persistence happens before the attach queue gate (in `attachTx()`).
2. **Pulled txs bypass the filter**: solidification pulls are always honored.
3. **Gossip is unaffected**: the transaction is gossiped to peers before reaching the attach queue
   (gossip happens after `ValidatePartialContext` in `txsenders`). Other nodes that need the
   transaction will receive it.
4. **Access nodes benefit most**: without a sequencer, they previously attached all non-seq
   transactions for no reason. Now they only attach what's pulled for solidification.

## Implementation

### `Transaction.HasOutputForSequencer(seqID)` (`ledger/transaction/tx.go`)

Scans all produced outputs, parses each lock, and returns true if any lock targets the given
sequencer chain ID:

| Lock type | Target field |
|-----------|-------------|
| `ChainLock` | `ChainID()` — the chain this output is locked to |
| `TagAlongLock` | `TargetSequencerID` — the sequencer that should consume this output |
| `DelegateLock` | `Target` — the sequencer receiving delegated tokens |

Other lock types (SigLock, etc.) don't target a sequencer and are ignored.

### `nonseq_attach.environment` interface extension

Added `GetOwnSequencerID() *base.ChainID` to the environment interface.
- Returns non-nil `*ChainID` when a local sequencer is running → filter is **active**
- Returns `nil` when no local sequencer (or test environment) → filter is **disabled**

Already implemented by `Workflow` (via `node.GetOwnSequencerID()`).
Test dummy environments return nil with a comment explaining the choice.

### Gate logic in `nonseq_attach.consume()`

```
if !pulled:
    if resource_constrained or snapshotting:
        drop (existing gates)
    if seqID != nil AND tx has no output for local sequencer:
        drop
attach
```

When `seqID` is nil (no sequencer / test env), the sequencer target filter is skipped entirely.
Access nodes without a sequencer still benefit from the resource-based gates (attacher cap,
vertex count, queue length, snapshot).

### Files changed

| File | Change |
|------|--------|
| `ledger/transaction/tx.go` | Added `HasOutputForSequencer(seqID)` method |
| `core/core_modules/nonseq_attach/nonseq_attach.go` | Added `GetOwnSequencerID()` to interface, sequencer target gate |
| `tests/test_util.go` | `GetOwnSequencerID()` returns nil (disables filter in tests) |
| `core/workflow/workflow_test.go` | Same — nil return for test dummy |

## Expected impact

- **Sequencer nodes**: only attach non-seq txs relevant to their own chain. Dramatic reduction in
  memDAG vertex count for non-seq transactions.
- **Access nodes**: effectively stop attaching non-seq txs entirely (no sequencer ID configured).
  Only pulled txs enter the memDAG.
- **Memory**: fewer vertices = less GC pressure, especially under high load.
- **Correctness**: no change to consensus or branch production. Solidification pulls ensure
  all needed transactions are fetched on demand.
