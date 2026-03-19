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

### Thresholds (adjusted through testnet iterations)

| Parameter | Value | Rationale |
|-----------|-------|-----------|
| `maxConcurrentAttachers` | 200 | Normal operation: 0-4. Crash had 223. Only applies when synced. |
| `maxNonSeqVertices` | 5000 | Normal operation: ~500 vertices. Crash had 3425. |
| `maxQueueLen` (nonseq) | 1000 | Queue grew to 111K when vertex gate didn't trigger. |

**Syncing exception:** seq_attach limit is disabled when `!IsSynced()`. During sync, all seq
transactions must pass — dropping them stalls the recursive solidification chain.

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

## Testnet observations (2025-03-19, 117 senders)

### What worked
- `nonseq_attach` queue length limit (1000) prevents unbounded queue growth
- `seq_attach` dropping non-pulled seq txs when `att >= 200` caps attacher goroutines
- `RUnwrap` for solid non-seq vertices reduces lock contention on overlapping past cones
- Context-aware `CheckConflicts` and `CoverageDeltaRaw` prevent proposer stalls

### What didn't work or needs attention

**1. Access nodes have no protection against sequencer vertex accumulation.**
Access node crashed at 5.9GB with `nonseq: 0` and empty queues. The memory was from
sequencer vertices in the memDAG and GC unable to reclaim. Rate control gates only check
non-seq count and attacher count — sequencer vertex count on access nodes is uncontrolled.
Need a total vertex count gate or a separate mechanism for access nodes.

**2. The `nonseq` counter alone is insufficient as a gate metric.**
On loc0, the nonseq queue grew to 111K because memDAG `nonseq` stayed at ~700 (queue consumer
couldn't keep up). The queue length check fixes this, but the broader lesson: multiple metrics
must be checked. Total vertex count, queue length, and attacher count together provide
coverage that any single metric misses.

**3. GC stalling is the ultimate killer, rate control can't fix it.**
In every OOM crash, Go's GC counter freezes while memory climbs. Rate control prevents new
load from entering, but can't reclaim memory from load already inside. When GC stalls, the
node is doomed regardless of rate control. This may need Go runtime tuning (GOGC, GOMEMLIMIT)
or architectural changes (bounded data structures, object pooling).

**4. Two nodes on 8GB machines is fragile under load.**
Current testnet machines have 8GB RAM, no swap, running 2 nodes each. Any memory spike on one
node triggers OOM killer for both. This is a testing environment constraint — production nodes
will have dedicated resources. But we want to squeeze maximum learning from the current testnet
before moving to machines with real specs.

**5. Syncing and normal operation need different strategies.**
See [sync.md](sync.md). Rate control that works for normal operation (drop excess) actively
harms syncing (blocks needed dependencies). The `IsSynced()` gate is a pragmatic workaround
but not a proper solution. Syncing needs its own architecture.

**6. Lock contention cascades under resource pressure.**
The tippool `purgeAndLog` write lock blocked the sequencer for >30 seconds — not because the
operation was slow, but because the goroutine was CPU-starved (OOM neighbor consuming all
resources). Rate control can't protect against OS-level resource exhaustion.

## Phase 2 (future)

### Syncing
See [sync.md](sync.md) for detailed analysis.

### Additional rate control metrics to consider
- Total vertex count (seq + non-seq) as a gate, especially for access nodes
- GC pressure indicators (allocation rate vs collection rate)
- Memory-based emergency gate (e.g., `runtime.MemStats.Alloc > threshold` → drop all non-pulled)
- Per-chain sequencer tip count (detect runaway chains)

### Structural improvements
- `GOMEMLIMIT` environment variable to give Go GC a hard ceiling
- Bounded memDAG with eviction policy (LRU by slot)
- Object pooling for past cone structures to reduce GC pressure
