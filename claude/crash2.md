# Crash Analysis: Token Conservation Invariant Violation & Conflict Detection

## Session Context

Branch: `develop07-seq-improvement` (Phase 2: event-driven sequencer with plateau detection)

Date: 2025-04-09

After deploying Phase 2 changes, the testnet crashed with all 8 nodes hitting the same fatal assertion during branch commit. A separate conflict detection issue was also observed on access nodes.

---

## Issue 1: Token Conservation Crash (All Nodes)

### Symptom

All 8 nodes crashed simultaneously on the same branch:

```
FATAL: _commitPendingBranchUnlocked([230327|0br]01c8cc156afc..)
  baseline=[230326|0br]0172651c14fe..
  -> updateTrie: major inconsistency.
  Mismatch input amount(1_005_023_327_296_036) + inflation(84_912_659)
  != output amount(1_006_696_011_243_129).
  Diff: 1_672_599_034_434
```

### Mutation Dump

The branch commit delta between baseline `[230326|0br]0172651c14fe..` (boot's slot 326 branch) and new branch `[230327|0br]01c8cc156afc..`:

```
DEL   [230326|0br]0172651c14fe..[0]
DEL   [230326|0br]0172651c14fe..[1]
ADD   [230326|15sq]00596b984fdf..[0] (1_672_621_119_947, inflation 94_278)
ADD   [230326|44sq]003fa5a31213..[0] (1_568_309_779_832, inflation 90_873)
ADD   [230326|47sq]00832487ed50..[0] (1_637_513_048_140, inflation 93_129)
ADD   [230327|0br]01c8cc156afc..[0] (1_001_817_567_295_210, inflation 62_643_144)
ADD   [230327|0br]01c8cc156afc..[1] (0, inflation 0)
ADDTX [230326|15sq]00596b984fdf.. : unspent [0]
ADDTX [230326|44sq]003fa5a31213.. : unspent [0]
ADDTX [230326|47sq]00832487ed50.. : unspent [0]
ADDTX [230326|56sq]00986ab96f2c.. : unspent []
ADDTX [230327|0br]01c8cc156afc.. : unspent [0 1]
```

### Key Observations

1. **Only 2 DELs**: Both from boot's baseline branch outputs (chain + stem). No DELs from any other source.

2. **3 other-sequencer txs as ADDs**: `[230326|15sq]`, `[230326|44sq]`, `[230326|47sq]` are from other sequencers. Their output [0] (chain output) is unspent. These txs consume their predecessor chain outputs, but those consumed outputs are NOT in the DEL list.

3. **Missing branches**: The other sequencers' slot 326 branches (which are the predecessors of those 3 txs) do NOT appear in the ADDTX list at all. They should be either:
   - In the state (InTheState) → their consumed outputs should be DELs
   - In the delta (not in state) → they should be ADDTXs

   They are **neither** — they're absent from the mutation set entirely.

4. **Boot's seq tx**: `[230326|56sq]` has `unspent []` (all outputs consumed by the branch). This is boot's own seq tx that endorsed 2 other sequencer txs (`endorse: 2` in log).

5. **Excess matches one tx's output**: The diff 1,672,599,034,434 is close to `[230326|15sq]`'s output (1,672,621,119,947). The difference (22,085,513) roughly corresponds to inflation accumulated along the chain.

### Timeline

```
19:35:20.326  SUBMIT BRANCH [230326|0br]0172651c14fe.. (boot's baseline branch)
19:35:24.584  BRANCH COMMIT [230326|0br]015dc8d51941.. 'loc1' (6 seq + 0 non-seq)
19:35:25.677  BRANCH COMMIT [230326|0br]0172651c14fe.. 'boot' (4 seq + 0 non-seq)
19:35:26.485  SUBMIT SEQ TX [230326|56sq]00986ab96f2c.. endorse: 2
19:35:30.497  [memdag GC] detached: 1, deleted: 0
19:35:30.568  SUBMIT BRANCH [230327|0br]01c8cc156afc..
19:35:33.050  purged 1 own milestones
19:35:34.383  FATAL assertion
```

### Root Cause Analysis

The bug is in `core/attacher/attacher.go`, function `attachOutput`, lines 591-594:

```go
if wOut.VID.IsBranchTransaction() {
    // if it is on the branch tx, it must be marked as defined
    a.pastCone.SetFlagsUp(wOut.VID, vertex.FlagPastConeVertexDefined)
    return true
}
```

This code handles branch transaction dependencies during past cone solidification. When the attacher encounters a branch in the dependency chain, it marks it as Defined and **returns immediately without recursing into the branch's inputs**.

This is correct when the branch IS in the baseline state — all its inputs are already accounted for in the state trie. But it's **incorrect when the branch is NOT in the baseline state**, because:

1. The branch is added to `pc.vertices` (Known + NotInTheState + Defined)
2. The branch's **consumed inputs are never traversed** — the predecessor chain outputs that the branch consumes are NOT added to the past cone
3. The predecessor vertices are NOT in `pc.vertices` at all
4. In `Mutations()`, the branch goes to the `else` branch (not in state), but since its consumed outputs' predecessors are missing, no DEL mutations are generated for them

#### Why the other sequencers' branches are NOT in the baseline state

`BranchKnowsTransaction(baselineID, txid)` in `branches.go:644`:

```go
if branchID.Slot() <= txid.Slot() {
    return false
}
```

Boot's baseline is slot 326, and the other sequencers' branches are also slot 326. Since `326 <= 326 → true → return false`, same-slot branches from different sequencers are never "in" each other's state. This is correct behavior — same-slot branches conflict.

#### The chain that leads to the bug

```
[230327|0br] (boot's new branch)
  extends [230326|56sq] (boot's seq tx, endorse: 2)
    endorses [230326|47sq] (other sequencer's tx)
      extends [230326|44sq]
        extends [230326|15sq]
          extends [230326|0br]015dc8d51941.. (loc1's branch, DETACHED)
            ← consumes output from loc1's slot 325 milestone (IN STATE)
            ← THIS INPUT IS NEVER TRAVERSED
```

When the attacher reaches `[230326|0br]015dc8d51941..` (loc1's detached branch):
- `refreshDependencyStatus` → Known, NOT InTheState (same-slot conflict)
- `attachOutput` → not InTheState → check IsBranch → YES → mark Defined → **return true**
- **No recursion** into loc1's branch inputs
- Loc1's slot 325 predecessor (which IS in boot's baseline state) is never added to `pc.vertices`
- No DEL mutation is generated for the consumed output

#### Why this wasn't triggered before Phase 2

Before Phase 2, the sequencer rarely accumulated 2+ endorsements from different sequencers in a single non-branch milestone. The old fixed-pace model submitted milestones quickly with 0-1 endorsements. Phase 2's plateau detection waits for coverage to stabilize, allowing more endorsements to accumulate. Boot's `[230326|56sq]` had `endorse: 2`, pulling multiple non-baseline same-slot branches into the past cone.

### Proposed Fix

The branch short-circuit in `attachOutput` must be conditional on the branch being in the baseline state:

```go
if wOut.VID.IsBranchTransaction() {
    a.pastCone.SetFlagsUp(wOut.VID, vertex.FlagPastConeVertexDefined)
    if a.pastCone.IsInTheState(wOut.VID) {
        return true  // state boundary — no need to recurse
    }
    // NOT in baseline state — must recurse to find the proper state boundary
    // Need to traverse this branch's inputs to generate proper DEL mutations
    // Note: attachVertexNonBranch asserts !IsBranchTransaction, so a new
    // traversal path is needed for non-state branches.
}
```

**Challenge**: `attachVertexNonBranch` has `Assertf(!vid.IsBranchTransaction())` at line 119. A new code path is needed for non-state branches that:
1. Traverses the branch's inputs (the chain output it consumes)
2. For each input, follows the chain until reaching a state vertex
3. Properly tracks consumers so DEL mutations are generated

Alternative: relax the assertion in `attachVertexNonBranch` for this specific case, or create a `attachBranchInputsForDelta` function.

### Location of the invariant check

File: `ledger/multistate/mutate.go`, lines 547-551:

```go
if addAmount != delAmount+inflation[0] {
    err = fmt.Errorf("updateTrie: major inconsistency. Mismatch input amount(%s) + inflation(%s) != output amount(%s). Diff: %s",
        util.Th(delAmount), util.Th(inflation[0]), util.Th(addAmount), util.Th(int(addAmount)-int(delAmount+inflation[0])))
}
```

### Files involved

| File | Relevance |
|------|-----------|
| `core/attacher/attacher.go:591-594` | **Bug location**: branch short-circuit in `attachOutput` |
| `core/attacher/attacher.go:118-206` | `attachVertexNonBranch` — the recursion that's skipped |
| `core/attacher/attacher.go:499-532` | `attachInput` — how inputs are processed |
| `core/attacher/attacher.go:414-440` | `defineInTheStateStatus` — determines InTheState |
| `core/vertex/past_cone.go:613-676` | `Mutations()` — generates DEL/ADD mutation set |
| `core/vertex/past_cone.go:546-583` | `consumersByOutputIndex` — tracks output consumers |
| `core/vertex/past_cone.go:586-605` | `producedIndices` — identifies unspent outputs |
| `core/core_modules/branches/branches.go:639-646` | `BranchKnowsTransaction` — slot comparison |
| `ledger/multistate/mutate.go:547-551` | Token conservation assertion |
| `core/attacher/wrapup.go:40-90` | `commitBranch` — where mutations are computed |

---

## Issue 2: Conflict in Past Cone (boot-acc and loc0-acc)

### Symptom

One conflict was detected on boot-acc and loc0-acc (same transaction propagated via gossip):

```
ATTACH [230239|71sq]0043ec811f2a.. (baseline: [230239|0br]01e96bea1fec..)
  -> BAD(conflict [230238|0br]010aea43e400..[0] in the past cone:
```

The transaction `[230239|71sq]` was rejected because output `[230238|0br]010aea43e400..[0]` appears to be double-spent in its past cone.

### Full Past Cone Dump

```
    ------ past cone: '[230239|71sq]0043ec811f2a..'
    ------ baseline: [230239|0br]01e96bea1fec..
    ------ tip: [230239|71sq]0043ec811f2a..

    #0  S+ [230233|27sq]008044959046.. consumers: {0: {[230239|46sq]0090062f6c7b..}}
        flags: known: true, defined: true, inTheState: (true,true),
        endorsementsOk: false, inputsOk: false

    #1  S+ [230233|27sq]00f1bb27a810.. consumers: {0: {[230238|79sq]002740dbf846..}}
        flags: known: true, defined: true, inTheState: (true,true),
        endorsementsOk: false, inputsOk: false

    #2  S+ [230238|0br]010aea43e400.. consumers: {0: {[230238|88sq]0065b0816716..}, 1: {[230239|0br]01e96bea1fec..}}
        flags: known: true, defined: true, inTheState: (true,true),
        endorsementsOk: false, inputsOk: false

    #3  S+ [230238|79sq]002740dbf846.. consumers: {0: {[230238|96sq]002b2ed601d6..}}
        flags: known: true, defined: true, inTheState: (true,true),
        endorsementsOk: true, inputsOk: true

    #4  S- [230238|88sq]0065b0816716.. consumers: {0: {[230239|0br]01e96bea1fec..}}
        flags: known: true, defined: true, inTheState: (true,false),
        endorsementsOk: true, inputsOk: true

    #5  S- [230238|96sq]002b2ed601d6.. consumers: {0: {[230239|55sq]00ae27cefeb0..}}
        flags: known: true, defined: true, inTheState: (true,false),
        endorsementsOk: true, inputsOk: true

    #6  S+ [230239|0br]01e96bea1fec.. consumers: {0: {[230239|63sq]0031ab78da0b..}}
        flags: known: true, defined: true, inTheState: (true,true),
        endorsementsOk: false, inputsOk: false

    #7  S- [230239|46sq]0090062f6c7b.. consumers: {}
        flags: known: true, defined: true, inTheState: (true,false),
        endorsementsOk: true, inputsOk: true

    #8  S- [230239|55sq]00ae27cefeb0.. consumers: {0: {[230239|71sq]0043ec811f2a..}}
        flags: known: true, defined: true, inTheState: (true,false),
        endorsementsOk: true, inputsOk: true

    #9  S- [230239|63sq]0031ab78da0b.. consumers: {}
        flags: known: true, defined: true, inTheState: (true,false),
        endorsementsOk: true, inputsOk: true, poke: true

    #10 S- [230239|71sq]0043ec811f2a.. consumers: {}
        flags: known: true, defined: false, inTheState: (true,false),
        endorsementsOk: true, inputsOk: true
```

### Analysis

The conflict is on `[230238|0br]010aea43e400..[0]` — output 0 of a branch from slot 230238.

Looking at entry #2:
```
[230238|0br]010aea43e400.. consumers: {0: {[230238|88sq]0065b0816716..}, 1: {[230239|0br]01e96bea1fec..}}
```

This is a **slot 238 branch** (InTheState=true). Its outputs:
- Output 0: consumed by `[230238|88sq]` (a sequencer tx from same slot)
- Output 1: consumed by `[230239|0br]01e96bea1fec..` (the slot 239 branch = the baseline)

These are **different outputs** (indices 0 and 1). There's no double-spend here at first glance.

But the conflict detection says `conflict [230238|0br]010aea43e400..[0]`. This means output 0 specifically is the problem. Output 0 is consumed by `[230238|88sq]`.

#### Chain reconstruction

The past cone shows two parallel chains converging:

**Chain A (boot's chain):**
```
[230238|0br]010aea43e400.. (state, slot 238 branch)
  output 0 → [230238|88sq]0065b0816716.. (delta, extends boot's chain)
    output 0 → [230239|0br]01e96bea1fec.. (state, baseline = boot's 239 branch)
      output 0 → [230239|63sq]0031ab78da0b.. (delta, boot's seq tx)
```

**Chain B (another sequencer's chain):**
```
[230233|27sq]008044959046.. (state, very old tx from slot 233)
  output 0 → [230239|46sq]0090062f6c7b.. (delta)

[230233|27sq]00f1bb27a810.. (state, another old tx from slot 233)
  output 0 → [230238|79sq]002740dbf846.. (state, another seq's tx)
    output 0 → [230238|96sq]002b2ed601d6.. (delta)
      output 0 → [230239|55sq]00ae27cefeb0.. (delta)
        output 0 → [230239|71sq]0043ec811f2a.. (tip, being attached)
```

The tip `[230239|71sq]` extends `[230239|55sq]` which extends `[230238|96sq]` which extends `[230238|79sq]`.

#### The conflict scenario

The conflict report says `[230238|0br]010aea43e400..[0]` is conflicted. Output 0 is consumed by `[230238|88sq]`. But the tip `[230239|71sq]` doesn't consume this output — it consumes `[230239|55sq]`'s output.

The conflict is detected because `[230238|88sq]` consumes `[230238|0br]...[0]`, and this consumption appears in the past cone. The conflict checker likely found that the same output is referenced as consumed by a transaction that is NOT in the tip's causal past, creating a conflict.

**Key question**: Is `[230238|88sq]` in the past cone of `[230239|71sq]`? Looking at the dump:
- `[230238|88sq]` (entry #4) IS in the past cone (S-, not in state)
- Its consumer is `{0: {[230239|0br]01e96bea1fec..}}` — the baseline branch
- The baseline `[230239|0br]` (entry #6) is InTheState

So the chain is:
```
[230238|0br]...[0] → consumed by [230238|88sq]...[0] → consumed by [230239|0br] (baseline)
```

Both `[230238|0br]` and `[230239|0br]` are InTheState. `[230238|88sq]` is NOT in state but IS in the past cone.

The conflict might be: output `[230238|0br]...[0]` is consumed by `[230238|88sq]`, but `[230238|88sq]` is NOT in the baseline state. If the baseline state has `[230238|0br]...[0]` as an unspent output (because `[230238|88sq]` wasn't committed in the baseline), then having `[230238|88sq]` consume it creates a conflict with the baseline's view.

Wait — that can't be right either. The baseline `[230239|0br]` consumes `[230238|0br]...[1]` (stem output), and `[230238|88sq]` consumes `[230238|0br]...[0]` (chain output). Both are in the past cone. The baseline was built on top of `[230238|88sq]` (since the baseline branch extends boot's chain through `[230238|88sq]`).

#### Key insight: explicit baseline from boot strategy

The conflict is clearly related to the **explicit baseline** set in transaction `[230239|46sq]0090062f6c7b..` (entry #7). This transaction consumes a very old output from slot 233 (`[230233|27sq]008044959046..`), which is 6 slots behind. This pattern — consuming an output many slots behind with an explicit baseline — is the signature of the **boot proposer** (`tryBootProposal` in `task/proposer_boot.go`).

The boot proposer creates transactions with an explicit baseline (the LRB) when the sequencer's own milestone is more than 1 slot behind. It fires when `extend.VID.Slot()+1 < targetTs.Slot`. However, in this context there was **no reason for the boot strategy to be active** — the network had active branches and sequencer transactions, the sequencer was not stale, and normal factory/base strategies should have been used instead.

**The boot strategy kicked in incorrectly.** This likely happened because `OwnLatestMilestoneOutput()` returned a stale milestone (from the tippool gap after branch submission), making the boot condition `extend.VID.Slot()+1 < targetTs.Slot` evaluate to true when it shouldn't have.

The explicit baseline introduced by the boot strategy creates a transaction whose past cone merges two independent state views — the explicit baseline (LRB) and the endorsement chain — which can have conflicting consumers for the same outputs.

#### Investigation needed

1. **Why the boot strategy fired**: Trace the conditions that led to `tryBootProposal` returning a proposal when normal strategies should have worked. Likely related to the tippool gap (branch not yet processed, OwnLatestMilestoneOutput returning stale data).

2. **Past cone reaction is inadequate**: The past cone logic's response to this kind of incorrectness (an explicit baseline that conflicts with the endorsement chain) should be investigated. Rather than producing a transaction that gets rejected as BAD with a conflict, the attacher or proposal validation should detect this incompatibility earlier and reject the proposal before submission. The fact that it was submitted, gossiped, and then rejected on access nodes means the conflict detection happens too late in the pipeline.

3. **Conflict detection specifics**: Trace the exact conflict detection logic to understand what condition `[230238|0br]010aea43e400..[0]` violates — whether it's a legitimate double-spend from merging incompatible state views, or a false positive from incomplete past cone traversal.

---

## Relationship Between the Two Issues

Both issues stem from Phase 2 changes creating scenarios that weren't common before:

- **Issue 1**: Non-baseline branches' inputs are not traversed → missing DEL mutations → token conservation violation. Root cause: branch short-circuit in `attachOutput` (line 591-594) that skips input traversal for non-state branch dependencies.
- **Issue 2**: Boot strategy fires incorrectly due to stale `OwnLatestMilestoneOutput()` (tippool gap after branch submission) → creates a transaction with explicit baseline that merges incompatible state views → conflict detected too late (after submission and gossip). Root cause: boot proposer condition check doesn't account for the tippool gap, and the past cone logic doesn't fail-fast on incompatible explicit baselines.

Both are triggered by the new timing characteristics of Phase 2 (plateau detection, longer intervals between submissions, tippool gap from fire-and-forget branch submission).

---

## Phase 2 Status

### What's deployed (testnet was running before crash)

Commits on `develop07-seq-improvement`:
1. `796ebe5a` — Phase 2 rewrite: event-driven sequencer with plateau detection
2. `5cdf8982` — fix: concurrent map access crash in txinput_queue
3. `db8f7ff7` — fix: deadline too tight and tight loop on failed branches
4. `3a8b425e` — fix: budget drain from repeated no-proposals in drain loop
5. `0f57adf1` — fix: allow base extend to fire on plateau when factory has no skeleton

### Uncommitted local changes (not deployed)

1. **Remove `adjustBudget(false)` from plateau path** — "no proposals" is not an overload signal, budget should only decrease on branch failures
2. **Increase `finalizationTicks` from 3 to 12** — the 3-tick (240ms) deadline was too short for `task.Run` to solidify and finalize, causing "context deadline exceeded" on most proposals

### Known issues to address (in order of priority)

1. **[CRITICAL] Token conservation crash** — Fix the branch short-circuit in `attachOutput`. This blocks all testnet operation.
2. **`finalizationTicks` too short** — Increase from `plateauHoldTicks` (3) to a separate `finalizationTicks` (12) constant. Without this, most proposals fail with context deadline exceeded.
3. **Budget drain** — Remove `adjustBudget(false)` from plateau detection path. "No proposals" on plateau is normal idle behavior, not overload.
4. **Alternating dead/active slots** — Even with fixes 2 and 3, the pattern may persist. The ~4 second branch processing time creates a gap where `OwnLatestMilestoneOutput()` returns stale data. Base extend can't work until the branch appears in the tippool.
5. **`branch: NONE` in SLOT STATS** — The branch submission is not tracked in slotData because the branch is for the NEXT slot but slotData is for the current slot. Cosmetic but confusing.
