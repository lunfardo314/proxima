# Wedge: pending branch invisible to DB-only baseline lookup (bug-hunting artefact)

**FIXED** develop (2026-07-02). Kept as a bug-hunting artefact; the fix is one line.

## Symptom

hloc0-seq stalled at slot 32512 during a fleet spam: LRB frozen, every milestone
32512→tip failing `conflicting branch endorsement s32512-0`, `baseline: N/A`.
DAG-verified NOT a fork (single canonical branch at 32512, no competitor), not load
(light branch), not the fork-detection changes.

## Root cause

Branch commits are **deferred**: `AddPendingBranch` puts the branch in `Branches.b.m`
with `Root==nil`; the DB write happens lazily, only when someone requests the branch's
state reader (`GetStateReaderForTheBranch`, the sole trigger).

`attach.go` resolved a branch txid with DB-only `multistate.FetchBranchData`, which is
**blind to pending branches**. A successor milestone adopting a still-pending branch B
as its baseline missed it → cached a **not-`Good`** virtual vertex for B in the memDAG.
That vertex poisons everything: `AttachTxID` short-circuits on it and never re-derives
`Good`, so B's baseline stays N/A, and `GetStateReaderForTheBranch(B)` (which would
flush B) is never called — so B never even commits. Permanent wedge; the
`conflicting branch endorsement` error is a misnamed unresolved-baseline.

## Why it never appeared before

`a43bef91 "todo fix cached branches"` swapped `env.Branches().Get()` → DB-only
`FetchBranchData` as a stopgap to dodge a then-existing recursion in the cache
(`getNoLock → calcAndCacheLedgerCoverage → getNoLock`). Before that, the pending branch
was visible via `b.m`. The recursion is long gone (coverage now read from stemLock
`TotalCoverage`), but the stopgap stayed. It only bites when a milestone references a
branch inside its pending window — a fast multi-seq spam burst.

## Fix

`core/attacher/attach.go`: read via `env.Branches().Get(txid)` (includes pending
branches) instead of DB-only `FetchBranchData`. `Get()` does not force a commit, so
lazy commit is preserved — the flush still happens only at first state-reader access.
`newVirtualBranchTx` never reads `Root`, so wrapping a pending branch (Stem/SequencerOutput
set, Root nil) as `Good` is safe. Validated: `go test -race ./core/...` and the multi-seq
branch/baseline suite (`Test5SequencersIdlePruner`, `Test3Seq*TagAlong`).
