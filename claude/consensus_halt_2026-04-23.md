# Consensus halt investigation — 2026-04-23

Snapshot of a live testnet stuck in a consensus halt, saved mid-investigation.
Testnet is running untouched; do not disturb without discussing with user first.

## Two independent incidents

### A. loc1-acc crash (Apr 22, 17:30 UTC) — ROOT CAUSE FIXED

Panic: nil-pointer deref at `ledger/multistate/mutate.go:299` in `HasDeletedTx`,
called from `branches.branchKnowsTransactionCompute` (branches.go:693) via
`attacher.defineInTheStateStatus` (attacher.go:461).

Root cause: data race on `pb.Mutations` between `_commitPendingBranchUnlocked`
(runs without b.mutex; appends upgrade-inject / GC DeleteTxIDs; writes GCSlot)
and concurrent readers — `virtualStateReader` (no lock) and
`branchKnowsTransactionCompute` / `GetChainOutputFromBranch` (under b.mutex,
which the writer does not hold). Torn append leaves an interface slot with
itab=`*mutationDelTx` but data=nil → typed-nil assertion succeeds → `delTx.ID`
derefs nil → `SIGSEGV addr=0x0`.

Fix committed as `ff05e018` on `develop07-peering` (pushed). Introduces
`Mutations.Clone()` and makes `_commitPendingBranchUnlocked` mutate a clone.
`virtual_state_reader.go` header comment updated to document the invariant.
Tested via `go test -race ./ledger/multistate/...`.

**NOT DEPLOYED to testnet yet** — binaries on the 4 boxes are pre-fix.
loc1-acc systemd service is `failed` (stopped retrying ~20:33 UTC).

### B. Network-wide consensus halt (Apr 23, 09:14 CEST / 07:14 UTC) — UNSOLVED

All 4 sequencers (boot, loc0, seq1, loc1) stopped producing branches at the
same minute. Simultaneous, not caused by the loc1-acc crash (loc1-acc had been
down ~13 h at that point). Tx receive rate dropped ~8 → ~1.14 tx/s
(heartbeats only).

Network is idle (no spammer / faucet). Just 4 sequencers on lowest-possible
load. User says this is a **liveness** problem — sequencers should guarantee
liveness and they're not.

## Current stuck state (preserved)

- LRB: slot **344452**, branch id
  `00054184010113627022bcfc748e9b7f5a0e0680f56c9432f1e14facf4d2c877`,
  root `95f80fac32648ff8cea60607e5b9f13013229a25940c3174ebf4ef083533feb9`,
  sequencer_id boot (`9d2c6fedeb0f…`), `stem_output_index: 1`,
  `sequencer_output_index: 0`. Only one branch known at slot 344452
  (`get_branch_list?from_slot=344452` → 1 entry).
- Every sequencer on every slot attempt fails identically:
  ```
  tryBranchProposal-<seq>[N|0]: ATTACHER_FAIL target=N
    extend=[N-1|12sq]…[0] extSlot=N-1 extIsBranch=false extBaselineSlot=344452
    err=checkOutputInTheState: output [344452|0br]0113627022bc..[1] is already consumed
  ```
  Output `[344452|0br]…[1]` is the **stem output** of the LRB.
- Current slot is advancing (sequencer chain extends: 344850, 344852, …,
  344928+ at the time of the snapshot). Only BRANCH commits are stuck.
- Per Prometheus: `backlog_size=0`, `wait=0` on all sequencers;
  LRB slots-behind growing at ~6/min.

## The puzzle

The `ATTACHER_FAIL` claims the stem output is not in state, yet the same node's
API `GET /api/v1/get_output?id=…87701` **returns the output data** — proof the
committed LRB trie contains the stem.

Code paths:
- API: `node/apiserver.go:114 LatestReliableState` → `Branches().FindLatestReliableBranch()`
  → `Branches().GetStateReaderForTheBranch(lrb.TxID())`.
- Attacher: `core/attacher/attacher_incremental.go` sets
  `getBaselineStateReader = ret.Branches().GetVirtualStateReaderForTheBranch`
  (lines 80, 122, 164). `checkOutputInTheState` (attacher.go:594) calls
  `rdr.GetUTXO(oid)` via that.

For a committed branch, `GetVirtualStateReaderForTheBranch` should return a
fresh `MustNewReadable` on the same committed root — i.e. equivalent to the
API path. But they disagree.

`Readable.GetUTXO` (state.go:185) consults a per-Readable txrecord bitmap cache
(`_lookupTxRecord`) before hitting the trie. If the tx record says index 1 is
spent, it returns not-found even if the trie row exists. That short-circuit is
the most likely place for the divergence — but both paths start from fresh
readers, so the cache can't itself be stale across paths.

## Open hypotheses (in order of plausibility)

1. **The attacher's baseline is not the LRB.** The log prints
   `extBaselineSlot=344452` but not the full branch id. If the extending
   sequencer chain's `BaselineBranch()` returns a txid at slot 344452 that is
   *not* the LRB (an orphan sibling we can't see in b.m), the virtual state
   reader walks a different state. `get_branch_list` shows only the LRB, but
   that might reflect committed-state memory only.
2. **344452 is (wrongly) in `b.pending` on the sequencers.** If so,
   `GetVirtualStateReaderForTheBranch(344452)` takes the
   `buildVirtualStateReader` branch and overlays pending mutations on an older
   ancestor. If those mutations don't add `[344452|0br]…[1]` explicitly, the
   overlay returns not-found. The race fix (committed but not deployed) could
   remove a mechanism by which this sticks.
3. **Trie/bitmap inconsistency in the committed 344452 record.** Unlikely
   given the API already returned the stem output — both paths go through
   the same bitmap short-circuit.

## Diagnostic plan — resume here next session

The log's missing piece is the full baseline branch id. One-minute change:
modify `sequencer/task/proposer_base.go:46` to include
`a.BaselineBranch().StringHex()` and a `b.pending`-contains flag. Build.
Deploy to **seq1 only**. `systemctl restart proxima-seq1`. The other 3
sequencers stay untouched for comparison. seq1 will re-sync to LRB 344452 in
seconds (LRB is on disk) and hit the same failure — now with full baseline id
visible.

Interpretation:
- If baseline id == LRB and not pending → attacker-side bug in the committed
  reader path; dig into `_getRootForCommittedBranch` and the bitmap cache.
- If baseline id != LRB → a sibling 344452 branch is held somewhere in memDAG
  / pastCone; investigate how it got there.
- If baseline id == LRB and IS in `b.pending` → `b.pending` corruption;
  likely downstream of the race bug fixed in `ff05e018`.

If diagnostic points at (2)/(3), the race-fix deploy (full rollout of
develop07-peering + restart all 4 sequencers) becomes the right remediation.

## Operational setup done during this session

- User added `lunfardo ALL=(ALL) NOPASSWD: ALL` on all 4 testnet boxes
  (boot, loc0, seq1, loc1). `sudo -n journalctl/systemctl` works for the
  Claude session without prompting. Revoke with
  `sudo rm /etc/sudoers.d/lunfardo-nopasswd` on each box.
- pprof is enabled and reachable on port 8080 on each node
  (`pprof.external_access_enabled: true` in proxima.yaml). Useful for
  goroutine dumps without restart.

## Closing the investigation — outcome and fixes (end of 2026-04-23)

The corrupted in-memory state on boot/loc0/loc1 did not produce any additional
insight beyond "the `pb.Mutations` race leaves b.pending/b.m inconsistent; the
inconsistency prevents branch proposals." Further poking of their state was
ruled out in favour of a clean-state restart against the fixed binary.

### What shipped on `develop07-peering`

- `ff05e018` — race fix on `pb.Mutations` during deferred branch commit
  (`Mutations.Clone()`). This is the **root cause fix**.
- `622f486f` — enriched `ATTACHER_FAIL` / `checkOutputInTheState` logs with
  full baseline branch id, `b.pending` flag, root hex. Low-cost permanent
  diagnostic for future state-corruption incidents.
- `726c8128`, `511e5a1a`, `dedde301` — three read-only debug endpoints
  (`/api/v1/debug_compare_readers`, `/api/v1/debug_branches_at_slot`,
  `/api/v1/debug_pending_branches`) plus helpers on `Branches` / `ProximaNode`,
  **gated behind `debug.enable` config flag**.
- `c2e3bac4` — `bootstrapOwnMilestoneOutput` always starts from the LRB
  rather than iterating tippool-reported milestones. Bounds the attacher
  past-cone walk at the LRB's committed state; eliminates the cold-start
  deadlock path that tripped the sequencer watchdog during the investigation.

### What was dropped

- Tolerance bumps (`selfAttachmentLatencyToleranceTicks`, `deadlockTolerance`)
  — those were diagnostic work-arounds. With the boot-proposer fix the
  underlying blocking path is gone, so the watchdog's default 30 s is fine.
- Task ctx plumbing through `AttachTxID` / `pullIfNeeded` — was going to be
  belt-and-suspenders for bounded `task.Run` wall-clock; decided unnecessary
  once the primary blocking path was closed. File as a follow-up only if we
  see the watchdog fire again under load.

### Testnet reset plan (executed in this session)

1. Final `go build` + `go test -race` on `develop07-peering`.
2. Push `develop07-peering`.
3. Revert the seq1 diag-session local changes (restore original
   `proxima.yaml`, original binary, systemd unit) so the reset uses the
   real production posture.
4. Stop all 4 nodes, wipe DB + txstore + txlog on each, snapshot-restore
   from the genesis snapshot, restart in order `boot → loc0 → seq1 → loc1`.
5. Re-enable spammer / faucet once all 4 are healthy.
6. Watch `proxima_lrb_slots_behind` (should stay ≤ 2), `proxima_general_gauge_att`
   (should stay bounded), and the newly-enriched `ATTACHER_FAIL` log lines
   (should never fire). Any recurrence has the full diag toolkit available
   via `debug.enable: true` on one node.

## Key files for next-session reference

- `core/core_modules/branches/branches.go` — branch commit lifecycle, the
  `_commitPendingBranchUnlocked` / `GetStateReaderForTheBranch` pair
- `core/core_modules/branches/virtual_state_reader.go` — overlay reader over
  pending mutations (docstring now accurate as of `ff05e018`)
- `core/attacher/attacher.go:594` — `checkOutputInTheState` (the failing check)
- `core/attacher/attacher_incremental.go` — sets the virtual reader as the
  baseline state reader for incremental attacher paths
- `sequencer/task/proposer_base.go:46` — `ATTACHER_FAIL` log line
- `ledger/multistate/state.go:150–197` — `_lookupTxRecord` + `GetUTXO` bitmap
  short-circuit
- `ledger/multistate/mutate.go` — `Mutations.Clone` (added in `ff05e018`)
