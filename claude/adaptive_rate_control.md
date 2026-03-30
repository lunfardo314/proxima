# Adaptive Rate Control

## Problem

Under load, access nodes (and sequencer nodes with tight memory) experience sharp memory spikes
that trigger the memory watchdog shutdown. The current rate control is binary — gates are either
open or closed based on static thresholds. There is no graduated response to increasing pressure.

The loc0-acc crash pattern: memory goes from 415 MB to 3.7 GB in 60 seconds during branch commits
with many non-seq transactions, while GC can't keep up. The node hits the 90% shutdown threshold
before any rate control has a chance to react.

## Design

### Memory stress level

A single scalar **stress level** (0–100) derived from `runtime.MemStats.Alloc / memory.limit_mb`.
Computed every 1 second and exposed via `NodeGlobal` for all components.

```
stress = clamp(0, 100, int(100 * allocatedMB / limitMB))
```

When `memory.limit_mb` is 0 (disabled), stress is always 0 (no regulation).

**Regulation target**: keep stress at 50–70% under sustained load.

### Pipeline size

A single counter tracking total transactions "in the pipeline":
```
pipeline_size = total_vertices + solicited_queue_len + txs_waiting_for_clock
```

Components:
- `total_vertices` — all vertices in memDAG (seq + non-seq)
- solicited queue length — txs waiting in `txsolicit_queue`
- `wait` counter — txs in goroutines waiting for wall clock alignment

Does NOT include `txinput_queue` length for now (gossip buffer, not "in the pipeline").

**Signal**: "pipeline too large" — another input to the rate control decision, alongside stress.

### Sequencer backlog

The tippool tracks milestones per sequencer. A large own-milestone count (e.g., 100+ remaining
after purge) signals the sequencer is producing faster than the network can absorb. This is
already visible in the logs (`purged 1 own milestones, 127 remain`).

**Signal**: "sequencer backlog too large" — triggers tag-along throttle.

### Three levers

The rate control has exactly three levers, ordered from least to most aggressive:

| # | Lever | Effect | When |
|---|-------|--------|------|
| 1 | Drop unsolicited seq transactions | Reduces attacher goroutines, slows pipeline growth | stress > 50% or pipeline too large |
| 2 | Cut sequencer attachment budget | Fewer tag-along inputs per milestone, lighter branch commits | stress > 40% |
| 3 | Drop transactions before entering txinput_queue | Last resort: shed load at the gate | stress > 80% |

**Important**: unsolicited non-seq transactions targeting the local sequencer are **never dropped**.
They are needed for the sequencer to include tag-along inputs. The way to reduce non-seq load
is lever 2: cutting the attachment budget so the sequencer includes fewer tag-alongs per milestone.

**Access nodes**: always drop all unsolicited non-seq transactions (no local sequencer to target).
Unsolicited seq transactions follow the same rules as sequencer nodes: branches always pass,
non-branches are subject to the attacher cap (lever 1). Essentially not much different from
sequencer nodes — the only difference is there's no sequencer and no tag-along budget to cut.

Pulled transactions always pass, regardless of stress level.

### Sequencer attachment budget and backlog management

**Attachment budget throttle**: The attachment cost budget has a deterministic consensus cap that
all nodes must agree on for validation. The local sequencer can only set a **lower** local cap,
never exceed the consensus one. Under stress, the sequencer reduces its local budget cap, which
means fewer tag-along inputs per milestone on average. This reduces the non-seq transactions
that need solidification in subsequent branches.

**Backlog pruning by LRB depth**: Tag-along outputs in the sequencer backlog are pruned
primarily by checking whether the tagged-along transaction is N slots below the LRB — not just
clock TTL. If a tag-along output's transaction is confirmed deep in the branch chain, it's
either already been included in a branch or is too old to be useful. LRB-depth check is
the primary criterion; clock TTL is the fallback for cases where LRB info is unavailable.
Pruning stale backlog entries reduces the work the sequencer does scanning candidates and
prevents unbounded backlog growth.

### Graduated response (sequencer nodes)

| Stress | Lever 1 (unsolicited seq) | Lever 2 (budget cap) | Lever 3 (input gate) | Backlog pruning |
|--------|---------------------------|----------------------|----------------------|-----------------|
| 0–40%  | normal (all pass) | consensus cap (full) | normal | LRB depth 3 + clock TTL |
| 40–55% | tighten: cap attacher count | 2/3 of consensus cap | normal | LRB depth 3 |
| 55–70% | aggressive: only branches pass | 1/3 of consensus cap | normal | LRB depth 2 |
| 70–85% | aggressive | minimal (branches only) | start dropping | LRB depth 1 |
| 85%+   | aggressive | minimal | drop all non-pulled | LRB depth 1 |

Non-seq transactions targeting local sequencer always pass (never dropped).

### Graduated response (access nodes)

| Stress | Unsolicited seq | Unsolicited non-seq | Input gate |
|--------|-----------------|---------------------|------------|
| 0–40%  | same as sequencer nodes (branches pass, non-branches capped) | drop all | normal |
| 40–55% | tighten: cap attacher count | drop all | normal |
| 55–70% | aggressive: only branches pass | drop all | normal |
| 70–85% | aggressive | drop all | start dropping |
| 85%+   | aggressive | drop all | drop all non-pulled |

No attachment budget or backlog management (no local sequencer).

### Hysteresis

To prevent oscillation, lever activation uses asymmetric thresholds:
- **Activate** at the threshold (e.g., lever 1 at 50%)
- **Deactivate** only when stress drops 10% below activation (e.g., lever 1 deactivates at 40%)

This prevents rapid on/off cycling when stress hovers near a threshold.

### MemDAG pruning as a backpressure lever

Currently memDAG GC runs every 5 seconds and prunes vertices based on two criteria:
- **Wall-clock TTL** (`vertexTTLSlots = 24`): vertex added > 24 wall-clock slots ago (only when synced)
- **Ledger-time TTL** (`vertexLedgerTTLSlots = 48`): tx slot > 48 slots behind latest committed branch

These are passive, time-based criteria. They don't respond to memory pressure — a vertex 10 slots
old is kept regardless of whether the node is at 30% or 85% stress.

#### LRB-depth pruning

Add a third pruning criterion: **depth behind the Latest Reliable Branch (LRB)**.

Once a transaction is included in a committed branch that is N branches deep behind the LRB,
it is no longer needed in the memDAG — it's confirmed in the persistent state and no attacher
will reference it. It can be safely detached and removed.

**Mechanism**: track the set of vertex IDs (vids) in the past cone of each committed branch.
When a branch reaches depth N behind the LRB, all vertices in its past cone set are eligible
for pruning. Reasonable starting value: **N = 3**.

This is more precise than slot-based TTL: a vertex from 2 slots ago that's already 3 branches
deep is safe to prune, while the current TTL would keep it for 22 more slots.

**Data structure**: `map[base.TransactionID]set.Set[*vertex.WrappedTx]` — branch ID to its past
cone vertex set. Updated during branch commits (the committed tx list is already available).
Pruned entries are removed when the branch itself falls off the depth window.

#### Stress-triggered memDAG cleanup

Under memory pressure, the node should actively prune the memDAG rather than waiting for the
next 5-second GC tick. Add memDAG cleanup as a response to rising stress, analogous to how
`MemoryPressureGC()` currently forces Go runtime GC:

| Stress | MemDAG pruning behavior |
|--------|------------------------|
| 0–40%  | Normal: 5-second periodic GC, TTL + ledger-time criteria |
| 40–60% | Normal + LRB-depth pruning at depth 3 |
| 60–80% | Aggressive: reduce LRB-depth to 2, run memDAG GC on every stress check (1s) |
| 80%+   | Emergency: reduce LRB-depth to 1, run memDAG GC immediately, force Go GC between runs |

This gives the node two ways to shed memory:
1. **Admission control** (the three levers) — reduces inflow
2. **Aggressive pruning** — reduces what's already in memory

### MemoryPressureGC before branch commits

Independent of the stress level, call `MemoryPressureGC()` before each `ForceCommitBranch`.
Branch commits are the heaviest allocators (trie mutations, committed tx lists). Giving GC a
chance to run between commits prevents the GC-stall death spiral observed in the crash log.

## Visualization: stress gauge in dagviz

Display the stress level as a vertical linear gauge on the left side of the dagviz canvas,
between the legends. Rainbow color scale:

```
100 ████ red        (shutdown imminent)
 85 ████ orange     (dropping at input gate)
 70 ████ yellow     (aggressive dropping)
 55 ████ light green (tightening)
 40 ████ green      (normal)
  0 ████ blue       (idle)
```

The gauge updates in real-time via the existing WebSocket feed. The current stress value is
also shown as a number next to the gauge.

Additionally display:
- Pipeline size (numeric)
- `isSyncing` flag
- `isSnapshotting` flag

### API

Add stress level to the existing node info or as a new lightweight endpoint:
- `GET /api/v1/stress` → `{ "memory_stress": 67, "pipeline_size": 1234, "seq_backlog": 42 }`
- Or add fields to existing `/api/v1/get_node_info` response.

## Implementation plan

All thresholds and rate-control constants are static code constants (no config options for now).
All constants and rate-control related variables must be well-commented.

### Phase 1: Stress level infrastructure

1. Add `MemoryStressLevel() int` to `NodeGlobal` interface
2. Implement in `global.Global`: computation every 1 second, atomic int storage
3. Add `PipelineSize() int` — sum of `total_vertices` + solicited queue len + `wait` counter
4. Add stress level and pipeline size to `/api/v1/get_node_info` response
5. Call `MemoryPressureGC()` before each `ForceCommitBranch` in branches module

### Phase 2: Dagviz gauge

1. Add stress level, pipeline size, isSyncing, isSnapshotting to WebSocket feed
2. Render vertical rainbow gauge on dagviz canvas
3. Show numeric values for stress and pipeline size, boolean flags for sync/snapshot

### Phase 3: LRB-depth pruning in memDAG

1. Track past cone vertex sets per committed branch in the branches module
2. Add LRB-depth pruning criterion to `doGC()`: prune vertices in branches at depth >= N behind LRB
3. Wire stress level into memDAG GC: at higher stress, reduce depth threshold and increase GC frequency
4. Clean up branch past-cone sets when the branch itself falls off the depth window

### Phase 4: Graduated gates in txinput_queue

1. Replace static `shouldAttachSequencer` / `shouldAttachNonSeq` with stress-aware decisions
2. Implement the three levers with hysteresis using stress level thresholds
3. Access nodes: always drop unsolicited non-seq; seq follows same rules as sequencer nodes
4. Add input gate: `txinput_queue.consume()` checks stress before processing

### Phase 5: Sequencer budget throttle and backlog pruning

1. Expose stress level to sequencer via existing `NodeGlobal`
2. Scale local attachment budget cap based on stress (never above consensus cap)
3. Add LRB-depth pruning to the sequencer backlog: remove tag-along outputs whose
   transactions are N+ slots behind LRB (N decreases with stress)
4. The sequencer already has budget logic in the factory — apply stress-scaled local cap

### Phase 6: Tuning

1. Deploy to testnet with default thresholds
2. Run spammer at increasing TPS
3. Observe stress gauge, adjust thresholds empirically
4. Document final thresholds
