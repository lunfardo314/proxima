# coverageDelta enforcement — sync wedge fix

Status: **PART B SHIPPED 2026-06-11** (uncommitted). Go-only change, no hardfork.
The on-chain rule (Part A) was deliberately left untouched. Testnet was stopped
2026-06-10 for this; can restart once nodes upgrade.

## What shipped (Part B)
- `core/attacher/wrapup.go enforceSeqCoverageDelta`: when the attach baseline is
  newer than the milestone itself (`a.finals.baseline.Slot() > a.vid.Slot()` —
  the snapshot-restore + forward-sync re-attachment case, impossible in
  real-time), the declared-vs-recomputed cross-check is **skipped** instead of
  rejecting. Silent unless the `sync` log topic is verbose
  (`a.WarnTopicf("sync", 1, …)`). Otherwise (real-time path) a genuine mismatch
  still rejects the milestone.
- Determinism-violation banner added to both off-chain stem cross-checks:
  `>>>>>>>> **************** VIOLATION OF DETERMINISM ****************** …` on the
  coverageDelta mismatch (`wrapup.go`) and the stem-value mismatch
  (`check.go enforceStemValues` `report` closure — covers BaselineRoot,
  FrozenCoverage, SlotInflation, TotalSupply, TotalCoverage, NumConfirmed*,
  NumSeq*).

## Open follow-up
The stem-value cross-checks in `check.go enforceStemValues` are also off-chain
recomputations of values carried on the wire. They run only for branch txs and a
branch's baseline is its predecessor branch (strictly earlier slot), so they do
NOT hit the same "baseline newer than self" wedge. But if a branch is
re-attached during sync against a foreign baseline (snapshot anchor rather than
its true predecessor), accumulated values (supply/coverage) could diverge.
Not observed yet; revisit if sync wedges on a stem-value mismatch.

## The incident (loc0-acc, 2026-06-10 ~14:20)

`proxima-loc0-acc.service` (63.250.56.190) was restarted, **restored from a
snapshot** whose LRB/anchor sat at slot 753 (`s753-0-01adb9371209`), and
forward-sync wedged permanently (fell from 64 to 100+ slots behind, never
recovering). Repeating log line: `[forward_sync] branch s754-0.. not yet
ready, stopping batch`.

Root rejection:
```
ERROR >>>>>>>> coverageDelta mismatch in milestone s752-27-004d9afb03e4..:
      computed=1_209_959_146_347 seqConstraint=998_842_324_939_269
WARN  ATTACH s752-27.. (baseline: s753-0-01adb9371209..) -> BAD(... coverageDelta mismatch ...)
```
`s752-27` (slot 752) → BAD, cascading BAD to every pulled branch
(s753-14, s753-27, s754-0 … s763, all `baseline: N/A`). Branch s754 can never
become "ready", so the whole batch is stuck.

The rest of the cluster was healthy throughout (seq nodes committing branches
normally). This is purely a **sync-path bug on a snapshot-restored node**.

## Root cause

Introduced yesterday by `a4169294` ("move coverageDelta to per-milestone
sequencer constraint") + gate restored in `cff91844`. Two enforcement halves
share the ledger constant `constEnforceCoverageDeltaMonotonicity`
(`EnforceCoverageDeltaMonotonicity`, default true):

1. **On-chain rule** — `_enforceCoverageAdvance` in `ledger/def/sequencer.easyfl:68`,
   applied from `_sequencer` at line 120. Compares **declared-vs-declared**:
   self's coverageDelta (constraint arg) must strictly exceed
   `_effectivePredCoverage` = the same-slot non-branch predecessor's
   coverageDelta (read from the input), else 0. Both values are producer-
   declared and relative to the **same baseline** (the easyfl comment at
   lines 51–54 spells out this assumption). This rule is internally consistent
   and **did NOT cause the wedge**.

2. **Go attacher cross-check** — `enforceSeqCoverageDelta` in
   `core/attacher/wrapup.go:48` (called from `wrapUpAttacher`, runs for EVERY
   milestone). Compares **declared-vs-recomputed**: the constraint's declared
   coverageDelta against `a.CoverageDelta()` =
   `pastCone.CoverageDeltaRaw(ctx, a.getBaselineStateReader)` + adjustment
   (`attacher.go:686`). The recomputed value is **baseline-relative**.

The bug: coverageDelta is only meaningful relative to the milestone's **own
canonical baseline** (the producer computed `998.8T` for `s752-27` against its
slot-752 baseline). After a snapshot restore the node's only committed branch
state is the anchor `s753-0`. When forward-sync drags `s752-27` into the past
cone of branch `s754`, its attacher resolves baseline to `s753-0` — *newer than
the milestone itself*. Recomputing `s752-27`'s coverage against `s753-0` yields
a tiny `1.2T` (most of its past cone is already rooted in s753-0). Declared
(998.8T, vs s752-0) ≠ recomputed (1.2T, vs s753-0) → wrongly rejected → wedge.

So: the **Go cross-check assumes the attachment baseline equals the producer's
baseline**, which is false during snapshot+forward-sync re-attachment of
pre-anchor milestones.

## The fix (per user instruction, 2026-06-10 — REVISED)

> Initial instruction: "Remove enforcement of coverage from the ledger
> constraints. Enforce it only in the node, snapshot-aware, otherwise warning."
>
> **REVISED same day:** on-ledger enforcement is fine (it is declared-vs-
> declared and internally consistent — it did NOT cause the wedge). So **keep
> the on-chain rule, NO hardfork. Do Part B ONLY.**

### Part A — DROPPED. Do NOT touch the EasyFL constraint.
Leave `_enforceCoverageAdvance` (`ledger/def/sequencer.easyfl:68`), its
application in `_sequencer` (line 120), the helpers, and the gate constant
`constEnforceCoverageDeltaMonotonicity` exactly as-is. The on-chain rule
compares the milestone's declared coverageDelta against its same-slot
predecessor's declared value (both producer-relative to the same baseline), so
it is sound and stays. No LibraryHash change, no hardfork, no test changes to
`coverage_delta_monotonicity_test.go`.

### Part B — node-side cross-check, snapshot/sync-aware (Go) — THE ONLY CHANGE
In `core/attacher/wrapup.go enforceSeqCoverageDelta`:
- When the attachment baseline differs from the milestone's canonical baseline
  — i.e. the snapshot/forward-sync re-attachment case — **do not reject**; emit
  a **WARN** and continue. Concretely: detect when the past-cone baseline slot
  is `>=` the milestone's own slot (a pre-anchor milestone re-attached against a
  newer anchor), or more generally when this is a force-commit/sync attach
  rather than real-time attachment.
- Only **reject (mark BAD)** in the normal real-time case where the baseline is
  the milestone's canonical one and the values genuinely should match.
- Net: a real mismatch on the live path still fails the milestone; the sync
  path can never wedge on this again.

Open design question to resolve while implementing: the cleanest signal for
"this is a sync re-attachment against a foreign baseline". Candidates: baseline
slot ≥ milestone slot (simple, catches the observed case); an explicit flag set
by `ForceCommitBranch`/forward-sync; or comparing against the milestone's
derived canonical baseline. Prefer the simplest that is provably correct.

## Validation
Part B is a Go-only change (no ledger/EasyFL touched). Run
`go test ./core/attacher/...`; `go test ./ledger/...` as a sanity check (should
be unaffected since the constraint is untouched).

## To unwedge loc0-acc when testnet restarts
It will never pass `s754` on its own. Either wipe its DB and restore from a
**fresh** snapshot at/after slot ~817, or run it with the gate disabled until
caught up. (Moot once Part A+B ship and all nodes upgrade past the hardfork.)

## Key code map
| What | Where |
|------|-------|
| On-chain strict-increase rule | `ledger/def/sequencer.easyfl:68` (`_enforceCoverageAdvance`), applied `:120` |
| effectivePred helper | `ledger/def/sequencer.easyfl:55` (`_effectivePredCoverage`) |
| Gate constant (easyfl) | `ledger/def/def_constants0.json:147` (`constEnforceCoverageDeltaMonotonicity`) |
| Gate constant (Go) | `ledger/def_constants0.go:37,70,128`; `ledger/constants.go:115`; `ledger/lib_singleton.go:458` |
| Go cross-check (THE wedge) | `core/attacher/wrapup.go:48` (`enforceSeqCoverageDelta`), called `:31` |
| Recomputed coverage | `core/attacher/attacher.go:686` (`CoverageDelta` → `CoverageDeltaRaw`) |
| BranchData source | `ledger/multistate/roots.go:277` (`bd.CoverageDelta = sc.CoverageDelta`) |
| forward-sync force-commit | `core/core_modules/forward_sync/sync.go:402` (`ForceCommitBranch`) |
| On-chain rule test | `ledger/tests/coverage_delta_monotonicity_test.go` |
| Introducing commits | `a4169294` (move to per-milestone + cross-check), `cff91844` (proposer gate) |
