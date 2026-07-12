# TODO — backlog

This file contains TODO list for future Claude sessions.

## Pre-branch consolidation: exempt the branch from sequencer pace (breaking ledger change)

Goal: eliminate the residual sequencer canonical-branch bias. Background and
current state in memory `project_seq_prebranch_consolidation_bias.md`. Shipped
so far (`830abb40`): in the pre-branch zone the sequencer holds, builds ONE final
consolidation as late as pace allows (tick `boundary - pace` = 116), then branches.
This dropped the top sequencer's win share 50%→40% and made ~47% of slots reach
exactly-equal coverage delta — but convergence is only partial because the final
milestone is capped at tick 116.

Desired end state: the sequencer keeps consolidating from the tippool until the
LAST tick (127) and issues the single consolidation milestone THEN, immediately
followed by the branch. Rationale: the ledger's ≤1-input pre-branch rule means no
NEW coverage can enter the zone (only consolidation of existing), so one
consolidation at the very last tick maximizes synchronization across sequencers →
coverage deltas equal as the norm → the fair VRF branch-inflation bonus decides
the winner.

Blocker (the actual TODO): a milestone at tick 127 → branch at tick 0 (absolute
128) is only 1 tick apart, violating the 12-tick sequencer pace on the branch's
consumed chain-predecessor input (`ledger/transaction/parse.go` scanInputs,
`isSequencer` branch). **Exempt the branch's chain-predecessor from the sequencer
pace constraint** (keep monotonicity + the cross-slot chain-transition rules).
Breaking ledger change → coordinated hardfork redeploy. Then update
`sequencer/strategy_async.go` to target the last tick (127) instead of
`boundary - pace`.

Option to evaluate alongside: **make sequencer pace 3 ticks in general**
(`defaultTransactionPaceSequencer` in `ledger/def_constants0.json` /
`ledger/def_constants0.go`, currently 12). A smaller pace shortens the gap between
the last feasible consolidation and the branch (last milestone at `boundary - 3` =
tick 125 without any exemption), which alone would tighten convergence, and also
raises milestone throughput. Also a breaking ledger change; weigh against
increased tx/branch rate and self-attachment-latency pressure.

## Snapshot protocol

- **Don't rush snapshotting right after start.** Wait at least ~30 slots after the
  node is synced before writing a snapshot. A snapshot taken before the node's own
  chain state has settled can lock the node onto a short-lived branch that peers
  never confirm.
- **Snapshot selection rules (at restore time):**
  1. Reject snapshots younger than ~60 slots relative to wall-clock — too recent
     to be safely common across the network.
  2. ..

## Sync
Revisit weird behavior with syncing after warm restart. 
- forward syncing may be interfering when there's no need of it

## Sequencer self-attachment latency threshold (96 ms)

`selfAttachmentLatencyToleranceTicks = 12` in `sequencer/strategy_async.go`,
i.e. 96 ms at the default 8 ms tick. Multiple factory tests
(`TestFactoryNonDecreasingCoverage` etc.) log `sequencer throttled:
self-attachment latency 200+ ms exceeds tolerance 96 ms` warnings during
normal operation — branches still attach and the tests pass, but the
sequencer pauses submissions until the pending milestone clears.

The 200 ms observed wall-clock is pulse-cycle-related
(`pulseInterval = pace × tickDuration`), not the raw validation work —
the canonical-P / EasyFL hot path was profiled during the branch_cost.md
work and proven to take much less. See "Lessons learned" in
`claude/branch_cost.md` for the profile evidence.

The threshold (12 ticks) looks tight for default config: a single missed
pulse pushes any pending milestone over the limit. Investigate:

- whether the threshold should be widened (e.g. 16–24 ticks);
- whether `pulseInterval` itself can be tightened;
- whether the throttle should distinguish "pending milestone genuinely
  not attaching" vs "pending milestone attached but next pulse hasn't
  fired yet";
- whether the warning should be suppressed under normal-noise conditions
  to avoid alarming readers.

## Cheaper branches (orthogonal to branch-cost design)

Per-branch DB-commit and validation cost reduction. Independent of
whatever spam-control direction the branch-issuance-cost question is
eventually answered with — see `claude/branch_cost.md` § "Future
directions" item 4. Concrete sub-items:

- **State-delta compression** on persisted branch commits.
- **Batched / async commit** of branch ledger-state deltas.
- **Reduce EasyFL hot paths** that fire on every produced sequencer /
  stem output (e.g. canonical-P-style nested tuple manipulation if a
  mineable-bonus design returns).
- **Profile-driven tuning of the sequencer self-attachment latency
  threshold** (see the "96 ms self-attachment latency" entry above —
  same root cause from a different angle).

Net effect: even with `O(N)` branches per slot, per-node steady-state
cost stays bounded as `N` grows.

## Tools

- limit number of dagviz connection (it is already the case). Add clear message for the user if that is the case
- Default of the dagviz connection time let be 20 min

## Dust attack vector from arbitrary locks

After the UTXO indexing refactor (slot 2 = arbitrary EasyFL bytecode), any
EasyFL author can ship a lock that bypasses `selfRequireEnoughStorageDeposit` /
`selfEnforceZeroAmountsInNonChainedOutput`. That opens a dust spam vector:
cheap-to-create UTXOs accumulate indefinitely in the trie state.

The library locks (`sigLock`, `chainLock`, `tagAlong`, `delegateLock`,
`stemLock`, and the new `htlc`) all enforce these checks themselves, but we
cannot rely on the lock to police itself once arbitrary locks are admitted.

Action: enforce a minimum-storage-deposit and zero-non-chain-amounts rule on
every produced UTXO at the **Go level** (i.e. unconditionally in the
transaction validator), with a small exemption set (chained outputs already
allow non-zero inflation/frozen coverage; stem may need its own carve-out).
Likely lives next to `EnoughAmountForStorageDeposit` in `output.go` /
`txbuilder/`. Drop the per-lock `selfRequireEnoughStorageDeposit` calls once
the framework rule is authoritative.

# Upcoming ledger refactor

## Needed for bridging
(needs refinement)
- include coverage delta, supply, baseline root into the stem. Enforce at the node level. Remove it from the metadata. Probably remove the persistent metadata as such
- remove persistent TxMetadata as concept
- refactor locks and indexing in the ledger, replace with tuple of indices pos 1 + the lock constraint pos 2 + chain pos 3. Remove lock serialization   
- Expose Merkle proof in the Readable
- delegation constants per chained account rather than global — spec at [claude/delegation_epoch_params.md](delegation_epoch_params.md). DEFERRED. Current thinking: bumping the two global constants (`constDelegationEpochSlots`, `constDelegationMaxFrozenEpochs`) may be sufficient. If we do move them, possibly only `maxFrozenEpochs` needs to be per-target (epochSlots could stay global). Revisit when there's a concrete need.
- support native token constraints on the amounts vector
- Remove plain data list element at the tx tuple level
- I implement evidenceHash(hashPrefix, data) enforcer hasPrefix(hash(data), hashPrefix). Use it in the enforced script list at the txLevel.
- Implement validateWithRedeemed(index of evidenceHash() bytecode, redeemed lib hash prefix, lib tuple index called function, args …). It will compare hashes and call library. The idea is not to run hash function for each revocation
- Library compilation caching
- Inclusion proof validation embedded opcode
- Implement open lock as plain index data list value. The index will be the evaluated data. Unlockable by anybody. Consider randomization of the unlock slot, e.g. by hash(public key||UTXO ID||slot) mod 5 == 0
Another option. Interpret open lock data as tuple of index values

## Audit conditional locks: delegate to `sigLock` / `chainLock` where fallback is equivalent — DONE

When a lock's conditional fallback path is meant to behave "like an ordinary
sigLock for the issuer" (or chainLock for a chain), the body should invoke
`sigLock` / `_sigLock($holder)` (or `chainLock` / `_chainLock($id)`) rather
than hand-rolling a `txHolderID == issuer` / chain-id equality. Calling the
real thing picks up unlock-by-reference for free, keeps semantics in lockstep,
and shrinks the lock body.

Audit results:

- `lock_tag_along.easyfl` ✓ target window → `_chainLock($0)`, sender reclaim → `_sigLock($1)`.
- `lock_send_with_deadline.easyfl` ✓ target window → `_sigLock`/`_chainLock` (per targetType), master reclaim → `_sigLock($1)`.
- `lock_delegate.easyfl` ✓ master path → `_sigLock($1)`, target path → `_chainLock($0)`. Frozen/on-hold/safe-revocation gymnastics are delegation-specific, not redundant sigLock logic.
- `lock_chain.easyfl` ✓ baseline; nothing to delegate.
- `lock_signature.easyfl` ✓ baseline; nothing to delegate.
- `lock_stem.easyfl` ✓ uses `signaturePublicKey(txSignatureData)` for VRF proof, stem-specific, not a sigLock fallback.
- `timelock.easyfl` (htlc) ❌ → ✓ — signature path was `equal($0, txHolderID(txSignatureData))`; refactored to `_sigLock($0)`. Both HTLC tests still pass; reference-unlock now works on the post-deadline path for free.

Produce-side `equal(masterID, txHolderID(txSignatureData))` checks in tagAlong /
sendWithDeadline / dex order locks are NOT refactor candidates — they bind the
issuer at create time, the lock element at output position 2 may not be sigLock,
and sigLock's produce-side rules (e.g. `selfEnforceZeroAmountsInNonChainedOutput`)
would be inappropriate to import wholesale.

Reference: `examples/dex/dex.easyfl` — sell/buy order reclaim windows just call
`sigLock`. Bundle shrank ~110 bytes vs. the hand-rolled version.

## INVESTIGATE: intermittent FATAL "ledger coverage should not decrease along endorsement"

Source: `core/attacher/check.go:50` inside `checkConsistencyBeforeWrapUp()`.
Fires when the milestone attacher's recomputed `FinalLedgerCoverage` is less
than the coverage of one of the endorsed (non-branch) milestones.

Surfaced during `go test ./tests/...` in the delegation_epoch_params refactor
session (2026-05-17). Behaviour observed:

- Reproduced twice running `go test ./tests/... -count=1 -timeout 900s` without
  `-v`, in concurrent multi-sequencer scenarios (boot + seq0..seq3, around
  slot 13–15).
- Did NOT repro on the immediately-following `-v` run on the same codebase
  (`go test ./tests/... -count=1 -timeout 900s -v` — all 30+ tests green).
- Verbosity-dependent reproduction strongly suggests a race / timing issue
  rather than a deterministic logic bug. Output buffering and goroutine
  scheduling differ subtly between -v and non-v runs.

The FATAL is invoked from a goroutine inside `core/attacher.AttachTransaction`
→ `runMilestoneAttacher` → `milestoneAttacher.run` → `checkConsistencyBeforeWrapUp`.
On firing it calls `global.Fatalf` which crashes the whole test process
(hence the surrounding integration-suite FAIL).

Why this matters:

- The invariant is a real protocol soundness property — endorsements must NOT
  decrease coverage. If the check fires legitimately, something allowed an
  endorsement to be issued against a lower-coverage milestone.
- More likely: a transient stale-state read during the attacher's wrap-up
  ordering (similar in spirit to `ErrAttacherTransientStaleState` already
  handled a few lines above for cleared coverage). The check should perhaps
  retry / bail with a transient error instead of FATAL-ing the process.

Investigation entry points:

- `core/attacher/check.go:35-55` — the loop walking endorsements. Note the
  existing `ErrAttacherTransientStaleState` branch on `lcEnd == nil`. A
  similar transient guard may be needed when `lcCalc` is read before all
  inputs to `FinalLedgerCoverage` have stabilised.
- `core/attacher/attacher_milestone.go:154` — caller. Compare how the
  transient error is propagated up the stack vs. the current FATAL path.
- The Phase 5b refactor (`delegateLockState` at last position) changed how
  the chain's frozen-coverage vector is sourced for non-delegation chain
  outputs. Unlikely to be the cause (the FATAL path doesn't touch
  delegationParams; coverage is computed independently of per-target
  params), but worth ruling out by reproducing the FATAL on `ba7d0559` (the
  parent of the layout change).

Reproduction hint: run `go test ./tests/... -count=1 -timeout 900s` (no -v)
in a loop until it fires; capture the surrounding lines and the attacher
dump (`---- attacher lines ----`) for the failing milestone.

Not fixed in the delegation_epoch_params refactor; this TODO captures it
for a focused follow-up.


## proxi `-c` / `--config` profile flag is ignored — FIXED 2026-06-10

Root cause: `config` was bound to viper per-subcommand (`nodeCmd`,
`snapshotCmd`, and five `util` subcommands), each `viper.BindPFlag("config",
...)` passing its OWN `*pflag.Flag`. viper keeps only the LAST binding for a key
in its global state, so `viper.GetString("config")` always read back the flag
object of whichever `Init()` ran last (snapshot_cmd) — never the one the value
was actually parsed onto. Result: the parsed `-c` value was unreachable and
`ReadInConfig` always fell back to the `proxi` default.

Fix: bind `config` / `-c` exactly ONCE as a persistent flag on the root command
in `proxi/main.go` (alongside `verbose`/`v2`/`force`), so it inherits to every
subcommand and there is a single viper binding. Removed all per-subcommand
`config` flag definitions and binds (`node_cmd.go`, `snapshot.go`,
`util_parse_tx.go`, `util_ledger_def.go`, `inflation.go`,
`util_verify_ledger_def.go`, `util_compile_ledger_def.go`) and their now-unused
`viper` imports.

Verified: `proxi node balance -c proxi2` → `config profile './proxi2.yaml'`;
no flag → `./proxi.yaml`; same for `proxi util parse_tx ... -c proxi2`.
