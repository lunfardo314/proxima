# Branch past-cone leak — findings & fix plan (handoff for next session)

Status 2026-06-15 EOD. The `proxima_past_cone_size` / memDAG unbounded-growth leak.
Diagnosis is solid; fix approach agreed with the user but NOT yet implemented.
TEMPORARY working doc — fold the durable parts into a planned `dag_semantics.md`
(see end).

## Symptom

`proxima_past_cone_size` (size of the last sequencer milestone's past cone) and
`proxima_memDAG_numVerticesGauge` climb without bound (observed ~5100+ and rising,
GC `oldestSlot` frozen at the restart slot, `deleted ~ 0`). It is gradual
(~24/min) and reset by a restart, so a restarted node starts low and climbs back
over hours.

## Root cause (confirmed)

Two mechanisms, both about ROOTED vertices being retained in the past cone:

1. **Branch wrap-up re-attaches a clone of its whole past cone.** In
   `attacher_milestone.go` run() branch path (~line 175-184):
   ```go
   a.vid.ConvertToDetached()                                    // nils pastCone (correct)
   a.vid.SetTxStatusGood(a.pastCone.PastConeBase.CloneImmutable(), cov)  // re-attaches a strong-ref clone (LEAK)
   ```
   The detach nils the cone (natural/correct — a branch is committed state), then
   the very next line stores a `CloneImmutable` clone whose `vertices` map holds
   STRONG refs to every vertex. This pins the cone and lets it be merged forward.
   This clone line is OLD (regression from `bf4477f5`, Mar 2025; `git log -S
   CloneImmutable -- core/attacher/attacher_milestone.go`).

2. **`CheckAndClean` never trims BRANCH vertices.** `_checkVertex`
   (past_cone.go:1089) already removes a rooted vertex when
   `len(byIdx) > 0 && allConsumersAreInTheState` (all its consumers are themselves
   rooted). BUT branch vertices are exempted from trimming (per `856927cc` msg).
   So the **older baseline-ancestor branches** accumulate in every cone, and
   `MergePastCone` copies a predecessor's whole vertex set forward — propagating
   the growing rooted history into every successor milestone.

Net: each milestone drags the entire branch chain back to the oldest in-memory
branch; GC can't reclaim because everything is strong-ref pinned → frozen
`oldestSlot`, climbing size.

## Recent timeline (why it "appeared after a fix")

- `856927cc` (Jun 14 22:16) — fix: branches store NIL past cone
  (`SetTxStatusGoodBranch`, coverage only). Stops the leak.
- `6596b026` (Jun 14 22:30, 14 min later) — REVERTED it. Reason: it caused
  lagging nodes to **re-derive branch cones by walking deep** ("solidification
  reached depth"), so behind nodes fell further behind. (Caught-up nodes were
  fine.) The revert is why the leak is live again on develop `54919a2f`.
- The recursion that forced the revert is now separately bounded by `d4974cfc`
  (pull.go depth cap) — but the depth cap only prevents a CRASH, not the slowdown.

## The correct design (per user, 2026-06-15)

- A **branch is final committed state on DB**. Any UTXO's rootedness is a
  deterministic O(1) lookup in the branch's state (via the `branches` module /
  state reader). So a branch `WrappedTx` needs **no past cone** — and ideally not
  even cached coverage, since the **`branches` module already caches every
  branch's consolidated values** (coverage delta, supply, frozen, etc.). Read
  branch coverage from `branches`, not from the `WrappedTx`.
- **Finalized rooted branches in a cone are harmless** — they're `VirtualTx`,
  carry no past-cone references, do no harm. So they need not be force-removed for
  correctness; they're just dead weight when not contributing.
- **The cleanable category:** a vertex that is **rooted but NONE of its UTXOs are
  consumed by a not-rooted tx contributes nothing** to the baseline coverage
  delta and can be cleaned. The **older baseline-ancestor branch transactions are
  exactly this category** — and they're the ones currently never trimmed.
- **PRESERVE the solidification walk**: when a tx uses a branch as baseline, the
  attacher must still descend to all inputs rooted in that baseline state,
  regardless of timestamp. That termination-at-rooted is what makes coverage
  deterministic. Only the *retained* structure shrinks; the walk does not change.
- The `PastCone` is a **compressed cache** of that walk for fast conflict
  detection + mutation generation. Correct semantics: it need not touch / retain
  any vertex already rooted in the baseline that contributes nothing.

## Fix approach (agreed direction — targeted, NOT the full dual-rewrite)

1. **Let `CheckAndClean` clean rooted vertices that contribute nothing** — i.e.
   rooted vertices (INCLUDING ancestor branches) whose UTXOs are not consumed by
   any not-rooted tx. Keep the consumed boundary (those feed coverage/mutations)
   and keep the baseline branch itself. Concretely: remove (or relax) the
   branch-trim exemption, and clean rooted vertices with no
   consumed-by-not-rooted UTXO. `_checkVertex`'s `allConsumersAreInTheState`
   logic is the right basis; check the `len(byIdx) > 0` guard (a rooted vertex
   with NO in-cone consumers also contributes nothing but currently isn't
   removed).
2. **Branch wrap-up stops re-attaching the clone** — store coverage only (or
   nothing, reading from `branches`). This is the `856927cc` change; it is parked
   on branch `stop-branch-pastcone-leak` (cherry-pick `c272cd3b`) but must NOT be
   merged alone — on its own it triggers the deep re-derivation slowdown on
   behind nodes. It is safe only together with (1) + ensuring rootedness checks
   use the state reader (so behind nodes don't deep-walk).
3. Keep coverage/mutations/frozen computations correct. They currently iterate
   the rooted vertices to find consumed baseline outputs (`CoverageDeltaRaw`
   past_cone.go:1139, `Mutations` :696, `SequencerFrozenCoverageDelta` :1175). As
   long as the **consumed-boundary** rooted vertices stay in the cone, these keep
   working unchanged — so the targeted clean (1) is safe where my earlier trim was
   not. (A full rewrite to the input-side/dual form — iterate not-rooted txs'
   inputs into the baseline — would let ALL rooted vertices leave, but is bigger
   and riskier; defer unless (1)+(2) prove insufficient.)

## Failed attempts (do NOT repeat)

- **Trim `InTheState && slot < baselineSlot` from `CloneImmutable`** (parked
  stash earlier): broke coverage with a FATAL "coverage should not decrease along
  endorsement". WRONG criterion — it removed consumed-boundary vertices and acted
  on stale merge-relative `InTheState` flags. The correct criterion is
  "no UTXO consumed by a not-rooted tx", NOT "below baseline slot".
- **Branches store nil alone** (`856927cc` / `stop-branch-pastcone-leak`): fixes
  the leak metric (past_cone_size 5766→2 on loc0 today) but makes behind nodes
  re-derive branch cones by deep walking → they fall behind ("solidification
  reached depth", LRB stops advancing). Validated broken on loc0 today; reverted.
  Needs (1) + state-reader rootedness to be safe.

## Key code locations

- `core/attacher/attacher_milestone.go` run(): branch wrap-up ~175-184 (the
  `CloneImmutable` re-attach, line ~180); non-branch ~186-187 (sets
  `proxima_past_cone_size` via `EvidencePastConeSize`).
- `core/vertex/past_cone.go`: `CheckAndClean` (957), `_checkVertex` (1089,
  `canBeRemoved`), `Mutations` (696), `CoverageDeltaRaw` (1139),
  `SequencerFrozenCoverageDelta` (1175), `consumersByOutputIndex` (629),
  `consumedUTXOIndices` (663), `MergePastCone` (795). `IsInTheState` (421).
- `core/vertex/vid.go`: `SetTxStatusGood`/`SetTxStatusGoodNoLock` (209/216;
  note: does NOT store coverage when pastCone==nil), `convertToDetached` (120),
  the reverted `SetTxStatusGoodBranch` (on branch `stop-branch-pastcone-leak`).
- `core/core_modules/branches/branches.go`: caches branch consolidated values
  (`LedgerCoverage`, `Supply`, `Get`, `GetStateReaderForTheBranch`,
  `SnapshotKnowsTransaction` 723) — the authoritative source for branch
  rootedness + consolidated values.

## Validation approach

- Deploy to **loc0-acc first** (access node, can't push bad txs), then loc0
  sequencer. Watch BOTH: (a) `past_cone_size` plateaus at a few hundred (leak
  fixed), AND (b) the node stays caught up — no "solidification reached depth" /
  no LRB stall (the trap that broke the nil-only fix). A behind node must still
  catch up.
- If touching coverage/mutations math, add a TEMPORARY equality cross-check
  (compute old way vs new way, assert equal) before trusting it — determinism.

## Today's network state (context for tomorrow)

- Whole testnet migrated to develop `54919a2f` (monotonicity fix + depth cap +
  sync-config rename) and is healthy/synced; 2 sequencers producing ~18/min.
- loc0 + loc0-acc recovered onto develop. **loc0 sequencer** was stuck (LRB
  frozen 10959, forward-sync couldn't progress, "no proposals") — recovered by
  moving its multistate DB aside (`/home/nodes/loc0/proximadb.old.claude`) and
  letting it restore from a fresh network snapshot → caught up, producing again.
- loc0-acc has leftover DBs from experiments
  (`proximadb.bak.claude` / `proximadb.wedged.claude` /
  `proximadb.txstore.bak.claude`).
- `54919a2f` has NO leak fix → expect `past_cone_size` to climb back to ~5k over
  hours network-wide. Known/tolerated until this fix lands.
- Config migration gotcha (7f8ee596 → 54919a2f): `snapshot.enable_api` →
  `enable_download_api`; nested `sync.sources` → top-level `sources`; add
  `sync.disable: false`. Old key ⇒ forward-sync silently off.

## Separate (lower-priority) finding: snapshot-boundary coverageDelta

Earlier this session a freshly snapshot-restored loc0-acc appeared to wedge on
the first branch above the snapshot (coverageDelta computed ≪ declared). BUT today
loc0 and loc0-acc both restored from fresh snapshots and caught up cleanly on
`54919a2f`. So that wedge may have been an artifact of loc0-acc's mixed-version /
mangled state, not a reliable bug. Re-confirm before spending effort. Notes in
`claude/sync_reattach_wedge.md` + memory `project_sync_reattach_wedge`. The
monotonicity FATAL fix from that thread IS shipped on develop (`54919a2f`).

## TODO: write `claude/dag_semantics.md`

User requested a durable doc explaining the model from BOTH perspectives:
- **Pure DAG perspective**: branches = committed state; a tx's coverage is
  deterministic over the real DAG + its chosen baseline; the walk terminates at
  vertices rooted in the baseline (any timestamp).
- **Volatile memDAG perspective**: memDAG is a TTL-pruned async cache; PastCone is
  a compressed cache of the walk for conflict detection + mutation generation;
  what may be dropped (rooted-non-contributing) vs what must be kept (the
  not-rooted delta + consumed boundary); branches hold no cone (consolidated
  values live in the `branches` module).

## UPDATE 2026-06-16 — root precisely diagnosed via memDAG debug API (autonomous session)

Built read-only memDAG debug API (commit 3047d0d9): /debug/memdag/{census,vertices,vertex,pinners}
on a loopback debug port (config debug.memdag_port). Deployed to loc0/loc0-acc and diagnosed
the LIVE leak:

- Leak signature reproduced: oldestSlot frozen at restart slot, detached_in_map climbing to ~1740.
- The leaked cohort (lag>25): 1705 vertices, ALL GOOD, **0 ref_by_sequencer**, incl. **474 branches**.
- Oldest pinned vertices are non-branch sequencer milestones with **has_past_cone=true** and
  **pastConeSize ~1400** (recent healthy milestones: ~220). The retained PastConeBase.vertices map
  strong-refs the whole cohort. FindPinners showed #pinners=0 for an old branch because the tool
  does NOT scan pastCone membership (a known blind spot) — the pin IS pastCone retention.
- Anomaly: cohort is detached_in_map=true (map strong-ref nil) yet kind=vertex (pastCone intact) =
  detach→REATTACH churn (orphan catch-up milestones perpetually re-derived into new past cones).

ROOT: non-branch milestone pastCones retain old ancestor branches + rooted boundary that contribute
nothing; MergePastCone propagates them forward; CheckAndClean never trims them (branch-exempt at
past_cone.go ~993). => handoff "fix #1".

Fix #1a SHIPPED this session (commit b5dbb042): RegisterBranchVertices now registers only the
committed delta (PastConeBase.CommittedVertexSet), not the full VertexSet. Correct + removed the
branchVertices pin (pinners #branches went 6->0), but SECONDARY — leak persists via the pastCone pin.

NEXT: implement CheckAndClean trim (fix #1 proper) with criterion "rooted AND no not-rooted consumer"
(NOT slot<baseline — that broke coverage FATAL before), incl. ancestor branches, keep baseline+tip.
Add temporary coverage equality cross-check. Validate on loc0-acc first.

## INCIDENT + LESSONS 2026-06-16 EOD — trim crashed the network, REVERTED

The CheckAndClean trim (8a010fef + b9a48762) was deployed fleet-wide and caused a
**consensus-breaking conservation crash**. loc0 FATAL:
`_commitPendingBranchUnlocked(s19449) -> updateTrie: major inconsistency.
input(900_447_414_807_960) + inflation(79_024_224) != output(901_486_097_952_584)`.
Other nodes rejected the bad branches ("violation of determinism").

REVERTED on develop -> `6318ca52` (back to b5dbb042 behavior: branch-exempt CheckAndClean,
leaky-but-stable). loc0/loc0-acc redeployed on the revert. Fix preserved on branch
`trim-fix-wip` (@ b9a48762). Debug API stays on develop. User stopping all nodes; resume tomorrow.

### Root of the crash
Removing an IN-STATE **branch** (or any in-state vertex with `byIdx==0`) from the past cone
loses its **DEL mutation** -> conservation breaks at branch commit (input+inflation != output).
This is exactly what f860eac7 named: the branch's stem is consumed by the NEXT branch, which is
NOT in pc.vertices, so the consumption is INVISIBLE (`byIdx==0`), `canBeRemoved=true`, the branch
is dropped, and its DEL is never generated.

### Why my safeguards missed it
1. **Phase 1 does NOT cover this.** Phase 1 (_removeOrphanedBranchSubtrees) catches NOT-in-state
   COMPETING branches (conflict evidence). The crash is from removing an IN-STATE (rooted, lineage)
   branch whose DEL is needed — a DIFFERENT failure mode. My claim "Phase 1 supersedes the exemption"
   was WRONG: it supersedes the conflict-evidence half of f860eac7, not the lost-DEL-mutation half.
2. **The coverage-only cross-check was insufficient.** It verified coverage invariance (and fired 0x
   — coverage really was invariant), but the trim changed the **mutation set** without changing
   coverage. MUST also cross-check Mutations()/conservation, not just CoverageDeltaRaw.

### The specific unsafe change
`canBeRemoved = inTheState && allConsumersAreInTheState` newly allowed removing in-state vertices
with `byIdx==0` (no IN-CONE consumer). That is the trap: an invisible consumer OUTSIDE the cone
(the next branch / baseline consuming the stem) still needs the DEL. Branches are the prominent
case; any in-state vertex with an out-of-cone consumer is at risk.

### What is still solid
- Leak diagnosis: pastCone member-accumulation via MergePastCone, CheckAndClean not trimming. Solid.
- The b5dbb042 part (RegisterBranchVertices = committed delta) is stable, kept on develop.
- The debug API is the right tool; keep using it.

### Direction for tomorrow (user's idea)
Re-add the branch **exemption from purging** (keep branches in the cone for DEL + conflict evidence),
but stop branch **accumulation at the MERGE** instead: do not carry old branches (below the new
baseline) forward in MergePastCone, rather than purging them in CheckAndClean. That kills the leak
(no accumulation) without dropping branches that a cone still needs for mutations.

### HARD REQUIREMENT for any retry
Add a **mutation-conservation cross-check**, not just coverage: compute Mutations() (or assert
input+inflation==output for a test branch) over the cone before vs after any trim/merge change, and
assert identical. Validate on **loc0-acc (access node) FIRST**, watch for the conservation assert,
before ANY sequencer node. The coverage cross-check alone is NOT sufficient.
