# Crash Analysis and TODO

## Crash: 2026-03-17 — Testnet crash under 217 multispam senders

**Affected**: all access nodes and sequencer nodes on the testnet (boot, loc0, seq1, loc1).
**Log analyzed**: `seq1-acc:/home/nodes/seq1-acc/proxima.log.1773772126` (only surviving log).

### Timeline (seq1-acc, last ~2 minutes before crash)

| Time | Slot | Memory | Goroutines | MemDAG Vertices | Notes |
|------|------|--------|------------|-----------------|-------|
| 19:25:27 | 36556 | 433 MB | 196 | 422 | Steady state |
| 19:25:37 | 36557 | 1,069 MB | 196 | 441 | Spam hits — memory 2.5x in 10s |
| 19:25:47 | 36558 | 1,961 MB | 196 | 408 | |
| 19:25:57 | 36559 | 2,663 MB | 197 | 409 | |
| 19:26:07 | 36560 | 4,425 MB | 196 | 424 | Peak memory |
| 19:26:17 | 36561 | 2,732 MB | 197 | 414 | GC kicks in |
| 19:26:27 | 36562 | 3,136 MB | 331 | 698 | Goroutines + vertices start climbing |
| 19:26:37 | 36563 | 3,556 MB | 444 | 732 | |
| 19:26:47 | 36564 | 3,952 MB | 504 | 877 | |
| 19:26:57 | 36565 | 4,520 MB | 643 | 1,140 | |
| 19:27:07 | 36566 | 624 MB | 603 | 1,224 | GC reclaims, 7 attachers active |
| 19:27:17 | 36567 | 706 MB | 628 | 1,366 | |
| 19:27:27 | 36568 | 676 MB | 1,138 | 1,917 | Goroutines double in 10s |
| 19:27:33 | — | — | — | — | FATAL: deadlock detected |

### Crash sequence

1. **Memory spike (slots 36557-36560)**: 217 spammers flood the network. Memory goes from 433 MB to 4,425 MB in 30 seconds while goroutines stay at ~196. The input queue / parsing stage absorbs raw transaction bytes faster than they can be processed.

2. **Goroutine + vertex explosion (slots 36562-36568)**: Parsed transactions enter memDAG. Vertex count jumps from ~420 to 1,917. Each sequencer transaction needing solidification spawns an attacher goroutine — goroutines go from 196 to 1,138.

3. **Deadlock in solidification**: Attacher for `[36568|28sq]003935e56c57..` gets stuck in `solidifyPastCone()` for >10 seconds. The `lazyRepeat` loop calls `attachVertexUnwrapped()` which traverses the past cone — but with ~1,900 vertices and heavy contention on vertex locks from hundreds of concurrent attacher goroutines, the function cannot complete within the 10s deadlock threshold.

4. **FATAL exit**: Deadlock detector fires `log.Fatalf()`, dumps all goroutines, kills the process. The goroutine dump buffer (128KB = `2*MaxUint16`) was too small to capture the actual stuck goroutine among 1,138+ goroutines.

### Key observations

- **No backpressure**: No mechanism to limit incoming transaction rate or cap concurrent attacher goroutines. The node accepts everything the 217 senders push.
- **Not a true deadlock**: This is resource exhaustion causing slowness, not a cyclic lock dependency. The 10s threshold is too tight for a node processing 1,900+ vertices with 1,138 goroutines competing for vertex locks.
- **Boot sequencer node was more stable**: It showed steady memory ~230-400 MB and ~177-186 goroutines at the same timestamps, with much higher GC frequency (~22,500 vs ~1,680 GC cycles).
- **Goroutine dump truncated**: The `runtime.Stack(buf, true)` buffer at `2*math.MaxUint16` (128KB) is insufficient to capture 1,138 goroutine stacks. The actual stuck goroutine 388722052 was not in the dump.

### TODO: fixes to consider

1. **Backpressure on txInputQueue**: Cap the queue size or rate-limit incoming transactions when the node is under load (e.g., based on goroutine count or memory usage).

2. **Cap concurrent attacher goroutines**: Introduce a semaphore limiting how many solidification goroutines can run simultaneously. Excess transactions wait or are deferred.

3. **Adaptive deadlock threshold**: Make the 10s deadlock threshold adaptive (e.g., scale with vertex count or goroutine count), or downgrade it from FATAL to a warning/restart of the individual attacher.

4. **Vertex count limit in memDAG**: If vertices exceed a threshold, drop or defer low-priority (non-sequencer) transactions.

5. **Increase goroutine dump buffer**: Increase from `2*MaxUint16` to something larger (e.g., 1MB) so the actual stuck goroutine is captured in the dump.

6. **Investigate GC behavior difference**: Understand why boot node ran GC ~13x more frequently than seq1-acc. Could be GOGC settings or allocation patterns.
