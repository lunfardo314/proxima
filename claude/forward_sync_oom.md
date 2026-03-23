# Forward-Sync OOM on Access Nodes

## Incident: loc0-acc crash 2025-03-23

### Timeline (from proxima.log)

```
14:26 → Start forward-sync from slot 74515, memory ~510 MB, vertices ~2900
14:57 → Slot 80984, ~6,470 branches committed in 32 min (~3.4/sec), vertices: 34,272, memory: 709 MB
14:57:48 → vertices: 34,472, memory: 1,392 MB   ← GC starts losing
14:57:58 → vertices: 34,745, memory: 2,817 MB   ← GC counter stuck at 464-466
14:58:08 → vertices: 34,915, memory: 3,556 MB
14:58:18 → vertices: 35,184, memory: 4,357 MB
14:58:28 → vertices: 35,274, memory: 6,003 MB   ← OOM, log stops
```

### Key Observation

Vertex count grows only ~1000 in 50 seconds (34,272 → 35,274). Memory grows 8x in the same
window (709 MB → 6 GB). The problem is NOT vertex accumulation — it is allocation rate
exceeding GC's ability to reclaim during rapid branch commits.

## Root Cause Analysis

### 1. No rate limiting in forward-sync commit loop

`sync.go` processes all committed branches in a tight loop:
```go
for len(s.branchList) > 0 {
    s.ForceCommitBranch(s.branchList[0])
    if _, ok := multistate.FetchRootRecord(...) {
        s.branchList = s.branchList[1:]
        nCommitted++
    } else {
        break
    }
}
```
No pauses, no GC triggers, no memory checks between iterations.

### 2. Heavy per-branch allocation not freed until GC

Each branch commit allocates:
- `*multistate.Mutations` — full mutation set for the branch
- `[]base.TransactionID` (CommittedTxs) — every txID in the branch's past cone
- State reader with trie cache references
- Trie batch writes to BadgerDB

These objects are freed only after the branch is committed AND Go GC collects them. With
3.4 commits/second, the allocation rate overwhelms GC.

### 3. GC death spiral

GC counter freezes at 466 while memory climbs from 2.8 GB to 6 GB. This is the classic
Go GC death spiral: allocation rate exceeds collection rate → heap grows → GC takes longer
→ more allocations pile up → OOM.

### 4. Vertex TTL is wall-clock-based, not commit-based

MemDAG GC evicts vertices older than 24 wall-clock slots (~4 minutes). During forward-sync,
the node processes hundreds of historical slots per wall-clock slot. Vertices from recently
committed old-slot branches are still "fresh" relative to wall-clock time and won't be evicted.

### 5. State reader cache accumulation

`stateReaderCacheLimit = 3000`. During rapid forward-sync, many state readers are created
for branch commits. Each holds trie cache references that prevent deep GC.

## Proposed Fixes (pending implementation)

### Quick wins

**A. `runtime.GC()` between forward-sync batches**
After each 100-branch batch completes, call `runtime.GC()` to force collection before
the next batch. Simple, non-invasive, addresses the GC death spiral directly.

**B. Rate-limit forward-sync when memory is high**
Check `runtime.MemStats.Alloc` between commits. If above a threshold (e.g., 2 GB),
sleep briefly to let GC catch up. Or simply limit commits to N per second.

**C. Set `GOMEMLIMIT` environment variable**
Give Go GC a hard ceiling (e.g., `GOMEMLIMIT=6G`). This makes GC more aggressive as
memory approaches the limit, trading CPU for memory safety. Zero code changes — just
set in the systemd service file.

### Structural fixes

**D. Commit-count-based GC trigger during forward-sync**
After every N branch commits (e.g., 50), trigger memDAG GC explicitly — don't rely solely
on the 5-second timer. This ensures vertex cleanup keeps pace with commit rate.

**E. Limit pending branch accumulation**
Don't pull more branches (pull_ahead) while the number of pending uncommitted branches
exceeds a threshold. This bounds the amount of in-flight branch data.

**F. Eagerly free branch commit data**
Clear `mutations` and `committedTxs` slices immediately after the DB write completes,
rather than waiting for cache eviction or GC. Set fields to nil in `_commitPendingBranch`
after successful commit.

**G. Bound state reader cache during forward-sync**
Reduce `stateReaderCacheLimit` or eagerly evict readers for branches that are already
committed and no longer needed.

### Recommended implementation order

1. **C** (GOMEMLIMIT) — immediate deployment, no code change
2. **A** (runtime.GC between batches) — small code change, direct fix
3. **F** (eager free) — moderate change, reduces GC pressure structurally
4. **D + E** (rate limiting) — proper long-term solution

## Related

- [ratecontrol.md](ratecontrol.md) — general rate control architecture
- [snapshot_optimize.md](snapshot_optimize.md) — snapshot load shedding (also addresses resource contention)
- [sync.md](sync.md) — sync architecture notes
