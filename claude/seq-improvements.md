# Sequencer Optimization: Async Submission + Adaptive Timing

## Context

The sequencer currently blocks ~2s per milestone waiting for tippool confirmation, uses a rigid timestamp targeting policy, and doesn't optimize tag-along batch size or delegation freeze distribution. With 8+ sequencers (eventually hundreds), minimizing sequencer TPS while maximizing throughput is critical. Each unnecessary sequencer tx consumes network bandwidth and validation CPU across all nodes.

Current bottlenecks:
- `waitMilestoneInTippool` blocks 10ms-2s per submission (synchronous)
- Fixed pace-based targets: 2/3 pace ahead, no adaptation to endorsement availability
- Tag-along batches average 10-20 per tx (could be 50-100+)
- Delegation freezes can spike (entire epoch's delegations in one tx)

**NOTE**: All approaches below are heuristics. Each phase should be deployed, tested on the testnet, and measured before proceeding to the next. Parameters are starting points — expect iterative tuning.

## Changes

### Phase 1: Fire-and-forget submission

**Goal**: Remove `waitMilestoneInTippool` sync wait. Submit and continue immediately.

**Files**: `sequencer/sequencer.go`

1. **`submitMilestone`**: After `OwnSequencerMilestoneIn()`, don't call `waitMilestoneInTippool`. Instead, record the submitted txid as "pending" and return immediately. Set `lastSubmittedTs = tx.Timestamp()` optimistically.

2. **Track own milestones via events**: Subscribe to the existing `PostEventNewTransaction` / tippool notifications. When the submitted milestone appears in the tippool, call `AddOwnMilestone(vid)`. This already happens for milestones from other sequencers — we extend it to own milestones.

3. **Remove `waitMilestoneInTippool`** entirely. The 2-second timeout and 10ms polling loop are eliminated.

4. **Handle submission failure**: If a submitted tx never appears (validation failure, rejection), the sequencer continues with the next target. The `lastSubmittedTs` advancement prevents retrying the same timestamp. The factory already picks up new extend candidates from `FutureConeOwnMilestonesOrdered`.

5. **`AddOwnMilestone` becomes async**: Currently called inline after `waitMilestoneInTippool` returns. Move to an event-driven callback. The sequencer registers a listener on tippool for its own sequencer ID. When a new milestone appears, it calls `AddOwnMilestone` and updates metrics.

**Risk**: The sequencer may generate the next target before the previous milestone is in the tippool. The factory's `ChooseFirstExtendEndorsePair` uses `OwnLatestMilestoneOutput()` which reads the tippool. If the previous milestone isn't there yet, the factory extends from an older milestone. This is fine — it's equivalent to backtracking, which is already a supported pattern. The newer milestone will appear eventually and the factory will pick it up.

**Alternative approaches (for future experiments)**:
- **Short non-blocking check**: Submit tx, do a single non-blocking tippool check. If not there yet, continue. Pick it up on next iteration. Simpler than full event subscription but still has a small delay.
- **Callback-based**: Submit with a callback closure that fires when the milestone reaches tippool. Sequencer doesn't block but gets notified asynchronously. More structured than event-based but requires plumbing the callback through the attachment pipeline.

### Phase 2: Event-driven sequencer with plateau detection

**Goal**: Remove synchronous target-setting and context deadlines. Decouple the sequencer into two async components: a continuous skeleton factory and an event-driven submission loop. Submit when coverage stabilizes, not when an arbitrary timer fires.

**Files**: `sequencer/sequencer.go` (main loop, target logic), `sequencer/strategy_async.go` (target computation), `sequencer/factory/factory.go` (coverage reporting)

#### Architecture

Two decoupled components:

1. **Factory** (continuous goroutine): generates and improves skeletons asynchronously. Always holds the best current skeleton (highest coverage). Never killed by a context deadline — runs until the slot ends or a new target slot is set.

2. **Sequencer** (event-driven loop): at adaptive time points, examines the factory's best skeleton. If coverage improved over the last submission, attaches tag-alongs and freezes within budget B, and submits. If not, waits longer.

The factory is the *producer*, the sequencer is the *consumer*. They communicate through the factory's `BestCoverage()` atomic field and `BestSkeleton()` accessor.

#### Pace model

- **`minPace`** (ledger floor): `TransactionPaceSequencer` — hard constraint, cannot submit faster. Currently a few ticks; likely to increase to 10-12 ticks as a ledger constant in future.
- **`targetPace`** (adaptive): starts at `max(minPace, configuredPace)`. Adapts via throttling (see below). This is the *desired* interval between submissions, not a deadline.

#### Slot structure and special points

Within each slot, three boundaries constrain sequencer behavior:

```
Tick 0          Tick 12              Tick 102        Tick 127
|               |                    |               |
BRANCH          Post-branch          Pre-branch      SLOT EDGE
                consolidation        consolidation    (next branch)
                boundary             boundary

Zone A: [0-11]    No non-branch seq txs allowed (post-branch consolidation)
Zone B: [12-102]  Active zone: tag-alongs, delegations, endorsements
Zone C: [103-127] Single-input only, no tag-alongs (pre-branch consolidation)
```

The sequencer must respect these:
- **Post-branch consolidation (zone A)**: Enforced at the ledger level for all sequencers. Its purpose is to allow the branch transaction ~1 second to propagate across the network, giving all sequencers more fair and equal exposure to each other's branches before anyone starts building on them. Good skeletons are unlikely during this zone precisely because all sequencers are waiting — there's little new DAG activity to endorse.
- **Pre-branch consolidation (zone C)**: Tag-alongs and delegation freezes are suppressed — only single-input (endorsement-only) milestones are valid. This zone is for consolidation before the branch.
- **Slot edge**: At tick 127 → tick 0 of next slot, the sequencer must issue a branch transaction.

**Note**: The tick values above (12, 102, 127) are current ledger constants and may change. The logic should reference `PostBranchConsolidationTicks`, `PreBranchConsolidationTicks`, and `MaxTickValue` rather than hardcoded values.

#### Submission loop (replaces `doSequencerStep` + `getNextTargetTime`)

The main loop within a slot:

1. **After branch (zone A)**: Wait until post-branch consolidation boundary. The factory is running but good skeletons are unlikely — all sequencers are in the same waiting zone, so there's little new DAG activity to endorse.

2. **Active zone B**: Poll the factory's best coverage every ~50ms.
   - **Submission trigger — plateau detection (debounce)**: When coverage exceeds the last-submitted baseline, start a hold timer of H ticks (e.g., H = targetPace / 4). If coverage improves again during the hold, reset the timer. Submit when the timer expires without further improvement. This means: submit when endorsements stop arriving, not at a fixed time.
   - **Hard cutoff**: If within H ticks of the pre-branch boundary, submit immediately rather than risking zone C.
   - **Nothing viable**: If the factory has no skeleton with coverage above baseline, keep polling. The sequencer may reach zone C without submitting — that's acceptable. It's equivalent to a quiet slot.
   - **Tag-alongs increase coverage**: Even endorsement-less skeletons ("base" strategy) become worth submitting when tag-alongs are attached, because tag-alongs add coverage. The proposer attaches tag-alongs after the sequencer picks up a skeleton.

3. **Pre-branch consolidation (zone C)**: Tag-alongs and freezes are suppressed. The sequencer can still submit endorsement-only milestones if coverage improved. Otherwise, it waits for the slot edge.

4. **Slot edge**: Issue branch transaction and advance to next slot.

#### What `generateMilestoneForTarget` becomes

Currently, `generateMilestoneForTarget` calls `task.Run(seq, targetTs, slotData)` with a wall-clock deadline derived from `targetTs`. The task runs the factory under a context deadline and returns the best proposal.

In the new model:
- The factory runs independently (no deadline context).
- When the sequencer decides to submit (plateau detected), it calls a new method like `task.BuildFromSkeleton(seq, skeleton, slotData)` that takes the factory's best skeleton, attaches tag-alongs/freezes within budget B, and returns the transaction.
- No context deadline. No `ErrNoProposals` from timeout. The only "timeout" is the slot edge.

#### Throttling: asymmetric adaptation of `targetPace` and budget `B`

The hold timer H scales with `targetPace`, so adapting `targetPace` also adapts how long the sequencer waits for the coverage plateau.

- **Overload signal**: actual pace (wall-clock duration between consecutive submissions) exceeds `targetPace` by >10%. Response: **sharply** increase `targetPace` (e.g., +25%) and decrease budget B (e.g., by C). This backs off quickly under load.
- **Stable signal**: actual pace fluctuates within 10% of `targetPace`. Response: **gradually** decrease `targetPace` (e.g., -5%) and increase budget B (e.g., by C/5). This probes capacity slowly.
- **Floor**: `targetPace` never drops below `minPace`. Budget B never drops below 0 or exceeds the configured maximum.

The asymmetry (5:1 ratio between decrease and increase rates) prevents oscillation: the sequencer backs off fast and recovers slowly, similar to TCP congestion control. The existing `budgetLevel` mechanism (lines 568-598 in `sequencer.go`) already implements this pattern for tag-along budget — the new throttling extends it to pace as well.

**Alternative approaches (for future experiments)**:
- **Endorsement count target**: Wait until N endorsements are achieved or timeout. Simpler to reason about but doesn't capture coverage from tag-alongs.
- **Backlog-driven**: Submit when tag-along backlog exceeds N items. Directly ties submission frequency to demand.
- **Hybrid**: Combine plateau detection with minimum endorsement count. E.g., require ≥2 endorsements before submitting. Avoids submitting high-coverage txs with 0 endorsements.
- **Slot budget approach**: Budget the slot: first half for endorsement accumulation, second half for tag-along consumption. More structured but less adaptive.

### Phase 3: Cost-budget-based delegation freeze cap

**Goal**: Prevent delegation freeze spikes by allocating a budget fraction.

**Files**: `sequencer/task/proposal.go` (delegation insertion)

1. **Budget split**: The `AttachmentCostBudget` (550) is split:
   - 30% for delegation freezes (~165 cost units ≈ ~80 delegations)
   - 70% for tag-alongs (~385 cost units ≈ ~190 tag-alongs)
   - Configurable via sequencer config

2. **`insertDelegations`**: Add a `freezeCostBudget` parameter. Stop inserting when the delegation-specific budget is exhausted, even if the total budget has room. Remaining delegations are deferred to subsequent transactions in the slot.

3. **Cross-slot distribution**: Unfrozen delegations carry over to the next slot automatically (they stay in the state as unfrozen). The existing epoch-distribution logic (`optimalFreezeEpoch`) already spreads them across epochs. The new cap just limits how many are frozen per transaction.

**Alternative approaches (for future experiments)**:
- **Fixed cap (e.g., 10)**: Freeze at most N delegations per sequencer tx. Simplest, most predictable. May under-utilize budget when tag-along backlog is small.
- **Adaptive by backlog**: If tag-along backlog is large, prioritize tag-alongs over freezes. If backlog is small, freeze more. Maximizes throughput but adds complexity.
- **Freeze-only transactions**: Dedicate specific transactions purely to delegation freezing (no tag-alongs). Simpler per-tx but increases total sequencer tx count.

### Phase 4: `max_tag_along_inputs` config enforcement

**Goal**: Make the existing config actually limit tag-along count per tx effectively.

**Files**: `sequencer/task/proposal.go`

Currently `max_tag_along_inputs` (default 100) in the config exists but the actual limit is the cost budget. With larger transactions under adaptive timing, we should enforce this as a hard cap in `insertTagAlongInputs` to keep transaction size predictable. Verify the check exists and adjust the default upward (e.g., 200) since we want bigger batches.

## Implementation Order

1. **Phase 3** first (delegation cap) — smallest change, immediate benefit, no behavioral change to submission flow
2. **Phase 1** (fire-and-forget) — removes the sync bottleneck
3. **Phase 2** (adaptive timing) — builds on Phase 1's async model
4. Phase 4 (config check) — verification + default adjustment

Each phase: implement → deploy to testnet → measure → tune parameters → document findings → proceed.

## Verification

1. `go build ./...` — compiles
2. `go test ./tests/... -timeout 600s` — full suite passes
3. Deploy to testnet, run 217 senders:
   - Check endorsement count in logs (should increase from 0-1 to 2-3 average)
   - Check non-seq per branch (should increase from 20-50 to 50-100)
   - Check sequencer tx count per slot (should decrease)
   - Monitor memory stability
4. Compare Prometheus metrics before/after: `proxima_seq_endorsements_*`, `proxima_tx_confirmed_total`

## Key Metrics to Track per Experiment

- Endorsements per seq tx (distribution: `proxima_seq_endorsements_0..8`)
- Non-seq txs per branch (from branch commit logs)
- Seq txs per slot (from slot stats logs)
- Coverage delta per branch (from branch commit logs)
- Tag-alongs per seq tx (new counter needed)
- Delegation freezes per seq tx (new counter needed)
- Sequencer step duration (existing timing log)
- Memory usage under load

## Refinement after 2026-04-20 testnet run

Phases 1 and 2 shipped. Running them on the live testnet exposed that
several independently-reasonable heuristics — plateau-wait, attempted-primary
gate, fire-and-forget throttle, backlog drain, first-after-branch target
floor — interact in pathological ways under thin traffic. Specifically:
under minimal load every sequencer waits for factory coverage to plateau,
plateau arrives ~50 ticks into the slot, each sequencer's first milestone
has a timestamp floored at `T(slot, PostBranchConsolidationTicks)` while
being submitted at wall-clock tick 50+, and this makes the milestone look
stale to peers so cross-sequencer endorsement windows collapse — branches
then fail the coverage-delta health check, slots get skipped, liveness
degrades. None of the individual rules is wrong; the combination has no
principled priority order.

This section replaces Phase 2's submission-loop spec with a simpler
**pulse-based** policy. The design also calls for a matching ledger
refactor (pace semantics, PBC removal, endorsement-monotonicity-only),
but that is **consensus-breaking** against the currently-running testnet
and is deferred to the next testnet reset.

### Rollout in two phases

**Phase S (now, testnet-compatible):** sequencer policy rewrite only.
The pulse interval lives on a sequencer-internal constant
(`sequencerPulseTicks = 12`), fully decoupled from
`TransactionPaceSequencer` (still 2 on the live testnet). The sequencer
continues to respect the existing ledger rules in its timestamp
calculation, in particular `checkPostBranchConsolidationTicks` and
`checkPreBranchConsolidationTicks` (EasyFL) and the existing
`ValidSequencerPace` floor in `parse.go`. Cooperative alignment at
12 ticks comes from every honest operator running the same reference
policy, not from a ledger hard floor.

**Phase L (later, with next testnet reset):** ledger refactor described
in the "Ledger-layer changes" section below. Bundled with other
ledger-layer changes queued for the reset. The sequencer's internal
`sequencerPulseTicks` is removed at that point and the policy reads
`lib.TransactionPaceSequencer` directly.

The doc below keeps the Phase L design intact (as target state), and
marks items that belong to Phase S vs Phase L where the distinction
matters.

### Ledger-layer changes (Go constants, testnet-compatible)

Three distinct pace constraints apply to **consumed outputs**; endorsed
inputs have only monotonicity. The only input-side distinction is
consumed-vs-endorsed — within "consumed" there is no per-input-type
differentiation (chain predecessor, tag-along and delegation inputs are
treated identically).

| Constraint | Applies to | Value |
|---|---|---|
| Non-seq pace | output consumed by any non-sequencer tx | existing constant (~24 ticks) — **unchanged**, anti-spam on regular txs |
| Seq pace | output consumed by a **non-branch** sequencer tx | **12 ticks** (`TransactionPaceSequencer`) |
| Branch-consumer pace | output consumed by a **branch** sequencer tx | monotonicity only (≥1 tick) |
| Endorsement | endorsed tx | monotonicity only (≥1 tick); no pace |

**Consequences:**

- `PostBranchConsolidationTicks` constant and concept are removed. The
  12-tick seq pace applied to a branch output (which sits at tick 0 of
  its slot) forces the first non-branch extender to land at tick ≥12
  automatically — same effect, one less concept.
- **Extending an own same-slot branch**: min tick 12 (pace floor).
- **Extending a prev-slot branch or milestone**: cross-slot distance
  generally ≫ 12 ticks, so pace is trivially satisfied; the new
  timestamp is strategy-driven — could be tick 1, 12, or anywhere later.
  Best strategy here is unknown and regime-dependent: a sequencer may
  wait for enough peer branches to arrive, then pick the extend-endorse
  pair with biggest coverage, timestamping at whatever tick respects
  pace.
- **Branch seq txs** consume under monotonicity only, so the tick-126
  milestone + tick-0 branch two-tx play is structurally supported. Not
  the target of the reference policy: normally the global plateau is
  reached before the pre-branch zone, so this pattern only pays when a
  last-moment coverage opportunity appears in zone C. Rare.
- **Tag-along inclusion latency** under a non-branch seq consumer: a
  tag-along becomes consumable ≥12 ticks after its own timestamp. Users
  see a ~1s settlement-inclusion floor, consistent with the pulse cadence.
- `PreBranchConsolidationTicks` and its semantics (no tag-alongs / no
  delegation freezes in zone C) are kept as is. Endorsement-only
  milestones remain valid anywhere up to tick 127. The purpose of the
  pre-branch zone remains: prevent a sequencer from revealing
  self-controlled coverage at the last moment and denying others the
  chance to reference it in the heaviest-branch race.

### Purpose and scope

This is the **reference implementation** of sequencer submission policy
for early mainnet. Goals in priority order:

1. **Liveness** — keep the chain moving in adverse regimes (no load,
   thin gossip, partial peer connectivity).
2. **Predictability and observability** — one decision point, explicit
   parameters, explicit log lines.
3. **Good-enough coverage** — submit what the factory has at pulse time;
   don't wait for a plateau.

Optimality is explicitly not a goal. There is no single best sequencer
strategy; different regimes favour different trade-offs. The reference
policy is deliberately simple so operators can reason about it and so a
future learned/evolved strategy (see "Future directions" below) has a
well-specified baseline to compare against.

### Ledger time vs wall clock — the centre-of-mass framing

Proxima's two time axes can and will diverge. Under normal operation the
incentive structure keeps them aligned. A sequencer's coverage rises when
its tx arrives at peers with a timestamp close to the peers' wall-clock
at arrival. Too-early (big backdate) → peer treats it as stale, has
already built against a different chain tip. Too-late (far future
timestamp) → peer has to hold it before it can be used, meanwhile others
commit alternatives.

So every honest sequencer is implicitly running a positional strategy:
minimise RTT to the centre of mass of sequencer capitals, where
"distance" is RTT/latency to peers with significant stake. Being a
latency outlier costs coverage. This is the silent pressure that keeps
the network's clocks aligned — and the constraint every submission
heuristic must respect. With the 12-tick seq pace and rational operators,
sequencers self-align at roughly one milestone per second of wall-clock,
which sets the natural pulse rate.

### Pulse-based submission policy

Every rational non-branch sequencer submission is gated below by the
12-tick seq pace on its chain predecessor. In wall-clock terms that's
~1 s between consecutive non-branch milestones under approximately
tick-to-wall-clock unity. The reference policy is: **pulse at exactly
that rate**, anchored to when the previous own milestone becomes visible
in the local tippool.

```
after previous own milestone is observed in the tippool at time T_obs:
  wait until wallClock >= T_obs + pace * tickDuration
  then, if not overloaded() and slot-edge discipline permits:
    build from factory's current best skeleton (don't wait for plateau)
    attach tag-alongs / delegations if not in zone C
    submit
```

Key properties:

- **Tippool-observation anchor.** The wait starts only once the previous
  own milestone has been re-observed in the tippool, not at submit time.
  Under healthy load, attachment is sub-tick (≈80 ms), so the realised
  pace is ≈ `12 · tickDuration + attachment` ≈ just over 1 s.
- **Natural self-throttle.** Under stress, attachment lags → the pulse
  delays itself automatically. No extra mechanism needed; the existing
  `selfAttachmentLatencyToleranceTicks = 12` throttle remains as a
  belt-and-braces cutoff for pathological stalls.
- **No plateau wait.** Submit whatever the factory has at pulse time.
  Coverage growth and backlog drain influence *what* goes in, not
  *whether* to submit. Back to "sequencer pulse priority" in the
  original sense.
- **First-of-slot behaviour falls out for free.** In general, the seq
  pace floor on the next non-branch milestone is
  `previousOwnMilestone.tick + 12` in absolute ledger time.
  - Own last milestone is a branch in the current slot (tick 0) → pace
    floor is tick 12; pulse typically lands at tick 12 + a little.
  - Own last milestone is a non-branch in the previous slot at tick `k`
    → pace floor is `k + 12` in absolute time, i.e. tick `k + 12 − 128`
    of the new slot (for `k ≥ 116`, that's tick `k − 116`). For example
    `k = 126` ⇒ pace satisfied only from tick 10 of the new slot.
  - Own last milestone is a branch in a previous slot (tick 0) → pace
    floor is `0 + 12` in the prior slot's time = well before the new
    slot begins; any tick ≥ 1 of the new slot satisfies pace.
  The pulse anchor (tippool observation of the prev own milestone) sets
  wall-clock timing; sequencer is free to choose among extend-endorse
  pairs at that moment. The reference just uses the pulse wait; richer
  strategies are open future work.
- **Slot-edge discipline.** Near the slot edge, the sequencer issues a
  branch rather than a non-branch milestone. Inside zone C, tag-along
  and delegation attachment is suppressed; endorsement-only milestones
  can still fire on pulse up to tick 127. The tick-126 + tick-0 two-tx
  play is supported but not targeted — it only pays when a last-moment
  coverage opportunity appears after the global plateau.
- **Removed from current code.** `attemptedPrimary`, `plateauHoldTicks`,
  `maxGapTicks` liveness-floor, first-after-branch special-case
  target-floor in `tryBuildAndSubmit`, two-tier Zone B loop.

### Parameters

| Parameter | Role | Starting value | Notes |
|---|---|---|---|
| `TransactionPaceSequencer` | seq pace on non-branch seq consumers | 12 | Ledger-enforced. Sequencer derives pulse interval from this. |
| `selfAttachmentLatencyToleranceTicks` | throttle tolerance | 12 | Unchanged. Skips issuance while own last milestone remains unattached past tolerance. |
| `TagAlongDrainRate`, `MaxTagAlongInputs` | drain throughput | as configured | Operator knob; observe under the new pulse policy. |

No separate `plateauHoldTicks` or `maxGapTicks`. Parameters live in the
sequencer config yaml; no abstraction layer yet.

### Observability requirements

Every pulse event emits a one-line log under a single trace tag
(`seq_policy`):

- `pulse waiting: since_tippool=%v required=%v`
- `throttled: in-flight=%s elapsed=%v tolerance=%v`
- `submit attempted: cov=%s skeleton=%s built=<yes|no>`
- `zone C: endorsement-only milestone` / `zone C: suppressing tag-alongs`
- `branch submitted: slot=%d cov=%s`

A slot-end summary line aggregates counts.

### What changes from the current code

**Phase S items (ship to current testnet on `develop07-pastcone-diag`):**

1. Add sequencer-internal constant `sequencerPulseTicks = 12` with a
   one-line comment noting its Phase L migration to
   `lib.TransactionPaceSequencer`.
2. Replace the two-tier Zone B loop in `doSequencerSlot` with the pulse
   loop above.
3. Remove `attemptedPrimary`, plateau tracking, liveness floor,
   first-after-branch special case in `tryBuildAndSubmit`.
4. Tag-along and delegation attachment become by-products of the pulse,
   not gating signals. Zone C suppression stays.
5. Track `lastOwnMilestoneTippoolObservedTime` as the anchor for the
   pulse wait. Set it on tippool observation of an own milestone (same
   path that currently calls `clearPendingSubmitIfMatch`).
6. Keep existing `tryBuildAndSubmit` target-timestamp floor logic that
   respects `PostBranchConsolidationTicks` and `ValidSequencerPace` —
   the EasyFL constraints are still live.
7. Wire the `seq_policy` trace-tag log lines.

**Phase L items (held for next testnet reset):**

8. Raise `defaultTransactionPaceSequencer` to 12.
9. Split `parse.go scanInputs` dispatch by branch/non-branch (branch
   consumers → monotonicity only; non-branch seq consumers → seq pace
   12; non-seq consumers → unchanged).
10. Reduce `parse.go scanEndorsements` to monotonicity-only.
11. Remove `checkPostBranchConsolidationTicks` in
    `ledger/def/sequencer.easyfl`.
12. Remove the sequencer-internal `sequencerPulseTicks` and drive the
    pulse from `lib.TransactionPaceSequencer` directly.

No policy/mechanics interface layer yet. The decision block lives inline
in `doSequencerSlot`; extraction behind an interface remains easy if a
future learned strategy wants to replace it.

## Future directions

Once the reference policy is in and the testnet is stable on it, the next
steps are out of scope for this doc but worth naming so that current
choices stay compatible:

- **Per-operator parameter tuning**. Operators at high RTT from the centre
  of mass should be able to adjust `maxGapTicks` and `plateauHoldTicks`
  without forking the policy. All parameters live in config.
- **On-chain parameter persistence**. The sequencer's own chained output
  (`seqdata`) is the natural place to stamp the current parameter set
  used by this sequencer. That gives every transition a provenance trail
  and makes the sequencer's strategy snapshot readable by any observer
  without needing access to the operator's config file. Not implemented
  now — keep the current simple YAML path — but the schema for `seqdata`
  should leave room for a small parameter block in a future upgrade.
- **Evolving strategy agent**. The long-term vision is a learned agent
  per operator: observe the node's slot-by-slot outcomes (own coverage,
  peer coverage, orphan rate, RTT deltas) and adapt its parameters as a
  persistent "genome" — written to and carried forward by `seqdata`.
  Different operators will evolve different genomes suited to their
  network position. The reference policy in this document is not meant
  to compete with that; it is the well-specified baseline every evolved
  genome is measured against.