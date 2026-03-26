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

### Phase 2: Adaptive timestamp targeting

**Goal**: Wait for good coverage instead of submitting at the earliest possible tick.

**Files**: `sequencer/sequencer.go` (target logic), `sequencer/factory/factory.go` (coverage feedback)

**Chosen approach: Coverage threshold**

Replace `getNextTargetTime` with an adaptive approach:

1. **Coverage threshold**: The sequencer monitors the factory's best skeleton coverage. Instead of generating at a fixed target, it waits until either:
   - Coverage exceeds a threshold (e.g., 90% of recent best coverage delta), OR
   - A timeout expires (e.g., 3x pace intervals = ~36 ticks)

2. **Implementation**:
   - After `ClockCatchUpWithLedgerTime(lastSubmittedTs)`, enter a wait loop
   - Poll factory's `BestCoverage()` (new atomic field on factory) every ~50ms
   - Break when coverage threshold met or timeout
   - Use the factory's latest skeleton's `TimestampLowerBound()` to derive the actual target
   - Still respect pre-branch consolidation (force branch at slot boundary)

3. **Fewer, bigger transactions**: By waiting longer, the factory accumulates more endorsements per skeleton. The proposer then adds more tag-alongs (larger backlog has accumulated). Result: fewer sequencer txs with more endorsements and more tag-alongs each.

4. **The threshold adapts**: Track a rolling average of coverage deltas. The threshold is a fraction of this average. During high-endorsement periods, the threshold rises (wait for more). During low-endorsement periods, it drops (submit sooner to maintain visibility).

**Alternative approaches (for future experiments)**:
- **Endorsement count target**: Wait until N endorsements are achieved or timeout. E.g., target 3 endorsements, submit after 2 pace intervals if not reached. Simpler to reason about but doesn't capture coverage from delegations/tag-alongs.
- **Slot budget approach**: Budget the slot: first half for endorsement accumulation (fewer, bigger txs), second half for tag-along consumption. Branch at boundary. More structured but less adaptive to varying load.
- **Backlog-driven**: Submit when tag-along backlog exceeds N items (there's enough work to justify a tx). Combined with a minimum endorsement requirement. Directly ties submission frequency to actual demand.
- **Hybrid**: Combine coverage threshold with minimum endorsement count. E.g., wait for ≥2 endorsements AND coverage ≥ threshold, or timeout. Avoids submitting high-coverage txs with 0 endorsements.

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
