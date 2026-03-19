# Syncing Architecture — Issues and Insights

## Current approach

The node syncs by receiving sequencer transactions via gossip and spawning attacher goroutines
for each. Each attacher recursively pulls dependencies, which spawn more attachers. This creates
a tree of goroutines waiting for their dependencies to finish.

## Problems observed (2025-03-19 testnet sessions)

### Resource explosion during sync
- 235 slots behind: 200 concurrent attachers, 5193 seq transactions dropped
- Each attacher holds a past cone in memory, waiting for dependencies
- Dependencies arrive via gossip but get dropped by rate control → deadlock
- Disabling rate control during sync removes the deadlock but allows unbounded resource usage

### Syncing from distant snapshots is problematic
- More than a few hundred slots back (~1 hour) creates unsustainable attacher load
- Branches with many tag-along transactions have large past cones
- Recursive attacher model doesn't scale for large gaps

### "Am I synced?" is subjective
- In a distributed system, a node can't definitively know if it's in sync
- Current `IsSynced()` checks healthy branch slots, but this is a heuristic
- A node might think it's synced but be on a minority branch
- Rate control decisions based on `IsSynced()` can oscillate near the boundary

## Key insight: sequential sync doesn't need recursive attachers

We do not actually need recursive spawning of attachers when syncing IF we are on the true
branch sequence. We can sync one slot/branch at a time, then the next, one by one.

The current recursive model exists because the attacher doesn't know which branch sequence is
"true" — it explores the DAG by pulling dependencies on demand. But during sync from a known
snapshot, the branch chain is deterministic: each branch has a stem link to its predecessor.
Walking this chain slot by slot, committing each branch before moving to the next, would:

- Use exactly 1 attacher at a time (or a small fixed number for parallel branches)
- Have bounded memory — one past cone at a time, released after commit
- Not need rate control at all during sync
- Be predictable and debuggable

The challenge is determining the "true branch sequence" — this requires knowing which branches
are on the heaviest coverage path. During sync, the node could request this chain from peers
explicitly (a "sync protocol") rather than discovering it through gossip.

## Pragmatic approaches for consideration

- **Bulk txstore preload**: before building the DAG, request all raw transaction bytes for
  the sync range and store them locally. Then attach from local txstore — no pulling, no waiting.
- **Sequential branch-by-branch sync**: follow the stem link chain, commit one branch at a time.
  Past cone transactions are either in local txstore or pulled on demand, but only one branch's
  worth at a time.
- **Sync protocol**: peers provide the branch chain (list of branch txIDs) for a given slot range,
  along with their past cone transactions in bulk. Node verifies and commits sequentially.
- **Limit sync depth**: refuse to sync from snapshots more than N slots back (e.g., 500 slots /
  ~80 minutes). Require a fresh snapshot for larger gaps. This is a practical tradeoff — the
  snapshot mechanism already exists.
- **Hybrid**: use current recursive approach for small gaps (< 10 slots), switch to sequential
  for larger gaps.

## Relationship to rate control

Rate control (see [ratecontrol.md](ratecontrol.md)) should focus on normal operation only.
During syncing, the problem is not excess load from external spam — it's the node's own
internal work to catch up. Different mechanisms are needed:

- Normal operation: drop excess non-pulled transactions, limit attacher count
- Syncing: control the sync process itself (sequential, bounded), not the transaction flow
