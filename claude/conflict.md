# Conflict in past cone — seq1 testnet incident 2026-04-02

## Summary

Single occurrence of `conflict in the past cone` on seq1 sequencer during 117 sender load test.
No other sequencer reported this conflict. Needs investigation.

## Error message

```
04-02 13:07:36.911  WARN  ATTACH [168991|60sq]005f7b20cd50.. 
  (baseline: [168991|0br]01ffb49c86d3..) 
  -> BAD(conflict [168990|0br]013d3ab5e3e5..[0] in the past cone:
```

## Key facts

- **Conflicting tx**: `[168991|60sq]005f7b20cd50..` — seq1's own seq tx at slot 168991, tick 60
- **Baseline**: `[168991|0br]01ffb49c86d3..` — branch at slot 168991
- **Conflicted output**: `[168990|0br]013d3ab5e3e5..[0]` — output 0 of a branch from slot 168990
- **Detection**: milestone attacher's `checkConsistencyBeforeWrapUp` during attachment
- **Producer**: seq1's own sequencer (this was seq1's own transaction, not from gossip)

## The conflict

Vertex #91 in the past cone: `[168990|0br]013d3ab5e3e5..`
```
consumers: {0: {[168990|12sq]00a924ed7c31..}, 1: {[168991|0br]01ffb49c86d3..}}
```

Output 0 of branch [168990|0br] is consumed by:
- `[168990|12sq]00a924ed7c31..` — a seq tx at slot 168990, tick 12 (consumer index 0)  
- `[168991|0br]01ffb49c86d3..` — the baseline branch at slot 168991 (consumer index 1)

Consumer index 0 is the sequencer chain output (predecessor). Consumer index 1 is the stem.
Both consuming output 0 of branch 168990 — this is the **stem output** of the branch.

The stem output of a branch should only be consumed by the NEXT branch in the same sequencer's
chain (as the stem input). If a seq tx at tick 12 of the same slot also consumes it, that's
a conflict.

## Questions

1. **Why does seq tx [168990|12sq] consume the stem output?** Sequencer non-branch txs consume
   the sequencer chain output (not the stem). Only branch txs consume the stem. Is this a
   transaction building bug?

2. **Why did the IncrementalAttacher not detect this before building the tx?** The 
   IncrementalAttacher checks conflicts via CheckConflicts before submitting. Either:
   - The conflict was introduced after the check (race condition with pruning?)
   - The check missed it (detached vertex in the consumer map?)
   - The consumer was added after the IncrementalAttacher's snapshot

3. **Is this related to our pruning changes?** Possible scenario: a vertex was detached,
   its consumer map was cleared, then another transaction consumed the same output without
   the conflict being detected. When the milestone attacher later checks, it sees both
   consumers and reports a conflict.

## Context at the time

```
memstats: att: 3, nonseq: 18, nonseq_drop: 1972, prop: 1, seq_drop: 2, 
  allocated memory: 375.2 MB, GC counter: 377, Goroutines: 203
sync: latest reliable branch is 1 slots behind, coverage: 2_019_077_103_639_287
```

Node was healthy — low memory, normal attacher count, synced.
Just before the conflict: `context deadline exceeded` at 13:07:36.661.

## Past cone details (126 vertices)

- Vertices span slots 168982–168991 (10 slots)
- All state-rooted vertices (S+) are `inTheState: (true,true)` — confirmed
- Non-rooted vertices (S-) from slot 168990+ are `inTheState: (true,false)` — checked but not found
- The seq1 sequencer's own chain: [168989|12sq] → [168989|24sq] → [168989|48sq] → [168990|12sq] → [168990|24sq] → [168990|48sq]
- The conflicting branch [168990|0br] has its output 0 consumed by both [168990|12sq] (seq chain) and [168991|0br] (stem)

## Raw data

Full past cone dump is in the seq1 log at 04-02 13:07:36.911.
The seq1 log file is at: `83.229.84.197:/home/nodes/seq1/proxima.log`

---

## Second incident: systemic conflict 2026-04-02 15:14-17:16

### Summary

All 4 sequencer nodes produced conflicting transactions (23-59 per node). All point to the
SAME conflict. The network got stuck — every seq tx after the initial conflict failed.

### The conflict

Branch `[170439|0br]018563ce2b62..` (**loc0's** branch, slot 170439).
Output 1 (sequencer chain output) consumed by TWO branches at slot 170440:
- `[170440|0br]01cd776f40a3..` — **loc0's** branch (correct — consumes own predecessor)
- `[170440|0br]01d301b79dbe..` — **boot's** branch (WRONG — consumes loc0's chain output)

### Root cause

Boot's branch at slot 170440 consumed loc0's sequencer chain output instead of its own.
This is a **transaction building bug** — the sequencer proposer selected the wrong predecessor.

### Evidence

```
#141 S+ [170439|0br]018563ce2b62.. consumers: {
    0: {[170439|12sq]00f4e4886704..},   -- output 0 (stem) consumed by loc0's seq tx
    1: {[170440|0br]01d301b79dbe..,     -- output 1 (chain) consumed by boot's branch (WRONG)
        [170440|0br]01cd776f40a3..}     -- output 1 (chain) consumed by loc0's branch (correct)
}
```

### Timeline

- 15:14:39 — loc0 submits branch [170439|0br]018563ce2b62.. (healthy, 100% coverage)
- 15:14:49 — loc0 submits branch [170440|0br]01cd776f40a3.. (healthy, consumes own predecessor correctly)
- 15:15:00 — boot's branch [170440|0br]01d301b79dbe.. committed (consumes loc0's chain output — BUG)
- 15:15:31 — first conflict detected on loc0 during seq tx attachment
- 15:15:31+ — ALL subsequent seq txs from ALL sequencers fail with same conflict

The network is stuck because all sequencers use [170440|0br]01d301b79dbe.. (boot's buggy branch)
as baseline, and the conflict is embedded in that baseline's past cone.

### Hypothesis

The proposer may have confused chain predecessors due to:
1. **Detached vertex**: if loc0's branch was detached by GC and reattached, the predecessor
   lookup might return wrong data
2. **Stale baseline**: the proposer used an outdated view of the chain where boot's predecessor
   appeared to be loc0's branch
3. **Race condition in IncrementalAttacher**: the proposer built a branch while the
   predecessor was being modified concurrently

### Affected nodes

- boot: 23 conflicts
- loc0: 47 conflicts
- seq1: 55 conflicts
- loc1: 59 conflicts

All conflicts reference the same baseline and same conflicting output.

### Logs

Log files are on respective machines at `/home/nodes/<name>/proxima.log`.
