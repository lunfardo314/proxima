# Crash Investigation: Competing Branches in Past Cone

## DAG visualization

![DAG screenshot](/mnt/c/Users/evaldas/Desktop/proxima/crash3.png)

## The crash

All sequencer nodes crash on `_commitPendingBranchUnlocked` assertion:
```
Mismatch input amount + inflation != output amount
```

Example: branch `[260808|0br]01689f0d37cf..` (produced by seq1), baseline `[260807|0br]01439ae68253..`, diff `1_691_022_357_344`.

The diff matches the token amount of an output from seq1's orphaned branch chain — outputs from the losing branch get ADDed to mutations without corresponding DELs.

## Root cause chain

### 1. Seq1 builds on its own branch, then endorses a milestone on a different branch

- Seq1 produces branch `[260806|0br]016a65d47af1..` (its own, later orphaned)
- Seq1 extends through its own chain: `016a65d47af1` → `0074259c8ee5` (28sq) → `002c183a6900` (37sq) → ...
- At `[260807|37sq]005709cad955..`, seq1 **endorses** `[260807|26sq]008163547ff3..`
- That endorsed milestone has baseline `[260807|0br]01439ae68253..` which descends from a DIFFERENT slot 260806 branch (`01d63631d6b3`, not seq1's `016a65d47af1`)

### 2. The endorsement pulls competing branch chain into the past cone

After the endorsement, the past cone of `005709cad955` contains TWO incompatible branch chains:
- Seq1's chain through `016a65d47af1` (slot 260806)
- The winning chain through `01d63631d6b3` (slot 260806) → `01439ae68253` (slot 260807)

Both slot-260806 branches consume the same stem from slot 260805 → double-spend in the past cone.

### 3. Branch `01689f0d37cf` inherits the conflicting past cone

`[260808|0br]01689f0d37cf..` extends the chain through `005709cad955` and inherits the conflict.

### 4. Mutations include orphaned branch outputs

When computing mutations from the past cone:
- Seq1's orphaned branch `016a65d47af1` and its milestone `0074259c8ee5` are NOT in the baseline state
- `Mutations()` generates ADD operations for their outputs
- But these outputs were never consumed from the baseline state (no DEL)
- Conservation invariant: `input + inflation != output` → crash

## Why wasn't the conflict caught?

### IncrementalAttacher (sequencer proposal)

`MergePastCone` checks baseline compatibility via `IsDescendantBranch`:
- `descendant = 01439ae68253` (endorsed milestone's baseline, slot 260807)
- `ancestor = 016a65d47af1` (seq1's own branch, slot 260806)
- `BranchKnowsTransaction(01439ae68253, 016a65d47af1)` → false (different chain)
- `BranchKnowsTransaction(016a65d47af1, 01439ae68253)` → false (earlier slot)
- Result: `(false, false)` → NOT compatible → `MergePastCone` should return false

**Question**: If `MergePastCone` rejects the endorsement, how did it get through? Possible explanations:
1. The endorsed milestone was not yet GOOD when traversed (Undefined sequencer → "don't go deeper" → PastConeBase not merged yet)
2. Baseline swap happened through a different intermediate merge that made the baselines appear compatible
3. The endorsed milestone's PastConeBase was nil (GC'd) → reattachment path → PastConeBase merge skipped

### Milestone attacher (validation)

`CheckAndClean` → `_checkVertex` should catch the stem double-spend:
- Stem output from slot 260805 has consumers in `vid.consumed` (global map)
- `_filterConsumingVertices` filters to those known in past cone
- If both competing branches are `IsKnown` → `len(consumers) == 2` → conflict

**Question**: Are both competing branches in `pc.vertices`? The endorsed milestone's PastConeBase may have had the competing branch REMOVED by `CheckAndClean` (it was in-state with all consumers in-state → `canBeRemoved=true`). When merged, the competing branch is absent → only one consumer visible → no conflict detected.

## Key hypotheses to test

### Hypothesis A: PastConeBase merge loses conflict evidence

1. Endorsed milestone's attacher runs `CheckAndClean`
2. The competing branch (in-state, all consumers in-state) is removed from PastConeBase
3. `CloneImmutable()` stores the cleaned PastConeBase
4. When merged into seq1's past cone, the competing branch is NOT present
5. `_filterConsumingVertices` only returns seq1's branch as consumer
6. `len(consumers) == 1` → no conflict detected

### Hypothesis B: Endorsement accepted before PastConeBase is available

1. The endorsed milestone is Undefined (not yet GOOD) when the IncrementalAttacher processes it
2. For Undefined sequencers, `attachVertexNonBranch` returns immediately ("don't go deeper")
3. PastConeBase is nil → no `MergePastCone` call → no baseline compatibility check
4. Later, when the milestone attacher processes it, the endorsed milestone becomes GOOD
5. PastConeBase merge happens, but by then the attacher has already built on the conflicting structure

### Hypothesis C: Virtual state reader masks the conflict

1. `_checkVertex` line 928: `inTheState && !stateReader.HasUTXO(stem)` should catch it
2. If `stateReader` is for seq1's own baseline (before swap), the stem IS consumed in seq1's state
3. But the consumer `016a65d47af1` is NOT in state of the endorsed baseline `01439ae68253`
4. If baseline swap happened, `stateReader` would be for `01439ae68253` → stem also consumed → conflict caught
5. If baseline swap did NOT happen (incompatible baselines → merge rejected), the conflict should not be reachable

## Mutations dump from crash

```
DEL   [260805|26sq]00e940e5384b..[0]
DEL   [260806|12sq]00f962bb69b0..[0]
DEL   [260806|12sq]00b812f9b2da..[0]
DEL   [260807|0br]01439ae68253..[1]
DEL   [260807|0br]01439ae68253..[0]
ADD   [260806|0br]016a65d47af1..[1] (0, inflation 0)           ← seq1's orphaned branch stem
ADD   [260806|28sq]0074259c8ee5..[0] (1_691_022_452_148)       ← seq1's orphaned milestone output
ADD   [260807|12sq]004ebb64f82e..[0] (1_659_080_458_622)
ADD   [260807|12sq]000dd27118cc..[0] (1_001_917_890_690_760)
ADD   [260807|26sq]008163547ff3..[0] (1_595_517_004_391)
ADD   [260808|0br]01689f0d37cf..[0] (1_691_063_320_826)
ADD   [260808|0br]01689f0d37cf..[1] (0, inflation 0)
```

The ADD for `016a65d47af1..[1]` and `0074259c8ee5..[0]` are from seq1's orphaned chain — they should not be in the mutations.

## Log references

Logs preserved on seq1 (`83.229.84.197`):
- `/home/nodes/seq1/proxima.log.1776068189` — crash run
- Conflict messages start at line 1230
- Past cone dump for first conflict at lines 1230-1340

## Attempted fixes (reverted)

1. `2213228e` — treat baseline as "in the state" in `_checkVertex` (single consumer case)
2. `e06b49d8` — handle multi-consumer case, remove losing branch subtrees from past cone
3. Both reverted in `f8562508` — the multi-consumer tolerance allowed invalid past cones through, causing conservation mismatches in tests (`TestFactorySkeletonStructure`)

The original strict check (`len(consumers) != 1` → conflict) is correct for `CheckAndClean`/`Mutations`. The bug must be fixed upstream to prevent competing branches from entering the past cone in the first place.

## Investigation progress

### Hypothesis A confirmed — but fix insufficient

The competing branch IS removed from PastConeBase by `CheckAndClean` because:
1. The branch is in-state
2. Its stem consumer (the baseline) is NOT in `pc.vertices` (stored only as `baselineBranchID`)
3. `_filterConsumingVertices` doesn't see the baseline → no consumers for stem → `canBeRemoved=true`

Fix attempt `f860eac7`: skip removal for branch vertices in `CheckAndClean`. This preserves the branch, but **didn't prevent the crash** because:

### The real issue: competing branch never enters past cone at all

Under high load (117 senders), the competing branch is fully committed (in-state) in the endorsed milestone's baseline. It was NEVER in the endorsed milestone's past cone delta — it was already in-state when the endorsed milestone was attached. So `CheckAndClean` never even sees it.

When the endorsed milestone's PastConeBase is merged into seq1's past cone:
- The competing branch is NOT in the merged PastConeBase (it was in-state, never in delta)
- Seq1's own branch IS in the past cone (it's in the delta)
- `_filterConsumingVertices` for the stem only sees seq1's branch → 1 consumer → no conflict

### Why _checkVertex can't catch this

`_checkVertex` only examines consumers that are `IsKnown` in `pc.vertices`. The competing branch was committed before the endorsed milestone was attached — it's in the trie, not in the memDAG past cone. `_filterConsumingVertices` correctly filters it out.

The line 928 check (`inTheState && !stateReader.HasUTXO`) would catch it IF seq1's branch was being checked against the correct baseline. But after baseline swap to the endorsed milestone's baseline, the state reader reflects the winning chain where seq1's branch doesn't exist — the stem IS consumed (by the winning branch), and seq1's branch IS the single visible consumer → conflict would fire.

**Critical question**: is the baseline actually swapped? `MergePastCone` returns false for incompatible baselines. If the baselines are incompatible, the merge is rejected. But if it's rejected, how does the endorsed milestone get into the past cone?

### Hypothesis D: endorsed milestone's PastConeBase is nil (GC path)

In `attachVertexNonBranch` Good case:
- `pcb := vid.GetPastConeNoLock()` — if nil, falls to line 162
- `IsInTheState(vid)` → true → milestone marked defined without MergePastCone
- No baseline compatibility check!
- The endorsed milestone is "defined" in seq1's past cone with seq1's OWN baseline
- Its past cone (with competing branches) is NOT merged, so the conflict is invisible

This is the most likely explanation: under high load, the endorsed milestone's PastConeBase was already GC'd (ConvertToDetached) by the time seq1's attacher reaches it. The vertex was Good+InTheState, so it's accepted without merge. Seq1's attacher never learns about the competing branch chain.

### Hypothesis D tested: baseline compatibility check on InTheState path

Added `branchesCompatible` check to the Good + InTheState + nil PastConeBase path (line 162-165). This catches SOME cases but **not all** — the `TestFactoryParallelWithTagAlong` test still crashes.

The competing branch can enter through a chain of merges that are individually compatible. For example:
- Seq1 endorses milestone M1 (compatible baseline) → merge OK
- M1's past cone includes milestone M2 from a different sequencer
- M2 was built on the competing baseline, but M2 itself is in-state → accepted without merge
- M2's outputs propagate into the merged past cone

The baseline compatibility check on a single vertex doesn't catch transitive incompatibility through already-merged PastConeBases.

### Current understanding of the entry paths

The competing branch outputs enter the past cone through multiple possible paths:

1. **Direct merge** (MergePastCone): endorsed milestone's PastConeBase contains the competing branch → caught by baseline compatibility in MergePastCone
2. **InTheState with nil pcb** (Hypothesis D): endorsed milestone is Good+InTheState, pcb GC'd → baseline check added but insufficient for transitive cases
3. **Transitive merge**: endorsed milestone M1 has compatible baseline, but M1's own PastConeBase was built by an attacher that merged M2's PastConeBase which contained outputs from the competing chain. The merge at M1 level was compatible because M1's baseline IS the winning branch. But M1's PastConeBase still contains the orphaned outputs from M2.

Path 3 is the most insidious: each individual merge is compatible, but the accumulated past cone contains vertices from incompatible branches.

### Fix directions to explore

**Direction A: Validate in Mutations()**

Before generating ADD mutations for not-in-state vertices, verify that each vertex's consumed inputs are either (a) in the baseline state, or (b) produced by another not-in-state vertex in the past cone. If a consumed input is from a vertex NOT in the past cone AND NOT in the state, the vertex is orphaned and should be skipped.

This is a safety net at the mutation level — catches all entry paths.

**Direction B: Track branch lineage in PastCone**

When merging PastConeBases, record which baseline branch chain each vertex belongs to. In `CheckAndClean`/`Mutations()`, skip vertices whose branch lineage doesn't match the current baseline.

This is more invasive but catches the root cause.

**Direction C: Prevent at endorsement selection**

In the factory/IncrementalAttacher, when selecting endorsement candidates, verify that the candidate's baseline is compatible with the current attacher's baseline BEFORE attempting the endorsement. This prevents the problem at the source.

This only prevents the sequencer from producing bad transactions — other nodes' attachers still need a safety net.

**Direction D: Strengthen _checkVertex with state-level check**

For each not-in-state vertex in the past cone, verify that ALL its consumed inputs exist in the baseline state OR are produced by another not-in-state vertex in the past cone. This is essentially Direction A but integrated into `_checkVertex`.

## Attempted fixes

| Commit | Approach | Result |
|--------|----------|--------|
| `2213228e` | Treat baseline as in-state in `_checkVertex` | Allowed invalid past cones through → reverted |
| `e06b49d8` | Multi-consumer tolerance + subtree removal | Same problem → reverted |
| `f8562508` | Revert both | Correct — strict check must stay |
| `dc4af830` | Make baseline visible in `_filterConsumingVertices` | Insufficient — only helps when branch in PastConeBase |
| `4bbfcd6d` | Nil state reader guard | Separate startup bug fix |
| `f860eac7` | Preserve branches in CheckAndClean | Insufficient — competing branch often not in PastConeBase at all |
| (not committed) | Baseline compatibility on InTheState path | Catches some cases but not transitive merges |

## Log references

- seq1 (`83.229.84.197`): crash at 15:58 with 117 senders, diff `3_354_975_101_622` (~3.3T = 2 orphaned branches). Two orphaned branch stems in mutations: `013a3e3308e9` and `016ba7c36d33` (both slot 262806). No conflict messages — neither attacher caught it.
- Original crash logs: `/home/nodes/seq1/proxima.log.1776068189` (conflict messages at line 1230)
- DAG screenshot: `/mnt/c/Users/evaldas/Desktop/proxima/crash3.png`
