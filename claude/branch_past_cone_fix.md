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
