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
| 2 | Drop unsolicited non-seq targeting own sequencer | Reduces non-seq vertices, lighter branch commits | stress > 60% or pipeline too large |
| 3 | Drop transactions before entering txinput_queue | Last resort: shed load at the gate | stress > 80% |

**Access nodes**: always drop all unsolicited transactions (both seq and non-seq). Access nodes
have no sequencer, so unsolicited non-seq txs serve no purpose. All needed transactions arrive
via pull during solidification. This is the simplest and most effective policy for access nodes.

Pulled transactions always pass, regardless of stress level.

Additionally, the **sequencer tag-along budget** is reduced under pressure — this is not a drop
gate but a throttle on how many tag-along inputs the sequencer pulls per milestone.

### Graduated response (sequencer nodes)

| Stress | Lever 1 (unsolicited seq) | Lever 2 (unsolicited non-seq) | Lever 3 (input gate) | Tag-along budget |
|--------|---------------------------|-------------------------------|----------------------|------------------|
| 0–40%  | normal (all pass) | normal (all pass) | normal | full (2/3) |
| 40–55% | tighten: cap attacher count | normal | normal | reduced (1/3) |
| 55–70% | aggressive: only branches pass | tighten: lower vertex limit | normal | minimal (1-2 inputs) |
| 70–85% | aggressive | drop all unsolicited non-seq | start dropping | zero tag-alongs |
| 85%+   | aggressive | drop all | drop all non-pulled | zero |

### Graduated response (access nodes)

| Stress | Unsolicited seq | Unsolicited non-seq | Input gate | Notes |
|--------|-----------------|---------------------|------------|-------|
| 0–40%  | drop all | drop all | normal | access nodes never attach unsolicited |
| 40–70% | drop all | drop all | normal | |
| 70–85% | drop all | drop all | start dropping | reduce gossip processing |
| 85%+   | drop all | drop all | drop all non-pulled | |

### Hysteresis

To prevent oscillation, lever activation uses asymmetric thresholds:
- **Activate** at the threshold (e.g., lever 1 at 50%)
- **Deactivate** only when stress drops 10% below activation (e.g., lever 1 deactivates at 40%)

This prevents rapid on/off cycling when stress hovers near a threshold.

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

### Phase 2: Graduated gates in txinput_queue

1. Replace static `shouldAttachSequencer` / `shouldAttachNonSeq` with stress-aware decisions
2. Implement the three levers with hysteresis using stress level thresholds
3. Access nodes: always drop all unsolicited (both seq and non-seq)
4. Add input gate: `txinput_queue.consume()` checks stress before processing

### Phase 3: Sequencer tag-along throttle

1. Expose stress level to sequencer via existing `NodeGlobal`
2. Scale tag-along budget based on stress: full → reduced → minimal → zero
3. The sequencer already has budget logic in the factory — add stress multiplier

### Phase 4: Dagviz gauge

1. Add stress level, pipeline size, isSyncing, isSnapshotting to WebSocket feed
2. Render vertical rainbow gauge on dagviz canvas
3. Show numeric values for stress and pipeline size, boolean flags for sync/snapshot

### Phase 5: Tuning

1. Deploy to testnet with default thresholds
2. Run spammer at increasing TPS
3. Observe stress gauge, adjust thresholds empirically
4. Document final thresholds
