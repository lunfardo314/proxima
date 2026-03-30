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

### GC policy: uniform across node types

Two GC mechanisms work together to prevent memory spikes:

1. **Periodic baseline GC** (every 5s): `MemoryPressureGC()` called from the stress level loop.
   Keeps Go's GC "warm" on all node types uniformly. Without this, access nodes get 5-8x fewer
   GC cycles than sequencer nodes (which get GC from milestone attachment) and suffer memory
   spikes when allocation bursts outpace GC.

2. **Push-triggered GC** after bulk operations: `MemoryPressureGC()` called after branch commits,
   LRB-depth pruning, forward-sync batch commits. Catches spikes right after heavy allocations.

Both coexist. The periodic one prevents drift; the push one handles spikes. `MemoryPressureGC`
is internally rate-limited (100ms) so overlapping calls are harmless.

### Sequencer survival mode

When `context deadline exceeded` occurs repeatedly (proposer can't build milestones in time),
the sequencer should switch to survival mode:
- Minimize tag-along inputs (reduce attachment budget to near-zero)
- Skip non-branch milestone targets (only produce branches to maintain coverage)
- Prune stale milestones from the tippool aggressively
- Log the condition clearly so operators can diagnose

Without survival mode, the sequencer gets stuck in a loop of failed proposals — it can't
recover because each failed attempt wastes the full timeout period, and the tippool/backlog
accumulates stale state that doesn't clear even after load drops.

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

---

## Testnet findings (2026-03-30)

### Session summary

Branch `develop07-txflow2`. Testnet: 4 machines, 4 cores / 8 GB each, 2 nodes per machine
(sequencer + access). Memory limits: 3700-3800 MB per access node.

Implemented phases 1-3 + GC equalization + goroutine profiling. Phases 4-5 not yet implemented.

### Run 1: 117 senders (~25 TPS)

Stable for ~10 minutes, then **simultaneous memory spike** on all access nodes at ~10 min mark.
Two access nodes crashed (loc0-acc, boot-acc), two survived (seq1-acc, loc1-acc).

After spammers stopped, all 4 sequencer nodes and 2 surviving access nodes **recovered fully**:
goroutines dropped 500+→176, memory stabilized at 400-550 MB.

### Run 2: 217 senders (~40+ TPS)

Sequencers could not keep up. `context deadline exceeded` appeared within 2 minutes — proposers
can't build milestones fast enough when past cones contain 100+ non-seq transactions per branch.
loc0-acc crashed at 11 min. After stopping spammers, sequencers **did not recover** — stuck in
a loop of `context deadline exceeded` every 10s with stale tippool/backlog.

### GC frequency gap (root cause of access node crashes)

The fundamental issue: sequencer nodes run 5-10x more GC cycles than access nodes.

| Node type | GC cycles / 17 min | Mechanism |
|-----------|-------------------|-----------|
| Sequencer | 348-388 (~22/min) | Go's automatic GC + `MemoryPressureGC` from milestone attachment |
| Access | 44-53 (~4/min) | Go's automatic GC only (periodic `MemoryPressureGC` too conservative) |

The periodic 5s `MemoryPressureGC` was implemented but only forces GC when stress >= 50%.
Access nodes hover at 8-18% stress normally, so the forced GC never fires. The sequencer node
gets frequent GC from Go's automatic collector because its allocation pattern (milestone
generation, past cone walks) triggers GC more often.

**Next step**: Lower the threshold or force `runtime.GC()` unconditionally every 5s. The cost
(~5-10ms) is trivial compared to a crash. This should equalize GC frequency across node types.

### Goroutine profile (access nodes under load)

All 4 access nodes showed the same profile when goroutines exceeded 300:

```
IO wait:      210-314   (libp2p/quic network connections — gossip traffic)
select:       121-130   (background loops: cleanup, peering, poker, etc.)
chan receive:  65-68     (queue consumers, events)
running:      1-11
syscall:      1
```

The goroutine growth is **IO wait from libp2p** — not a code leak. Under 117+ senders, gossip
volume increases network connections. Access nodes accept more incoming connections because
they don't send outgoing milestones. After spammers stop, goroutines drop from 500+ to ~176
within seconds — connections close naturally.

### Memory spike pattern

The spike is simultaneous across all access nodes (within the same 10-second window), suggesting
a network-wide trigger — likely a heavy branch commit with many non-seq transactions. Example:
branch commits with `tx: 10 seq + 115 non-seq` appear right before the spike.

On the access node that survived (seq1-acc), the spike went: 637→1626→1987→2234→2736→2996→434 MB
over 70 seconds. GC eventually caught up (gc: 27→42) and memory dropped. The nodes that crashed
were those where GC couldn't recover before hitting the 100% limit.

### "WON'T SUBMIT BRANCH" analysis

These appear when `IsHealthyCoverageDelta` returns false — the branch's coverage delta is below
the healthy threshold. This happens when the sequencer can't endorse enough other sequencers'
milestones (they're not in the tippool because everyone is struggling). It's a cascading failure:
one slow sequencer → less coverage for others → they also can't produce healthy branches.

### No recovery after 217 senders

After stopping the 217-sender spammer, sequencers remained stuck with continuous
`context deadline exceeded`. The tippool had 154 stale own milestones. Each failed proposal
attempt wastes the full timeout period (~10s), preventing the node from making progress.
This confirms the need for sequencer survival mode (reduce work, clean backlog, produce
branches-only).

### loc0 machine anomaly

loc0-acc consistently crashes first despite:
- Same hardware as all other machines (4-core Xeon 2.60GHz, 8 GB)
- Same CPU benchmark performance
- Actually **better** I/O throughput (396 MB/s reads vs 140-318 on others)
- Same 2-node config (sequencer + access)

The cause remains unclear. Possibly Badger internal state (LSM tree shape, compaction history)
or timing-related (loc0 runs the spammer, so gossip arrives with slightly different timing).

### Remaining work for next session

1. **Fix GC equalization**: force `runtime.GC()` unconditionally every 5s in the stress loop
   (or lower `memPressureGCPct` from 50 to something like 10-15%). This is the single most
   impactful change for access node stability.

2. **Phase 4**: Stress-aware gates in `txinput_queue`. When stress rises, drop unsolicited
   seq non-branches. This prevents attacher goroutine pileup during memory spikes.

3. **Phase 5**: Sequencer budget throttle + backlog pruning by LRB depth. This reduces
   non-seq per branch (currently 50-115) which is the root cause of heavy branch commits.

4. **Sequencer survival mode**: When `context deadline exceeded` repeats, switch to
   branches-only production with minimal tag-alongs and aggressive backlog cleanup.

5. **Investigate loc0 anomaly**: Run with profiling (pprof) on loc0-acc specifically to
   find if there's a Badger or OS-level difference.
