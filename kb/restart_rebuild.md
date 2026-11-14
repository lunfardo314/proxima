# The restart stall: why a restarted node re-attaches committed history, and the fix

> **Status: BUILT on `fix/restart-rebuild-stall`, not yet on `develop`.** Written 2026-11-13; option A (section 5) approved by the user the same day, with a failed dependency retried against its own baseline, and built (`floorCanBeBaselineOf` and the retry in `core/attacher`). It changes how a dependency's baseline is chosen (`kb/dag_semantics.md` §1.3 and D1). Evidence and the session log are in `.internal/handoff_restart_rebuild_stall.md`.

## 1. The symptom

Some restarts of a node of the live network stall it for one to two minutes, and longer on experimental builds. Within ten seconds of the start the node runs about a thousand attachers of milestones more than 24 slots old (`att_past`). It commits no branch until they drain, falls up to 10-28 slots behind, and the GC then evicts vertices those attachers still need, which starts a reattach cascade of thousands. A sequencer node's chain balance is missing from the network's branches meanwhile: their coverage was about 75% for six slots during oloc2's stall.

On 2026-11-12/13 there were six stalls: hboot's access node at 17:49 and 18:18, hboot's sequencer at 19:19 and 19:28 (experimental builds), hloc0 at 10:10 and oloc2 at 16:41. Most other restarts were clean, and two in between delayed the node by 30-50 s without a cascade (hboot and oseq1 at 18:00). One was reproduced on a local access node on hdev (acc2, 16:08). Uptime, a restart of both nodes of a box together, and the node's state at shutdown were each ruled out as the cause.

## 2. The mechanism

Confirmed on acc2 with the attacher diagnostic (`[old solicit]`, `core/attacher/pull.go`), against the live DAG through the public node's explorer, and on oloc2's reattach edges.

1. **A first move carries a late milestone.** A sequencer's first move of a slot (tick about 9) often consumes its own last milestone of the previous slot, one that the slot's branch did not include. `kb/research/split_slots.md` describes it as the carry. Example: `s222770-9` endorses branch `s222770-0` and consumes `s222769-121`, which is not in that branch's past cone. The branches of 222771 include it.
2. **That milestone is attached by an attacher of its own, against its own baseline.** A running node attached `s222769-121` when it arrived; a restarted one attaches it now, as a dependency. Its baseline is branch 222769. Its delta against 222769 runs down its same-slot predecessors (`-104, -95, …, -9`), whose first move again consumes the previous slot's late milestone (`s222768-63`), and so on.
3. **Everything below the first step is already committed in the requester's baseline.** In the state of `s222770-0`, the baseline of the live milestone that started the walk, the explorer shows `s222769-9`, `s222768-63`, `s222768-9` and `s222767-33` all committed. The node attaches each of them in full anyway, because each is checked against the older baseline of the attacher that asked for it.
4. **The walk runs as long as the carry chains do.** Each step is one slot. It ended after 3 slots on acc1's clean restart, and ran 24+ on acc2, 40 on hloc0 and 86-128 on hboot. Short walks finish unnoticed. A walk longer than the GC's patience gets evicted mid-walk: under the ledger-time TTL (`memdag.ledgerTTLExpired`) a vertex more than 12 slots behind the latest branch is evicted once it has been in the memDAG 3 slots, protected only while its own attacher runs. Then come the reattach cascade and the full attacher cap.

Why earlier analysis missed it: the delta of every live milestone against its own baseline is 0-4 slots deep. Nothing live references old history. The depth comes from the chain of dependency attachers, each correctly validating against its own baseline. That is what D2 asks of each of them.

## 3. Why the existing floor does not stop it

`depAttachOpts` passes the requester's baseline down as a floor (`WithBaselineFloor`), and baseline solidification has a floor substitution: a dependency whose baseline direction is committed in the floor takes the floor as its baseline (`solidifyBaselineUnwrapped`, case `Undefined`). It never fires here, because it also requires `canBeBaselineOf(floor, tx)`: the floor must not be later than the transaction. That guards against a later branch that contains the transaction itself, where validating the transaction against it finds its own inputs spent and marks a valid transaction BAD. In this walk the floor is always the slot after the dependency, so the substitution is refused at every step. The past-cone in-state check (`defineInTheStateStatus`) never consults the floor.

## 4. Constraints

- **D2.** Each attacher walks its delta down to rooted vertices of its chosen baseline. Unchanged: the fix changes the baseline, not the walk.
- **M2.** Never shorten the walk to bound memory. Unchanged, for the same reason.
- **§1.3 and D1.** A milestone has one deterministic baseline, and derived values depend on the transactions and the chosen baseline. The fix gives some dependencies a different baseline than their producer used, so their recomputed values differ from a running node's. There is precedent: the forward-sync anchor after a snapshot restore and floor substitution #1 already attach milestones against a later branch, and `recomputedAgainstForeignBaseline` already skips the coverage-delta cross-check for them (`core/attacher/wrapup.go`; relaxed determinism near the snapshot, §2.4).
- **D4.** Committed state is authoritative. A transaction committed in a branch state is valid, and re-deriving that by walking is redundant.

## 5. Options

### A. The requester's floor as the baseline of a dependency it does not contain (recommended)

Extend floor substitution #2: a dependency may take a floor later than itself as its baseline when that floor does not contain it (`!BranchKnowsTransaction(floor, tx)`). That is exactly when the walk solicits it, so the hazard `canBeBaselineOf` guards against cannot arise. Each dependency then validates against the live baseline, and its walk stops at the first vertex that baseline commits: one slot deep, as on a running node.

- **Size.** A few lines in `solidifyBaselineUnwrapped`, plus the cross-check skip, which `recomputedAgainstForeignBaseline` already provides for a baseline later than the milestone.
- **D1 cost.** Such a dependency's coverage and score differ from the values a running node computed against its own baseline. They feed no consensus decision: the node is not sequencing on 10-slot-old milestones, and branch values come from committed state (D4). The monotonicity checks of its consumers compare against it; to verify.
- **A BAD verdict under a foreign baseline must not stick (decided: retry).** A dependency that conflicts with the floor is BAD against it, but may be valid on its own lineage. An honest requester's dependencies are all consistent with its baseline, so this can only come from an invalid requester, which could otherwise poison honest milestones on the node. A dependency failing against a later floor is therefore not marked BAD: its floor is cleared and it is attached again against its own baseline (`[floor retry]`, counter `floor_retry`).
- **Open: the walk inside the slot.** The same-slot milestones above the floor's frontier (`-104, …, -22`) are part of the live milestone's real delta and are still attached. That is the normal one-slot cost.

### B. The requester walks such dependencies itself

Instead of handing each undefined milestone dependency to an attacher of its own, the requester walks it against the requester's own baseline. The effect is the same as A, but it changes how milestones are attached throughout the attacher (`attachVertexNonBranch`, "don't go deeper for undefined sequencers"), and the dependency gets no verdict of its own. Larger and riskier than A for no gain.

### C. Keep the walk, protect it from GC

Keep walked vertices out of ledger-time eviction until the walk ends, a retention change that §2.5 and M2 allow. **Already tried and insufficient:** `3c264e55` (the ledger-time TTL waiting half the wall-clock TTL, reverted in `ad668738`) held off the cascade on hboot at 19:19 for about two minutes, then the deep walk outlived it and 7,265 reattaches followed. The full walk also still costs its thousands of attachers and the attacher cap. C is at most a complement to A.

## 6. Verification plan

1. **Unit test.** A dependency whose floor is later and does not contain it takes the floor as its baseline; one the floor contains still refuses it (the double-spend guard); a failure under the floor leaves no BAD verdict on the dependency.
2. **Core tests under `-race`;** the `tests/` restart and sync tests one at a time.
3. **Local access nodes** acc1 and acc2 on hdev (`.internal/acc_restart.sh`): repeated restarts on the build. Measures: `old_solicit` (counts milestones older than the memDAG TTL that attachers solicit) should stay near 0, `att_past` 0, first branch commit within about 15 s, no reattaches. Compare with the current build's restarts, which walk 3-24+ slots.
4. **One own node,** restarted once with a snapshot ready, before the rest.


## 7. Results

On the local access nodes on hdev, restarted onto the build on 2026-11-13: four restarts, all clean, including one of both nodes 26 s apart. A stall on the old build that day, for comparison: acc2 at 16:08, with 1,809 attachers (1,236 past) at 11 s, 2,032 reattaches and no branch commit for 99 s. On the build: at most 14 attachers in the first two minutes, no past attachers, no solicit of a milestone 8 or more slots old, no reattach and no retry; the first branch commit 2-6 s after start, and the node 1 slot behind within 30 s. The floor served as a baseline 2-7 times per start (`floor_baseline`) and never in steady state. The core packages and 13 integration tests on attachment, catch-up, restart and multi-sequencer runs pass under `-race`.
