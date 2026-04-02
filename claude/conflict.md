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
