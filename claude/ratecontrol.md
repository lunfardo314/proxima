# Rate and congestion controls 

## Current

Referring to [crash.md](crash.md) and also other cases, we need to implement infrastructure in the node that helps keep
the node more resilient to high loads. 

## Goal

We want to increase resilience of the node and of the network to high load and resource starvation situations. 

To implement global structure and interface(s) available across the node components as part of the `NodeGlobal`.

The structure collects information about relevant global parameters, relevant to the rate controls, such as:
- number of attacher goroutines
- sizes of input queue, txsenders queue, and others
- sizes of maps such as vertices, txinput Bloom filter map, txsenders maps, others  

The collection of the global interface calls such as `EvidenceAttacherCounte(ind/dec bool)` or similar will be used to maintain 
global atomic parameters.

A collection of advisory calls like `MaxAttachersReached() bool` will be used by components to make decisions about weather proceed, block or discard
certain operations.

Both interfaces and internal rules represent rate control statistics, dynamics and heuristics will be adjusted empirically as part of the interface implementation.

## Backpressure to the input load

Initially, we shall implement:

- collecting number of vertices in the memDAG
- collecting the number of running active attacher goroutines. 
- collecting sizes of tx input queues
- the call `AttacherLimitReached() bool` would signal that caller better wait (block) before spawning new attachments
- that means size of downstream queues will start growing. We shall put limits on them through similar mechanism
- at the very input we shall implement smart dropping strategy (heuristics) for incoming transactions when rate limits are reached.
E.g. all pulled (wanted) transactions must be let in, other may be dropped. 

We shall not modify current logic of pass/drop implemented in the txinput and txsenders (unless it is flawed).

## Phase 1 Implementation (Session 2025-03-19)

### Architecture: Split Transaction Flow

The transaction flow is split into two separate queues after validation, based on transaction type:

```
P2P / API
   ↓
txinput_queue (dedup, parse)
   ↓
txsenders (sender validation, pace check, gossip)
   ↓
attachTx() — ValidatePartialContext, persist to txstore
   ↓
pushToAttachQueue() — routes by txid.IsSequencerTransaction()
   ├─────────────────────────────────┐
   ↓                                 ↓
seq_attach queue                nonseq_attach queue
(blocks non-pulled when         (drops non-pulled when
 att >= 1000 attachers)          nonseq >= 5000 vertices)
   ↓                                 ↓
_attach → AttachTransaction     _attach → AttachTransaction
(spawns attacher goroutine)     (adds to memDAG, no goroutine)
```

### Key design decisions

**Why two queues, not one:**
- Sequencer and non-sequencer transactions have different resource profiles.
  Sequencer txs spawn attacher goroutines (expensive, hold past cone references).
  Non-sequencer txs only add vertices to memDAG (cheap per-tx, but unbounded accumulation).
- Different backpressure policies: sequencer queue blocks (attachers are valuable),
  non-sequencer queue drops (tx is already in txstore, can be pulled later).

**Why the gate is after validation, not before:**
- Gossip must happen after `ValidatePartialContext` to avoid propagating invalid transactions.
- The tx is persisted to txstore before reaching the gate, so dropped non-seq transactions
  can be pulled later when an attacher needs them for solidification.

**Pulled (wanted) transactions always pass:**
- Pulled transactions are needed for solidification. Blocking them would cause attacher deadlocks.
- They are pushed with queue priority (front of queue) for faster processing.

### Counters and metrics

All metrics use the existing `Counter`/`IncCounter`/`DecCounter`/`SetCounter` infrastructure in `NodeGlobal`:

| Counter | Incremented | Decremented | Purpose |
|---------|-------------|-------------|---------|
| `att` | attacher goroutine start | attacher goroutine finish | concurrent attacher count |
| `nonseq` | non-seq vertex attached | vertex deleted or detached in GC | non-seq vertex count in memDAG |
| `nonseq_drop` | non-seq tx dropped by gate | — | dropped tx count (monotonic) |
| `seq_attach_q` | set by queue callback | set by queue callback | seq attach queue length |
| `nonseq_attach_q` | set by queue callback | set by queue callback | nonseq attach queue length |

Queue length reporting uses `SetCounter` with an `OnLenChange` callback on the elastic queue,
invoked only when the length actually changes.

### Thresholds

| Parameter | Value | Rationale |
|-----------|-------|-----------|
| `maxConcurrentAttachers` | 1000 | Normal operation: 0-4 attachers. Crash had 27 at 6.9GB. 1000 is a safety ceiling. |
| `maxNonSeqVertices` | 5000 | Normal operation: ~500 vertices. Crash had 3425. 5000 allows headroom before OOM. |

### Files

| File | Change |
|------|--------|
| `global/types.go` | Added `SetCounter(name, value)` to `Logging` interface |
| `global/global.go` | Implemented `SetCounter` |
| `util/queue/queue.go` | Added `OnLenChange` callback (not thread-safe, set before Start) |
| `core/core_modules/seq_attach/seq_attach.go` | New module: sequencer tx attach queue |
| `core/core_modules/nonseq_attach/nonseq_attach.go` | New module: non-seq tx attach queue |
| `core/workflow/workflow.go` | Wired both new modules |
| `core/workflow/txinput.go` | Routes via `pushToAttachQueue()` instead of direct `_attach()` |
| `core/attacher/attach.go` | Increments `nonseq` counter on new non-seq vertex |
| `core/memdag/memdag.go` | Decrements `nonseq` counter on vertex delete/detach |

## Phase 2 (future)

There's one special situation: syncing.

When snapshot state is far away, more than 1000 slots back, syncing process creates a lot of attachers that are recursively waiting
for their dependencies to finish the work. This makes it more difficult if branches carries many transactions in their past cones.
Many attachers with their 10ms polling (currently) consumes a lot of resource and the system is not able to keep up with the current load.
We have to invent a strategy how to deal with it. Some options:
- assume we do not sync from a snapshot further than several hundreds of slot back (more nuanced)
- assume higher node requirements during syncing
- invent some special 'warp syncing' mechanism, e.g. sending past transactions in a bulk to the local txstore before starting building a DAG
- etc
