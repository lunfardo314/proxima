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

### Corrected analysis (from txstore inspection)

**The transactions are valid.** Boot's branch correctly consumes:
- Input #0: `[170439|48sq]00a85c577ec7..[0]` — boot's own chain predecessor (chain ID $/9d2c6f..)
- Input #1: `[170439|0br]018563ce2b62..[1]` — the stem output of slot 170439's branch

The stem is shared — all branches at slot S consume the same stem from slot S-1. Both boot's
and loc0's branch at slot 170440 correctly consume the stem from [170439|0br]. This is by design.

**The bug is in the past cone construction**, not in transaction building. The milestone
attacher at `[170444|12sq]` has baseline `[170440|0br]01d301b79dbe..` (boot's branch). Its
past cone includes BOTH branches from slot 170440:
- boot's: `[170440|0br]01d301b79dbe..` (correct — it's the baseline)
- loc0's: `[170440|0br]01cd776f40a3..` (should NOT be there — competing fork)

Both consume the stem from [170439|0br], creating the conflict. In a correct past cone,
only ONE branch per slot should be included — the baseline branch.

### Root cause hypothesis

The attacher's past cone includes loc0's branch because a non-seq transaction or seq tx
in the past cone has loc0's branch in its own inputs/endorsements. When the attacher walks
the past cone, it pulls in both branches. This could happen if:

1. **A non-seq tx was included in BOTH branches' past cones** — common under high load where
   branches have overlapping tag-along inputs. The attacher follows the non-seq tx's input
   references and pulls in loc0's branch alongside boot's.

2. **The IncrementalAttacher added a tag-along input that was already consumed in loc0's
   branch** — creating a cross-reference between the two forks.

3. **Related to GC/pruning**: The conflict point is at slot 170439, the failing tx at slot
   170444 — 5 slots gap. With `branchPruneDepth=2`, the branch vertex could have been
   detached by GC when healthySlot reached 170441. Key hypothesis:
   
   `ConvertToDetached` clears Inputs/Endorsements but NOT the consumer map
   (`consumed map[byte]set.Set[*WrappedTx]`). If the branch was detached and then
   consumers from a different fork context were added (via `AddConsumer` which uses
   `mutexDescendants`, not `mutex`), the consumer map would accumulate consumers from
   BOTH forks. When the attacher later checks conflicts, it sees consumers from both
   forks on the same output → conflict.
   
   To verify: check if `[170439|0br]018563ce2b62..` was detached by GC during the window
   between its creation (15:14:39) and the conflict (15:15:31). That's 52 seconds — with
   branchPruneDepth=2 (~20 seconds), this is well within the pruning window.
   
   **Update**: Verified that `AddConsumer` uses `mutexDescendants` (not cleared by detach)
   and the `consumed` map survives detachment. HOWEVER, this is NOT the direct cause —
   both branches at slot 170440 ALWAYS consume the same stem from 170439 (expected behavior).
   The conflict check should handle same-slot branches as different forks.
   
   **The real issue**: The past cone includes BOTH forks because loc0's chain predecessor
   path goes through loc0's branch at 170440, while the baseline is boot's branch at 170440.
   The attacher should not build a tx where the chain path and baseline are on different forks.
   
   The GC/pruning connection: if the branch vertex was detached and its internal state
   degraded (e.g., lost fork information), the attacher may have lost the ability to
   distinguish which fork the chain predecessor belongs to, allowing it to mix forks.
   
   Heavy GC activity (93-115 detachments per 5-second cycle) was occurring at exactly
   the time of the conflict (15:14:30-15:14:40). The branch from slot 170439 was 5 slots
   behind by the time the conflict was detected — well within the pruning window.

### Confirmation: conflicts only appear with new pruning code

Checked older log files on loc0: **zero conflicts** in previous runs. The conflict ONLY
appears in the current run with the aggressive pruning changes. This confirms the pruning
connection.

### Detailed timeline

```
15:14:39  slot 170439  loc0 SUBMIT BRANCH (healthy, 100%)
15:14:49  slot 170440  loc0 SUBMIT BRANCH (healthy, 100%)
15:14:59  slot 170441  loc0 WON'T SUBMIT BRANCH (coverage unhealthy)
15:15:00  slot 170440  boot's branch committed (LRB moves to boot's branch)
15:15:11  slot 170442  loc0 SUBMIT SEQ TX (coverage 260T = 25% — very low)
15:15:31  slot 170444  loc0 SUBMIT SEQ TX → CONFLICT
```

At slot 170441, loc0's branch lost the coverage competition. The LRB moved to boot's branch
at slot 170440. Loc0's chain continues through its own (losing) branch at 170440. At slot
170444, the attacher uses boot's branch as baseline but loc0's chain pulls in loc0's branch
→ both forks in the past cone → stem conflict.

### Root cause conclusion

The conflict is caused by the attacher building a past cone that spans two forks. This
happens when:
1. The sequencer's own branch loses the coverage competition (forks diverge)
2. Aggressive pruning detaches the losing branch before the chain can be reconciled
3. The attacher encounters detached vertices and can't properly track fork boundaries
4. The past cone includes vertices from both forks

Without aggressive pruning, the branch stays as a live Vertex long enough for the attacher
to detect the fork divergence and handle it properly.

### Fix direction

Options:
A. **Increase branchPruneDepth** so branches survive long enough for fork reconciliation
B. **Don't prune branches** — only prune non-seq and orphaned seq vertices
C. **Detect fork divergence in the attacher** — if chain predecessor is on a different fork
   than the baseline, abandon the attachment
D. **The sequencer should detect its own fork divergence** — when its branch loses coverage,
   it should switch to the winning fork before producing more milestones

### THE BUG FOUND

In `attacher.go` line 150-162, when a Good vertex's past cone is merged:

```go
case vertex.Good:
    pcb := vid.GetPastConeNoLock()  // nil for detached vertices!
    if pcb != nil {
        if !a.pastCone.MergePastCone(pcb, a.Branches()) {  // baseline compatibility check
            // ... reject incompatible baselines
        }
    }
    ok = true    // CONTINUES even when pcb is nil — NO compatibility check!
    defined = true
```

When `ConvertToDetached` clears `pastCone`, `GetPastConeNoLock()` returns nil. The
`MergePastCone` call (which includes `IsDescendantBranch` baseline compatibility check) is
**skipped entirely**. The attacher adds the vertex to its past cone without verifying that
the vertex's baseline is compatible with the attacher's baseline.

The vertex's inputs (referencing a different fork's branch) then pull the conflicting branch
into the past cone without any compatibility guard.

**Fix**: When `pcb == nil` for a Good vertex, still verify baseline compatibility using
the vertex's `BaselineBranch()` method (which reads from the vertex state, not the past cone).
If incompatible, reject the merge.

### Affected nodes

- boot: 23 conflicts
- loc0: 47 conflicts
- seq1: 55 conflicts
- loc1: 59 conflicts

All conflicts reference the same baseline and same conflicting output.

### Logs

Log files are on respective machines at `/home/nodes/<name>/proxima.log`.
