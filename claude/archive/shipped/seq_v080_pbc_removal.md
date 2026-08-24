# Session report — v0.8.0 sequencer pace / PBC permanent encoding

Date: 2026-04-30
Branch: `develop08`
Driver: ship the Phase L items from `claude/archive/shipped/seq-improvements.md` permanently into the ledger and the node code, without changing observable sequencer behavior.

## Goals

The v0.7.x testnet ran with several temporary adjustments to keep the
in-flight sequencer policy compatible with the deployed ledger:

- A sequencer-internal pulse constant `defaultSequencerPaceTicks = 12`,
  decoupled from the on-ledger `TransactionPaceSequencer = 2`.
- A Go-side `pbcFloor` clamp pushing target timestamps to ≥ 12 ticks
  inside the slot, because the EasyFL `checkPostBranchConsolidationTicks`
  rule was still live.
- Endorsements were subject to the same `ValidSequencerPace` check as
  consumed inputs, even though the policy only needed monotonicity.

v0.8.0 has no backward-compat constraint, so all of these were collapsed
into a single ledger constant and a clean two-case stage-1 dispatch.

## Concrete changes

### Ledger constants and EasyFL

- `ledger/def/def_constants0.yaml` — deleted `constPostBranchConsolidationTicks`.
- `ledger/def/sequencer.easyfl` — deleted `checkPostBranchConsolidationTicks`
  and its call inside `func sequencer`.
- `ledger/def_constants0.go` — `defaultTransactionPaceSequencer 2 → 12`.

### Go-side ledger struct

- `ledger/constants.go` — removed field `PostBranchConsolidationTicks`,
  its loader, the description line, and helper methods
  `IsPostBranchConsolidationTimestamp` /
  `EnsurePostBranchConsolidationConstraintTimestamp`.

### Stage-1 transaction validation

`ledger/transaction/parse.go`:

- `scanInputs` — two cases (no branch special path):
  - sequencer consumer (incl. branch) → `ValidSequencerPace` (12 ticks).
  - non-sequencer consumer → `ValidTransactionPace` (12 ticks).

- `scanEndorsements` — pace check dropped. Cross-slot rejection retained.
  Strict 1-tick monotonicity (`DiffTicks ≥ 1`) added inline. Same-tick
  endorsement is rejected; 1-tick gap is now valid.

The "branch consumer monotonicity-only" cell in the seq-improvements.md
table was deliberately not implemented: the user clarified that two
cases is enough, since the tick-126 + tick-0 two-tx play is not part of
the reference policy and the pace floor of 12 is fine for branches in
practice.

### Sequencer / attacher / txbuilder

- `sequencer/config.go` — deleted `defaultSequencerPaceTicks`. Default
  pace now comes from `ledger.L(base.MaxSlot).TransactionPaceSequencer`.
- `sequencer/strategy_async.go`:
  - Dropped `pbcFloor` from `tryBuildAndSubmit`. Target is just
    `MaximumTime(nowTs, paceMin)`.
  - Replaced the PBC reference in the throttle-escape clause with
    `lib.TransactionPaceSequencer`.
- `core/attacher/attacher_incremental.go` — `TimestampLowerBound`
  rewritten: per-input contributes `+pace`, per-endorsement contributes
  `+1`, max wins. No PBC clamp. This naturally allows tick=1 of a new
  slot when extending a prior-slot branch, which was the user's
  correction to my initial plan.
- `sequencer/txbuilder_seq/txbuilder_seq.go` — dropped the
  `IsPostBranchConsolidationTimestamp` precheck in `New`. The pace
  check on the chain input already covers what's needed.

### Tests

- `tests/test_util.go`, `tests/attach_cost_test.go`,
  `tests/attach_deadlock_test.go`, `tests/attach_test.go`,
  `tests/attach_timing_test.go` — removed all
  `EnsurePostBranchConsolidationConstraintTimestamp` calls.
- `tests/attach_timing_test.go` — deleted obsolete
  `TestAttachTimingPostBranchConsolidation`.
- `ledger/tests/claude_sequencer_test.go` — deleted obsolete
  `TestSequencerPostBranchConsolidation`. Made `TestSequencerInputPace`
  and `TestSequencerSameSlotNonSeqPredecessor` pace-agnostic by reading
  `ledger.L(0).TransactionPaceSequencer` instead of hard-coding 2.
- `ledger/tests/claude_endorsement_test.go` — replaced
  `TestEndorsementPaceViolation` with two new tests:
  - `TestEndorsementMonotonicityViolation` — same-tick endorsement
    rejected with "violates strict monotonicity".
  - `TestEndorsementOneTickGapAccepted` — endorsement 1 tick before
    the endorsing tx is accepted (would have been rejected pre-refactor).

### Test ledger

`tests/init.go` keeps `WithTransactionPaceSequencer(3)` for speed.
Production default is 12; tests use 3. Tests are now written
parametrically in terms of the active library constant rather than
hardcoded values.

## Verification

```
go build ./...
go test ./...
```

Both pass. `tests/` packages took ~7-8 minutes due to multi-sequencer
runs. No flaky failures observed on a clean rerun.

## Sequencer behavior

In steady state, the pulse-based reference policy is unchanged: the
sequencer still emits at most one own milestone per `cfg.Pace`-tick
wall-clock interval, anchored to the moment the previous own milestone
becomes visible in the local tippool. The only differences:

- The ledger-time floor for the first non-branch milestone of a slot is
  now strictly the pace floor, not max(pace floor, 12). When extending
  a prior-slot branch, this can land at tick 1 of the new slot instead
  of being clamped to tick 12. This was an intentional gain — Phase L
  unlocks the legitimate cross-slot tick=1 case that PBC blocked.
- The pulse interval is read from `lib.TransactionPaceSequencer`
  (default 12) instead of the duplicated sequencer-internal constant.
  Numerically identical for production; one fewer constant to keep
  in sync.

No metric, log line or external behavior intentionally changed.

## Risks / follow-ups

- The pulse anchor logic continues to enforce the wall-clock 12-tick
  cadence; the ledger-time tick=1 case only fires when a sequencer
  resumes after a multi-slot stall. Worth watching on testnet for any
  surprise interactions, but no specific failure mode is anticipated.
- `tests/init.go` could be raised to pace=12 to better match production,
  at the cost of slower tests. Left as-is per agreement (option (a)).
- `claude/archive/shipped/seq-improvements.md` "Phase L items" can be marked done in a
  follow-up doc edit; not done as part of this commit to keep the
  diff focused.
