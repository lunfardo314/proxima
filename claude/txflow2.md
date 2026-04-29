# Refactoring of transaction flow

## Initial user input

This next architectural refactoring after learnings gathered while implementing current rate and congestion control.
This is part of the effort to make node be stable under bigger loads and recover after them.


### Related
- [rate_control_non_seq.md](rate_control_non_seq.md)
- [ratecontrol.md](ratecontrol.md)
- [forward_sync_oom.md](forward_sync_oom.md)

### Vision
New architecture will have two input queues to the transaction workflow:

- incoming queue for gossiped transactions received from peering. It shall be based on the tweaked `txinput_queue`. 
- incoming queue for _wanted_ transactions, essentially those pulled from peers and fetched from the `txstore` 

Remove `txsenders` and `nonseq-attach` as separate queued modules, move logic to `txinput_queue`.
The most of the existing rate control logic, including the one related to syncing, shall remain intact.

#### Input queue for wanted transactions
- Propose a better name.
- the purpose to have this queue separate is to have fast track input and serialized queue for important transactions
- it is a core module with elastic input queue for incoming transactions that are either pulled from peers, or fetched from 'txstore'. 
- Accepts raw transactions or already parsed. Raw transactions from `txstore` are parsed (stage 1) here.
- module assumes all transactions at input already passed stage 2 validation (initial parse, signature etc). 
No need to repeat what was once checked in the `txinput_queue`. This is safe because transaction get here only from `txstore` (contains only pre-validated txs) or from `txinput-queue`
- all input transactions are attached. This is consistent with the rate control because these transactions are needed to finish solidification and free resources
- no need for timestamp with wall clock alignment: once pulled transactions are from the past anyway
- when attacher needs a transaction as input and finds it in the `txstore` it just posts the transaction to _wanted_ input queue. 


#### `txinput_queue`
- basic logic of the module remains the same.
- only raw transactions from the gossip are posted here, then transactions is preparsed and gate-checked with the Bloom filter
- _stage 2_ logic and data structure from `txsenders` is moved to `txinput_queue`. Tx is stage-2 validated here. For not-pulled transaction rate-per-sender control is enforced here.
- valid transaction is sent to the `txstore_writer`
- if transactions is `wanted` (pulled) it is passed to the _wanted tx_ queue. Otherwise, it is gossiped to peers
- what have left (unsolicited txs) is subject to the rate control:
   - for non-sequencer transactions:
      - if transaction has output for own sequencer, it is attached (subject to later optimization)
      - otherwise it is dropped. Remains in the `txstore` and will be pulled from there is required
   - for sequencer transactions:
      - branch transactions always attached
      - other sequencer transactions attached only if number of running attachers is below the threshold (static constant, say 30, or say 4 x numberOfCores()).
      - dropped sequencer transactions can be pulled from the `txstore`. Some of them won't get to the tippool and will be orphaned due to limited throughput   
- before attachment wall clock much catch up the ledger time, as it is currently 

#### Other
It may be reasonable to move `attachTx()` logic from `workflow` level to `txinput_queue`. This may be postponed

## Clarifications and refinements

### What problem are we solving?

The current transaction flow has **5 serialized queued modules** between receiving bytes and attachment:

```
P2P/API → txinput_queue → txsenders → attachTx() → seq_attach / nonseq_attach → _attach()
```

Each queue boundary adds latency, goroutine overhead, and complexity. The split happened organically
as rate control was layered on. Now that the rate control logic is well-understood, we can
consolidate without losing any protection.

Key inefficiencies in the current flow:
1. **txsenders is a separate queue** that only does signature validation, holder-known-in-LRB check,
   per-sender pace control, then calls `attachAndGossip`. All of this can run inline in `txinput_queue`.
2. **nonseq_attach is a separate queue** that re-checks the same counters (`att`, `nonseq`, `IsSnapshotting`)
   that `txinput_queue` could check directly. Its only unique logic is the sequencer-target filter.
3. **Pulled/wanted transactions take the same path as gossip**, passing through dedup, signature
   validation, and rate control — all of which are redundant for transactions from `txstore`.

### Naming: `txsolicit_queue` for the wanted queue

The wanted queue accepts _solicited_ transactions — those explicitly requested for solidification.
Proposed name: **`txsolicit_queue`** (or `solicited_queue`). Alternatives: `txwanted_queue`, `txpull_queue`.

### Refined architecture

```
                    P2P gossip / API
                         │
                    txinput_queue
                    (dedup, parse, stage-2, sender pace,
                     gossip, persist, rate-control gate)
                         │
              ┌──────────┴──────────┐
              │                     │
         attach directly       drop (in txstore)
         (seq + non-seq)
              │
              ▼
         _attach() → AttachTransaction
              │
              │ (attacher needs missing input)
              │
              ▼
     ┌─── txstore lookup ───┐
     │                      │
   found                  not found
     │                      │
     ▼                      ▼
 txsolicit_queue       pull from peers
 (parse if raw,            │
  attach all,          txsolicit_queue
  no rate control)     (on arrival)
     │
     ▼
 _attach() → AttachTransaction
```

Two queues, two paths:

| | `txinput_queue` (gossip path) | `txsolicit_queue` (wanted path) |
|---|---|---|
| **Source** | P2P gossip, API | txstore fetch, peer pull response |
| **Input** | Raw bytes only | Raw bytes or parsed `*transaction.Transaction` |
| **Dedup** | inGate (TTL map) | Not needed (only solicited txs enter) |
| **Stage 1** | Parse here | Parse here if raw bytes |
| **Stage 2** | Signature + holder check + sender pace | Skip (already validated before entering txstore) |
| **Persist** | To txstore_writer | Skip (already in txstore) |
| **Gossip** | Yes (non-pulled, after validation) | No |
| **Rate control** | Full: sequencer-target filter, attacher cap, vertex limits | None — all solicited txs are attached |
| **Clock alignment** | Yes — future txs wait | No — solicited txs are always past |
| **Attach** | Directly (no intermediate queue) | Directly |

### What moves where

| Current module | Fate | Logic destination |
|---|---|---|
| `txsenders` | **Removed** | Stage-2 validation (signature, holder-in-LRB, sender pace) moves into `txinput_queue.consume()` |
| `nonseq_attach` | **Removed** | Sequencer-target filter and resource gates move into `txinput_queue` attach decision |
| `seq_attach` | **Removed** | Attacher cap + deadlock-prevention logic moves into `txinput_queue` attach decision |
| `attachTx()` in workflow | **Simplified** | `ValidatePartialContext` + persist + time-bounds remain in `txinput_queue`. The `pushToAttachQueue` indirection is eliminated |
| `pull.go` in attacher | **Modified** | Instead of calling `TxBytesFromStoreIn` (which re-enters `txinput_queue`), posts to `txsolicit_queue` |

### Preserved rate control logic

All existing rate control decisions are preserved, just executed in fewer places:

1. **Dedup gate** (inGate) — stays in `txinput_queue`, unchanged
2. **Per-sender pace** (ring buffer) — moves from `txsenders` into `txinput_queue`
3. **Holder-known-in-LRB** — moves from `txsenders` into `txinput_queue`
4. **Attacher cap** — moves from `seq_attach` into `txinput_queue` gate decision
5. **Non-seq vertex limit** — moves from `nonseq_attach` into `txinput_queue` gate decision (likely will need revisiting after load testing)
6. **Sequencer-target filter** — moves from `nonseq_attach` into `txinput_queue` gate decision
7. **Snapshot load shedding** — moves from `nonseq_attach` into `txinput_queue` gate decision
8. **Clock alignment** — enforced right before attachment (not during gossip processing). Gossip path should not assume wall clock alignment; the check belongs at the attach boundary

### What changes for the sequencer's own milestones

Own sequencer transactions continue to flow through the full `txinput_queue` pipeline — signature
checks, `ValidatePartialContext`, dedup, gossip — the same as any other transaction. This is
**intentional**: it serves as a safety net to catch invalid transactions from sequencer bugs.
The overhead is negligible. No special path is needed.

### Safety argument

- **Nothing is lost**: txstore persistence happens before any drop decision (unchanged).
- **Pulled txs bypass rate control**: `txsolicit_queue` has no gates at all.
- **Gossip unaffected**: gossip happens in `txinput_queue` after stage-2, before the attach gate.
  Dropped transactions are still gossiped to peers.
- **Deadlock prevention**: the `seq_attach` timestamp-ordering logic for deadlock prevention
  (only attach older-than-latest when at cap) is preserved in `txinput_queue`.
- **Syncing**: `seq_attach` currently disables its cap when `!IsSynced()`. Same condition applies.

### System-level control (future topic)

The `txinput_queue` itself may need an admission cap driven by memory pressure, total vertex count,
and overall transaction pipeline load. This is a separate topic to be designed after the
refactoring is complete and tested under load.

### Resolved questions

1. **`txsolicit_queue` backpressure**: Not needed. Forward-sync pulls are batch-capped and pace-constrained.
   Current-time-zone pulling is depth-capped during syncing. Existing caps are sufficient.

2. **Move `attachTx()` into `txinput_queue`**: Yes — confirmed.

3. **Own sequencer milestones**: Continue routing through `txinput_queue` like any other transaction.
   Intentional: prevents invalid transactions from own sequencer bugs from propagating.
---

## Implementation plan

### Phase 0: Preparation (no behavior change)

**0.1** Move `txsenders` data structures into `txinput_queue`
- Copy `seenTimestamps`, `tsRingBuffer`, sender map, cleanup logic into `txinput_queue`
- `txinput_queue` gains `txSenders map[base.HolderID]*seenTimestamps` field
- Wire `isHolderKnownInLRB` into `txinput_queue`'s environment interface
- Keep `txsenders` module alive but unused (verify build)

**0.2** Move `attachAndGossip` logic into `txinput_queue`
- After stage-2 validation in `txinput_queue`, call `GossipTxBytesToPeers` directly for non-pulled txs
- Call `attachTx` directly (or inline its logic) for all txs that pass
- `txsenders` module is now dead code

**0.3** Inline `attachTx` logic into `txinput_queue.consume()`
- `ValidatePartialContext`, persist to txstore, time-bounds check — all move into `txinput_queue`
- The `attachTx` function in `workflow/txinput.go` becomes a thin wrapper (or is removed)

### Phase 1: Consolidate attach queues into `txinput_queue`

**1.1** Move attach gate logic from `seq_attach` and `nonseq_attach` into `txinput_queue`
- After stage-2 + persist + gossip, `txinput_queue` makes the attach/drop decision:
  - **Pulled?** → attach (no gates)
  - **Sequencer tx?** → attach if `att < maxConcurrentAttachers` (with deadlock-prevention ordering)
  - **Branch tx?** → always attach
  - **Non-seq tx targeting own sequencer?** → attach if `nonseq < maxNonSeqVertices`
  - **Non-seq tx not targeting own sequencer?** → drop (stays in txstore)
  - **Snapshotting?** → drop non-pulled
- Call `_attach()` directly from `txinput_queue`

**1.2** Remove `txsenders`, `nonseq_attach`, `seq_attach` modules
- Delete the three module directories
- Remove from workflow initialization
- Update environment interfaces

**1.3** Simplify `workflow/txinput.go`
- `TxBytesInFromPeerQueued` and `TxBytesInFromAPIQueued` remain as entry points
- `attachTx`, `pushToAttachQueue`, and option types are removed
- `_attach` remains as the function that calls `attacher.AttachTransaction`

### Phase 2: Create `txsolicit_queue`

**2.1** Create `core/core_modules/txsolicit_queue/` module
- Input type: `{ TxBytes []byte, Tx *transaction.Transaction, Meta *txmetadata.TransactionMetadata }`
  - Either `TxBytes` or `Tx` is set (raw from txstore, or already parsed)
- `consume()`: parse if raw, then call `_attach()` directly — no gates, no dedup, no gossip
- Standard `CoreModule[Input]` with elastic queue

**2.2** Wire `txsolicit_queue` into the workflow
- `workflow.TxBytesFromStoreIn` → posts to `txsolicit_queue` instead of calling `TxBytesIn`
- Attacher `pullIfNeeded` → when found in txstore, posts to `txsolicit_queue`
- Pull responses from peers → `txinput_queue` marks as pulled → after stage-2, forwards to `txsolicit_queue` (skipping the attach gate)

### Phase 3: Cleanup and testing

**3.1** Remove dead code
- Remove `TxBytesIn`, `WithMetadata`, `WithSourceType`, `WithPeerMetadata` option functions
- Remove `TxInOption` type if no longer needed
- Simplify `txmetadata.SourceType` (pulled vs txstore distinction may collapse)

**3.2** Update tests
- `core/workflow/workflow_test.go` — update dummy environment interfaces
- `tests/` integration tests — verify no behavior change
- New unit tests for consolidated `txinput_queue` (sender pace, attach gate decisions)

**3.3** Update documentation
- Update flow diagrams in `ratecontrol.md`
- Update this document with implementation notes

### Risks and mitigations

| Risk | Mitigation |
|---|---|
| `txinput_queue` becomes too large/complex | Keep clear internal structure: dedup → parse → stage-2 → persist+gossip → gate → attach. Each step is a private method. |
| Single queue bottleneck for gossip + attach | `txinput_queue` already processes gossip serially. Attach was always called from a single consumer. No throughput regression. |
| Regression in rate control | Each gate condition is preserved 1:1. Unit test each gate in isolation before removing old modules. |
| `txsolicit_queue` flooding during forward-sync | Not a concern: forward-sync pulls are batch-capped and pace-constrained; current-time pulling is depth-capped. Add queue length counter for monitoring. |