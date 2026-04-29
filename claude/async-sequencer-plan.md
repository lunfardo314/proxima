# Async Sequencer: Investigation & Implementation Plan

## Current Flow (synchronous, step-based)

```
sequencerLoop:
  for {
      targetTs = getNextTargetTime()           // blocks: ClockCatchUpWithLedgerTime + pace math
      factory.SetTargetSlot(targetTs.Slot)
      tx, meta = task.Run(targetTs)            // deadline = ClockTime(targetTs)
         ├─ b0: branches, exposure, non-endorsed milestones with inputs
         ├─ x:  bootstrap (explicit baseline, cross-slot)
         ├─ f0: drain factory skeletons, keep best, insert inputs
         ├─ e1/e2/e3/r2/r3: legacy endorsement strategies
         └─ pick best proposal
      msVID = submitMilestone(tx, meta)
         ├─ decideSubmitMilestone()             // health check
         ├─ OwnSequencerMilestoneIn()           // send to input queue
         └─ waitMilestoneInTippool()            // BLOCKING: poll 10ms, up to 2s timeout
      AddOwnMilestone(msVID)                    // cache consumed outputs
  }
```

**Problems:**
1. `waitMilestoneInTippool` blocks 10ms–2s every step
2. `getNextTargetTime` computes target from wall clock + pace, not skeleton readiness
3. `task.Run` creates fresh proposer goroutines per target with hard deadline
4. Factory produces skeletons continuously but they're only consumed in the brief `task.Run` window

## Strategy roles (preserved)

| Strategy | Short | Role | When active |
|----------|-------|------|-------------|
| **b0** (base) | `b0` | Everything without endorsements: branches at slot boundary, exposure milestones (extend-only), non-endorsed milestones with tag-alongs and delegation freezes | Always — fallback when no endorsement is possible |
| **x** (boot) | `x` | Bootstrap: cross-slot extend with explicit baseline when own milestone is >1 slot behind and nothing to endorse | Bootstrap phase only |
| **f0** (factory) | `f0` | Endorsed milestones: drain factory skeletons (extend + endorsements), insert inputs | Normal operation, non-branch targets when endorsements available |

In the new design, `b0` and `x` remain as **opportunistic issuers**. The factory replaces `f0` — skeleton consumption moves from f0 proposer into the sequencer main loop.

## Proposed Flow (async, pipeline)

```
sequencerLoop:
  start milestoneWatcher()                     // background: tippool → AddOwnMilestone

  for {
      targetTs = getNextTargetTime()           // simplified target policy
      factory.SetTargetSlot(targetTs.Slot)

      if needBootstrap() {
          doBootstrapStep(targetTs)            // x: explicit baseline, cross-slot
          continue
      }

      // try factory path first: endorsed milestones
      skeleton = pickBestSkeleton()            // non-blocking drain of factory.OutCh()
      if skeleton != nil {
          tx, meta = buildMilestone(skeleton, targetTs)  // insert inputs, sign
          submitAsync(tx, meta)
          continue
      }

      // no endorsed skeleton available — fall back to b0
      doBaseStep(targetTs)                     // branches, exposure, non-endorsed with inputs
  }
```

### Key design decisions

1. **Fire-and-forget submission**: `OwnSequencerMilestoneIn()` + advance `lastSubmittedTs` immediately. No `waitMilestoneInTippool`. Background `milestoneWatcher` calls `AddOwnMilestone` when milestone appears in tippool.

2. **Target timing** (constant `targetIntervalTicks = 12`):
   - After branch: first target at `branch + PostBranchConsolidationTicks` — ASAP
   - Then: every `targetIntervalTicks` ticks, adjusted for skeleton's `TimestampLowerBound`
   - Pre-branch consolidation zone: switch to branch target
   - Pace constraint (`TransactionPaceSequencer = 2`) always respected

3. **Factory-first, b0 fallback**: Main loop tries factory skeleton first (endorsed milestone). If none available, falls back to b0 which handles all non-endorsed cases: branches, exposure milestones, and non-endorsed milestones with tag-alongs/freezes.

4. **b0 and x stay as-is**: Their logic is unchanged. Both use `submitAsync` instead of old `submitMilestone`.

## Changes Required

### 1. `sequencer/sequencer.go` — Main loop

**Remove:**
- `waitMilestoneInTippool()`
- Current `submitMilestone()` (replace with `submitAsync`)
- Current `generateMilestoneForTarget()` for factory path

**Add:**
- `submitAsync(tx, meta)` — send to input queue, update `lastSubmittedTs`, no wait
- `milestoneWatcher()` — background goroutine polling tippool, calls `AddOwnMilestone`
- `pickBestSkeleton()` — non-blocking drain of `factory.OutCh()`
- `buildMilestone(skeleton, targetTs)` — insert inputs + build tx (extracted from f0)
- `needBootstrap()` — check if x strategy condition applies
- `doBaseStep(targetTs)` — b0 logic (branches, exposure, non-endorsed with inputs)
- `doBootstrapStep(targetTs)` — x logic (explicit baseline, cross-slot)
- Simplified `getNextTargetTime()`

**New constants:**
```go
const (
    targetIntervalTicks    = 12  // ticks between non-branch targets
    milestoneWatchInterval = 20 * time.Millisecond
)
```

### 2. `milestoneWatcher` — Background tippool monitor

Polls `GetLatestMilestone(seqID)` every ~20ms. When own milestone changes:
- Calls `AddOwnMilestone(vid)`
- Updates counters, runs callbacks, updates metrics

The factory already detects own milestone changes in its improvement loop and restarts its round. No factory changes needed.

### 3. `getNextTargetTime` — Simplified

```
after branch → PostBranchConsolidationTicks (ASAP)
regular      → lastSubmittedTs + targetIntervalTicks
near slot end → pre-branch → branch target (slot boundary)
always       → max(target, lastSubmittedTs + pace)
```

### 4. `pickBestSkeleton` — Non-blocking

Drain `factory.OutCh()` with `select/default`, keep highest coverage skeleton, close others.

### 5. `buildMilestone` — Extracted from f0

Logic from `factoryProposalGenerator` moves here:
1. Compute `effectiveTs = max(targetTs, skeleton.TimestampLowerBound())`
2. Check slot consistency
3. Create proposal (SeqTxBuilder)
4. Insert inputs (tag-alongs + delegations) unless pre-branch
5. Recompute lower bound after inputs
6. Build and sign transaction

### 6. `doBaseStep` and `doBootstrapStep`

**`doBaseStep(targetTs)`**: Reuses b0 logic. Handles all non-endorsed cases:
- Branch transactions at slot boundary
- Exposure milestones (extend-only)
- Non-branch milestones with tag-alongs and delegation freezes (no endorsement)

**`doBootstrapStep(targetTs)`**: Reuses x logic. Handles:
- Cross-slot extend with explicit baseline (LRB)
- Active only when own milestone is >1 slot behind

Both use `submitAsync` for fire-and-forget submission.

### 7. What stays unchanged

- **Factory** (`factory/factory.go`): No changes
- **b0 logic** (`proposer_base.go`): Reused from `doBaseStep`
- **x logic** (`proposer_boot.go`): Reused from `doBootstrapStep`
- **`insertInputs`** in `proposal.go`: Same, called from `buildMilestone`
- **`AddOwnMilestone`**: Same logic, called from watcher
- **Backlog, tippool, attacher**: No changes

## Risks & Mitigations

1. **Rejected tx after optimistic advance**: Sequencer has advanced `lastSubmittedTs` but tx failed validation. Next target will be later — effectively a skipped step. Factory produces new skeleton from whatever milestone is actually in tippool. Acceptable.

2. **Double-spend window**: Between `submitAsync` and `AddOwnMilestone` (~20ms watcher poll), sequencer could build another tx consuming same outputs. Mitigation: `targetIntervalTicks = 12` gap (~1.2s) ensures watcher sees the milestone well before next build.

3. **Bootstrap detection**: Check if own latest milestone is >1 slot behind (same condition as x strategy).

## File Impact Summary

| File | Change |
|------|--------|
| `sequencer/sequencer.go` | Major: new main loop, submitAsync, milestoneWatcher, pickBestSkeleton, buildMilestone, doBaseStep, doBootstrapStep, simplified getNextTargetTime |
| `sequencer/task/proposer_factory.go` | Eventually removed (f0 logic moves to sequencer). Keep initially. |
| `sequencer/task/proposer_base.go` | Unchanged (b0 logic reused) |
| `sequencer/task/proposer_boot.go` | Unchanged (x logic reused) |
| `sequencer/task/task.go` | Unchanged (still used for b0/x if called via task.Run) |
| `sequencer/task/proposal.go` | Unchanged (insertInputs used from buildMilestone) |
| `sequencer/factory/factory.go` | Unchanged |

## Implementation Order

1. Add `milestoneWatcher` goroutine + `submitAsync` (minimal, testable immediately)
2. Add `pickBestSkeleton` + `buildMilestone` (extract f0 logic)
3. Simplified `getNextTargetTime` with `targetIntervalTicks`
4. Rewrite `sequencerLoop` with async flow: doBootstrapStep / factory-first / doBaseStep fallback
5. Test on testnet, tune `targetIntervalTicks`
