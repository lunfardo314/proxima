# Finalization of the architecture of memDAG pruning/GC

## Context
We intentionally used low-end configuration of nodes in the testnet. 
The goal is to make the node adaptable and survive high load situations.

We've developed solutions, experimented and encountered problems in the intersection of:
- adaptive rate control in the situation of high load
- Go runtime GC 
- pruning and GCing the memDAG to keep number of objects manageable
- making node adapt and survive high loads

The relevant experience has been collected in:
- 
- [conflict.md](claude/conflict.md)
- [ratecontrol.md](claude/ratecontrol.md)
- [rate_control_non_seq.md](claude/rate_control_non_seq.md)
- [adaptive_rate_control.md](claude/adaptive_rate_control.md)

This file defines finalizes conclusions and the overall architecture of memDAG pruning. 
It should be implemented on the basis of the current implementation.

## The problem

MemDAG is a cache where node keeps most actual fragment of the overall transaction DAG it is working with.

In the probabilistic consensus system like Proxima we cannot know for 100% if a vertex of the DAG will be referenced
by some other transaction or not. In particular, there's no way how to determine finality of the property _being orphaned_.
 
That creates a problem of making aggressive pruning/GC of the DAG in order to keep memory requirements reasonable. 
while keeping consistent cache of the DAG and ensuring maximal independence and parallelism of software processed accessing it. 
In particular, whenever nodes try to delete an old vertex (prune it) from the memDAG,
we cannot rule out that some new vertex will try to access it. 

Currently, the problem described in the `claude/conflict.md` it seems happening when some vertices are considered orphaned and detached
from the memDAG, while other vertices still attempt to references it.

## Approach to memDAG pruning
We adopt the following approach:
- node `detaches` vertices from the DAG (nullifies past cone references) when it becomes prune-able, i.e. it's the chances of become referenced by some new transaction become negligible
- in case the attacher or any other part of the system encounters a `DetachedVertex`:
  - the attacher marks the vertex **Bad** with error like `"detached vertex encountered during attachment"`
  - the node issues a **graceful shutdown** (no assert/panic) with a clear highlighted log message and recommendation to restart
- encountering a detached vertex is not a fundamental inconsistency — it is a rare edge case of premature pruning. Node will continue its operation after restart from consistent persisted state.

### GracefulShutdown function
We need a universal `GracefulShutdown(reason string)` function callable from any context:
- detached vertex encounter (pruning edge case)
- Ctrl-C / OS signal handling
- memory watchdog (OOM prevention)
- any other unrecoverable but non-panic situation

It should:
- log the reason prominently (FATAL-level or similar)
- initiate orderly shutdown (cancel root context, flush stores, etc.)
- NOT panic or os.Exit — allow deferred cleanup to run
- be idempotent (safe to call multiple times from different goroutines)

The pruning mechanism should ensure the _negligible chances_ actually means `practically never`. It may happen only in severe forking situations and similar.

- vertex is removed by the memDAG pruning mechanism and Go runtime GC once it stops being referenced by other parts of the node: vertices in the DAG and the sequencer (if any). 
- _referenced by sequencer_ means any of the following:
     - it is referenced in the tippool
     - it is referenced in the backlog (backlog should not contain wrapped output of the vertex)
     - it is referenced by the _own milestones_ set
- sequencer is responsible for unreferencing vertices that lost their relevance by the cleanup (usually based on TTL) of tippool, backlog and own milestones.
- node already keeps special structure that lists sets of vertices confirmed by specific branch
- the criteria for prune-ability of the vertex (any of the following):
  - it becomes older than specific clock TTL AND it is not referenced by the sequencer. Reasonable TTL is 12 slots (2 min)
  - the vertex is confirmed k slots deep behind the LRB AND it is not referenced by the sequencer. Reasonable k is 3
  - "confirmed" means the vertex is in the `branchVertices` set of a branch that is at least k slots behind the LRB. This is never final — it's a probabilistic criterion.
  - for access nodes (no sequencer), "not referenced by sequencer" is always true, so only TTL and confirmed-depth apply

_Being orphaned_ is **removed** as a prune-ability criterion. There is no way to determine finality of the orphaned property, and premature pruning of "orphaned" vertices caused the conflict bug documented in `conflict.md`.

## Cleanup of sequencer references

- _tippool_ is cleaned up based on TTL (implemented)
- _own milestones_ is cleaned up based on TTL (implemented)
- _backlog_ is cleaned up based on the following criteria:
   - TTL (implemented)
   - wrapped output being consumed by own milestone that is confirmed at depth K (k=2) in the LRB
- all tag-alongs with the token balances less than the minimums tag-along fee set by the sequencer should not be included to the backlog  

## Implementation notes

### Changes to current `doGC` in memdag.go
Current `doGC` has 3 criteria: TTL, confirmed-depth, orphaned-seq-tx. Remove the third criterion (orphaned).
The remaining two already need "not referenced by sequencer" — this needs to be implemented.

### Checking "not referenced by sequencer"
The memDAG's `doGC` needs a way to ask the sequencer if a vertex is still referenced.
Add a method like `IsReferencedBySequencer(vid *WrappedTx) bool` accessible from the memDAG environment.
The memDAG environment already has access to the workflow which knows if a sequencer is running.

### Backlog depth-based cleanup
Currently `purgeBacklog` is TTL-only. Already-consumed outputs are filtered at proposal time via 
`IsConsumedInThePastPath` but remain in the backlog map until TTL expiry or explicit `RemoveOutput` call.
Add: check if output is still in the LRB state — if not, it was consumed and can be removed. 
Only remove if the consuming milestone is confirmed at depth K (k=2).

### Attacher DetachedVertex handlers
All `DetachedVertex` callback handlers in `attacher.go` and `attacher_milestone.go` that currently 
return `ok = true` (skip and continue) must be changed to: mark Bad, trigger graceful shutdown, return `ok = false`.

## Implementation plan

### Step 1: GracefulShutdown function
- Add `GracefulShutdown(reason string)` to `global.Global` (and `StartStop` interface)
- Logs prominently, calls `Stop()`, idempotent via `sync.Once` or atomic flag
- Update `main.go` signal handler to use it: `GracefulShutdown("received SIGINT/SIGTERM")`
- Update memory watchdog in `node.go` to use it

### Step 2: Attacher DetachedVertex handlers → Bad + graceful shutdown
- `attacher.go:187` (attachVertexNonBranch): change from `ok=true` to setError + GracefulShutdown + `ok=false`
- `attacher.go:225` (attachVertexNonBranchSolid fallback): same
- `attacher_milestone.go:104` (newMilestoneAttacher): already Fatalf — change to GracefulShutdown
- `attacher_milestone.go:284` (solidifyBaseline): already sets error + ok=false — add GracefulShutdown
- `attacher_milestone.go:334` (solidifyPastCone): same as above
- `attach.go:130` (AttachTransaction): log-only — add GracefulShutdown

### Step 3: Remove orphaned criterion from doGC
- In `memdag.go:doGC()`: remove criterion 3 (confirmed_or_orphan with `!isInAnyBranchSetNoLock`)
- Keep criterion 1 (TTL) and criterion 2 (confirmed-depth) 
- Confirmed-depth now means: vertex is in a branch set that is k slots deep behind LRB, i.e. `isInAnyBranchSetNoLock` returns true AND the branch is deep. Invert the logic: prune if confirmed deep, not if orphaned.
- Rename "confirmed_or_orphan" to "confirmed_deep"

### Step 4: IsReferencedBySequencer check
- Add `IsVertexReferencedBySequencer(vid *WrappedTx) bool` to workflow (delegating to sequencer)
- Sequencer checks: tippool (latestMilestones map), ownMilestones map, backlog outputs map
- If no sequencer running, returns false
- Add to memDAG environment interface, wire through workflow
- Use in doGC: both TTL and confirmed-depth criteria require `!IsVertexReferencedBySequencer`

### Step 5: Backlog depth-based cleanup
- In `purgeBacklog`: after TTL check, also check if output is still in LRB state
- If not in state → consumed → remove from backlog
- Guard with confirmed-depth (k=2): only remove if output's tx slot is at least 2 behind LRB

## Tests
It may be difficult to reproduce exact pruning-related situations in tests therefore checks may be relaxed to make existing tests pass.