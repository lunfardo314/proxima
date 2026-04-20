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
4. Compare Prometheus metrics before/after: `proxima_seq_endorsements_*`, `proxima_branch_tx_count`

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
unified policy, and frames the longer-term direction.

### Purpose and scope

This is the **reference implementation** of sequencer submission policy
for early mainnet. Goals in order of priority:

1. **Liveness** — keep the chain moving even in adverse regimes (no load,
   thin gossip, partial peer connectivity).
2. **Predictability and observability** — one decision point, explicit
   parameters, explicit log lines for each signal firing.
3. **Good-enough coverage optimisation** — wait for endorsements when it
   helps, but never at the cost of the first two.

Optimality is explicitly not a goal. There is no single best sequencer
strategy; different regimes favour different trade-offs. The reference
policy is deliberately simple so operators can reason about it, and so a
future learned/evolved strategy (see "Future directions" below) has a
well-specified baseline to compare against.

### Forces the policy balances

| Force | Regime where it dominates |
|---|---|
| Flood-protection (throttle) | High-load, when own attachment cannot keep up with own issuance |
| Liveness floor | Thin traffic, bootstrap, post-orphan recovery |
| Coverage opportunism (plateau-wait) | Medium load, when factory skeletons keep improving |
| Tag-along drain | Persistent backlog of sender txs |

These are not independent heuristics with private gates; they are four
signals feeding **one** submission decision, ordered by priority.

### Ledger time vs wall clock — the centre-of-mass framing

Proxima's two time axes can and will diverge. Under normal operation the
incentive structure keeps them aligned. A sequencer's coverage rises when
its tx arrives at peers with a timestamp close to the peers' wall-clock at
arrival. Too-early (big backdate) → peer treats it as stale, has already
built against a different chain tip. Too-late (far future timestamp) →
peer has to hold it before it can be used, meanwhile others commit
alternatives.

So every honest sequencer is implicitly running a positional strategy:
minimise RTT to the centre of mass of sequencer capitals, where "distance"
is RTT/latency to peers with significant stake. Being a latency outlier
costs coverage. This is the silent pressure that keeps the network's
clocks aligned — and the constraint every submission-timing heuristic must
respect.

### First-milestone timing: a probabilistic sweet spot

The first non-branch milestone in a slot must carry timestamp ≥ the
post-branch consolidation boundary (`T(slot, PBC)` = tick 12 today). That
floor is the ledger's price for branch propagation: it guarantees a slot
of gossip time before any sequencer tx can claim to extend that slot.

Submission wall-clock, however, is not pinned to tick 12. The issuance
point sits in a probabilistic sweet spot:

- Submit at wall-clock ≈ tick 12: fresh timestamp, but most peer branches
  haven't reached us yet → our tx carries few endorsements → low own
  coverage, peers prefer someone else's tip.
- Submit at wall-clock ≫ tick 12 (say tick 40+): we've seen more branches
  and can endorse more, but our tx arrives at peers with a timestamp
  already behind their wall-clock → stale-looking → not worth endorsing.
- Sweet spot: the centre of a distribution, maybe wall-clock tick 15-20,
  which picks up most of the branch-gossip inflow without looking stale.

The timestamp floor (`T(slot, 12)`) is fine to keep — it's the minimum
pace, not a backdate. What must not drift is the wall-clock submission
time relative to the timestamp. The liveness floor (below) bounds that
drift.

### Unified decision policy

Priority-ordered evaluation every tick after PBC:

```
if overloaded():              # (1) flood-protection: in-flight self submission
    continue                  #     stale beyond tolerance → skip

ticksSinceLastAttempt := now - lastAttemptedTime
livenessDue   := ticksSinceLastAttempt >= maxGapTicks
improvementOk := bestCoverage > lastSubmittedCoverage
plateauStable := time.Since(lastImprovementTime) >= plateauHoldTicks
coverageReady := improvementOk && plateauStable
drainDue      := backlog.NumOutputsInBuffer() > 0 &&
                  ticksSinceLastDrain >= drainIntervalTicks

if livenessDue || coverageReady || drainDue:
    tryBuildAndSubmit()
    lastAttemptedTime = now
    if backlog_tx_attached: lastDrainTime = now
```

Key properties:

- **No separate "first probe" gate**. The liveness floor handles bootstrap
  and first-of-slot: on slot start `lastAttemptedTime` is the previous
  branch's time, so `livenessDue` fires shortly after PBC ends, naturally
  positioning the first milestone in the sweet spot.
- **Throttle still gates everything**. Nothing submits while a previous
  own milestone is stuck unconfirmed past tolerance. Preserves the
  flood-protection guarantee.
- **Coverage opportunism within a budget**. `plateauHoldTicks` is a
  maximum wait, not a fixed delay. If `livenessDue` fires first, we go
  with what we have. If coverage keeps improving past the liveness deadline
  we go anyway (improvement didn't converge in time; submit and move on).
- **Drain integrated symmetrically**. No longer a fallback tier; one of
  four equal signals. Tag-along backpressure never starves beyond
  `drainIntervalTicks`.

### Parameters

| Parameter | Role | Starting value | Notes |
|---|---|---|---|
| `maxGapTicks` | liveness floor (ticks without an attempt) | 15-20 | Shorter = faster recovery, more conservative on coverage optimisation |
| `plateauHoldTicks` | max wait for coverage plateau | 3 | Matches current default |
| `drainIntervalTicks` | tag-along drain cadence | from `MaxTagAlongInputs` / `TagAlongDrainRate` | Unchanged from current |
| `selfAttachmentLatencyToleranceTicks` | throttle tolerance | 12 | Unchanged from current |
| `TagAlongDrainRate`, `MaxTagAlongInputs` | drain throughput | as configured | Operator knob; watch these with the unified policy on testnet |

All parameters remain in the sequencer config (yaml). No abstraction
layer between policy and config for now — the reference implementation
reads config, applies it, and logs every signal firing under a single
trace tag. If a future evolving agent wants to tune these, it can write
new values into config or (later) propagate them via the sequencer's own
on-chain output (see below).

### Observability requirements

Every signal in the policy above must emit a one-line log when it fires:

- `liveness-floor fired: gap=%d maxGap=%d`
- `coverage-plateau fired: cov=%s last=%s held=%dms`
- `drain-due fired: backlog=%d interval=%dms`
- `throttled: in-flight=%s elapsed=%v tolerance=%v`
- `submit attempted: signal=<liveness|coverage|drain> built=<yes|no>`

A slot-end summary line gives aggregate counts. This is what makes the
policy diagnosable in production without deep tracing.

### What changes from the current code

Compared to the state on branch `develop07-pastcone-diag` today:

1. Replace the priority-1 (coverage) + priority-2 (drain) two-tier loop
   in `doSequencerSlot` with the single conditional above.
2. Remove the `attemptedPrimary` flag — its only purpose was "one probe
   per plateau", now subsumed by the liveness floor.
3. Track `lastAttemptedTime` instead of (or alongside) the coverage-tied
   `lastImprovementTime` — separate the "when did I last try?" question
   from the "when did the factory last improve?" question.
4. Drop the first-after-branch special case in `tryBuildAndSubmit`. The
   floor `T(slot, PBC)` stays in the general `max(nowTs+offset, paceMin,
   T(slot, PBC))` formula; no separate branch.
5. Integrate the tag-along drain as a co-equal signal in the unified
   condition. Keep `drainIntervalTicks` computed from existing config.
6. Wire the five log lines listed above, all under a single trace tag
   (`seq_policy` or similar) so operators can enable them selectively.

No policy/mechanics interface layer yet. The decision block lives inline
in `doSequencerSlot`; if it ever needs replacement, extraction into a
function behind an interface is easy and localised.

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