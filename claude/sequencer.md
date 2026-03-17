# Optimization of the sequencer algorithms

## Currently
After removing some important bottlenecks, sequencer is working well. The system with 4 sequencers and on pretty low-end nodes
can handle 217 parallel senders. That makes some 10-15 TPS on the average and significantly more in peaks.

However, messages like _>>>>>>>>>>>>> sequencer step took 3.525236161s_ indicates that sequencer is CPU-hungry
and its architecture itself is a bottleneck.

## Goal

We are aiming at hundreds of sequencers working in the Proxima network and handling thousands of TPS.
That definitely requires much more powerful nodes, however at this stage we must optimize algos and architecture
of the sequencer.

We want to implement a new version of the sequencer, `sequencer2`, leaving existing version in the codebase.
Sequencer config section must choose which of versions, the old or `sequencer2` to run.

The `sequencer` can be seen as a reference implementation for the `sequencer2`.

We see the sequencer optimization problem as research and iterative improvement problem.

## Purpose of the sequencer

The sequencer follows a strategy of the greedy token holder.

Token holder, is the controller of funds via private key.
The sequencer is a software agent tha is issuing transactions on behalf of the token holder.
Transactions a subject of constraints, imposed by the ledger and by nodes.

Sequencer's main goal is maximize profitability via:
- generation of inflation from own funds
- generation of inflation from delegated funds
- collecting tag-along fees

Most of these things depends on the market dynamics between token holders: sequencer, delegators and those who transfer funds.
It is unpredictable and should not concern us at this stage.
Our concern is to make sequencer minimize downtime and sequencer do its best:
- issuing at least 1 milestone per slot
- freezing funds delegated to it and generating inflation from it
- consuming tag-along UTXOs with maximum fee, thus helping other token holders their transactions to be included into the ledger

The idea of the Proxima's _cooperative consensus_ is that rational profit-seeking behavior of token hodlers leads to the greater good,
a consensus on the ledger. So, stability of the system as a whole is also part of the sequencer's concern.

That means a behavior, such as overwhelming the network with transactions out of greediness or trying to trick other sequencers, ultimately
plays against the interest of the token hodler.

Besides, we want to assume the least possible minimal requirements for the node and sequencer computers to be able to participate in the consensus.

In general there can be many different implementations of sequencers. The `sequencer` and `sequencer2` are _reference implementations_.

## Constraints and optimization

The main rule of the sequencer is to issue transactions with the biggest ledger coverage possible, in the dynamically changing context and given the constraints.
The only essential type of messages that is exchanged by nodes, is raw transaction.

The constraints for the sequencer comes from the globally imposed system rules and from scarceness of real world resources.

### System-imposed constraints
- ledger constraints (validity rules): absolute majority of it is encoded in the EasyFL code
- constraints imposed by nodes, such as rate limits per transaction sender (public key, holder ID)

### Resource-bound constraints
The goal to maximize coverage is an optimization problem in the changing environment: knowledge of the node about the tip of the transaction DAG.

The latency limits _when_ node/sequencer start its active set of transaction to take samples for optimization.
We have to assume latencies at least 10-100 ms, perhaps more.

## Sketch requirements

Combinatorial complexity (hundreds of sequencers and at thousands of TPS). a number of possible samples that can be taken from the dynamically changing set of DAG tips,
points to high CPU requirements.

That points to importance of heuristics and an architecture with minimal bottlenecks.
Currently, those heuristics are implemented as different proposers, as time target setting and so on.

We cannot assume bounds on the system load and resources available.
Sequencer should weight between speed (pace) how it issues transactions and how big coverage those transactions have.

The current `sequencer` implements target setting strategy: it chooses reasonable timestamp target and proposers then generate
possible transactions for that timestamp target. Proposers compete over CPU and time target set a resource limit.

There can be variations in these strategies. For example, it may make sense to have flexible timestamp targets, that is waiting a bit longer,
until it becomes possible to issue one transaction but with 5,6 or more endorsements with much bigger coverage.
Note that the speed of convergence of the consensus depends on how quickly ledger coverage grows. That, in turn, is roughly O(exp(number of endorsements)).

We want to achieve the following principles in the architecture of the sequencer:

- doing the best under the load
- the transactions that cannot be included shall be orphaned
- majority of capital cooperating is prerequisite for the security of the ledger, so this is a priority
- the priority is reaching maximal coverage asap. That means maximal consolidation of sequencer milestones via endorsements
- freezing delegations and consuming tag-alongs are essential: they contribute directly to coverage AND generate revenue
- avoid overwhelming the system with unnecessary transactions

## Instruction for Claude

First assume you are a researcher and a system architect
- analyze available resources (ask) and the code of `sequencer`
- refine requirements in this doc, iterate over ideas and ask questions
- propose high level plan, the approach to the `sequencer2`.
That must be an iterative approach to the problem, with implementation/experiment/improvement cycle.
The high level planning would mostly use abstract ideas, not implementation level

After plan will be refined to the satisfaction of the user, we will proceed with the detailed planning, implementation and experimenting on the testnet.

---

## Analysis findings

### Current architecture

The sequencer runs a **fixed-target loop**: pick a timestamp target, launch 7 proposer goroutines (b0, x, e1, e2, e3, r2, r3)
that race to find the best coverage, select the winner by coverage (then by size), submit.

**Proposer strategies**:
- `b0` (base): extends own latest milestone, handles branches
- `x` (boot): bootstrap with explicit baseline when no endorsement is possible
- `e1`: one endorsement, coverage-sorted candidates
- `e2`/`e3`: two/three endorsements, coverage-sorted
- `r2`/`r3`: two/three endorsements, random selection

The best proposal wins by highest coverage; ties broken by smaller tx size.

### Metrics observations (4-node testnet, 217 senders, ~10-15 TPS)

From Grafana charts:
- **Winning strategies**: e2+r2 (~1.5-2 wins/slot) and e1 (~1-1.5) dominate. b0 is low (~0.5).
  e3/r3 do not appear as winners.
- **Proposals per slot**: total 100-150 under low load, dropping to 50-80 under spammer load.
  e1 generates most proposals (~30-50). Much CPU spent on proposals that lose.
- **MemDAG vertices**: spikes to ~4000 at load ramp-up, stabilizes at ~2500
- **Transactions in LRB**: ~60-80 per slot under load

### Key findings

1. **Fixed target is a fundamental limitation.** The sequencer commits to a timestamp *before* knowing
   what's endorsable at that time. The 2/3-pace-ahead heuristic is a compromise.

2. **Proposers duplicate work.** e1/e2/e3 and r2/r3 iterate the same candidate set with slight variations.
   The `alreadyCheckedCombination` cache helps but is a patch over redundant work.

3. **Most milestones have only 1-2 endorsements.** Under load, multi-endorsement strategies (e3/r3)
   likely run out of CPU time rather than lacking endorsable candidates. This reflects the CPU bottleneck,
   not a fundamental limit of multi-endorsement. With less wasted work, 3+ endorsements should become
   more achievable.

4. **IncrementalAttacher is expensive and already well-optimized.** DAG algorithms are O(n^2) or worse.
   The key optimization lever is *minimizing the number of attacher creations*, not making each cheaper.

5. **Coverage grows O(exp(endorsements)).** This makes maximizing endorsement count per transaction
   the highest-value optimization target.

6. **Visibility is essential.** The sequencer must issue at least one milestone per slot to expose itself
   to other sequencers. This is critical both for selfish reasons (being endorsed by others) and for
   network health. Strategies `x` and `b0` serve this purpose in the current sequencer.

7. **Sequencer timings are likely naturally staggered** across different sequencers within a slot,
   though the extent is not precisely known.

### Key code patterns to preserve

**Backtracking via conflicting transactions is fundamental.** `FutureConeOwnMilestonesOrdered`
(`sequencer/own_milestones.go`) builds a chain of own milestones from a root (baseline's chain output).
Different proposers can extend from *different* points in this chain, meaning the sequencer intentionally
issues conflicting transactions. This is by design: if a better endorsement opportunity appears, the
sequencer backtracks to an earlier milestone and branches from there. The DAG naturally resolves which
branch wins. This must be preserved in sequencer2.

**The extend-endorse pair selection** (`ChooseFirstExtendEndorsePair` in `sequencer/task/proposer.go`)
is the core decision loop: get endorsement candidates from backlog (sorted by coverage or shuffled) ->
for each candidate, find baseline -> get chain output -> build future cone of own milestones -> try all
(extend, endorse) pairs -> pick highest coverage. The endorsement drives the baseline, which drives
what's extendable. This logic should be extracted as a shared utility for sequencer2.

**IncrementalAttacher lifecycle**: create with (extend + endorsement + targetTs) -> optionally
`InsertEndorsement()` for more endorsements -> `InsertInput()` for tag-alongs/delegations -> `Close()`
to release memDAG references. Currently no clone/fork capability exists.

### Refined requirements

- **Coverage-first principle**: maximize coverage through ALL channels: endorsements, freezing
  delegations (adds to inflatable amount), consuming tag-alongs (adds fee coverage)
- **"Good enough, ship it"**: a valid transaction is already valuable. Don't wait for a perfect
  combination that may become stale. Every intermediate result is submittable.
- **Minimize attacher creations**: prefer incremental improvement via cloning over throwaway speculation
- **Strategy-specific adaptive pace**: each strategy owns its own timing. Safety strategies are fast,
  endorsement strategies take more time.
- **Coexist with `sequencer`**: config-selectable. Keep IncrementalAttacher, backlog, own_milestones
  and interfaces mostly intact or minimally modified for compatibility and to prevent new bugs.
- Designed as a stable, extensible reference implementation -- not over-optimized for current testnet size
- Assume the least possible minimal requirements for node hardware
- **Ultimate metric**: coverage + tag-alongs + freezes per CPU resource

---

## IncrementalAttacher timestamp dependency analysis

Investigation of how IncrementalAttacher uses `targetTs`. Key finding: **the exact tick is irrelevant
for incremental attachment. Only the target slot and branch/non-branch flag matter.**

### Coverage calculation uses only `txTs.Slot`

`Coverage()` → `AdjustedFrozenCoverage(txTs)` → `DiffEpochs(chainID, txTs, predTs)` → uses only
`txTs.Slot`. The tick value is irrelevant. `IsInFrozenSlot()` also uses only the slot.

### Classification of all `targetTs` uses

**Slot-only (work with just a target slot):**
- Library caching: `ledger.L(targetTs.Slot)` — needs slot
- Coverage: `CoverageDeltaRaw` → `ledger.Coverage` → `AdjustedFrozenCoverage` — slot only
- Endorsement slot matching: `targetTs.Slot == endorseVID.Slot()` — slot
- Cross-slot detection: `extend.Slot() != targetTs.Slot` — slot
- Delegation freeze checks: `IsInFrozenSlot(txTs.Slot)` — slot

**Branch detection (binary: tick == 0 or not):**
- `targetTs.IsSlotBoundary()` — determines stem input, no endorsements, baseline direction

**Pace checks (need exact timestamp — deferrable):**
- Constructor: `ValidSequencerPace(extend.Timestamp(), targetTs)` — assertion
- Constructor: `ValidTransactionPace(endorseVID.Timestamp(), targetTs)` — assertion
- `InsertEndorsement`: `endorsement.ValidSequencerPace(a.targetTs)` — can be deferred
- `InsertInput`: `wOut.VID.ValidSequencerPace(a.targetTs)` — can be deferred
- Pre/post-branch consolidation — proposal-level concern, not attachment

**Builder-only (after attachment):**
- `txbuilder_seq.Params{Timestamp: a.TargetTs()}` — final tx construction
- `FinalLedgerCoverage(p.targetTs)` — proposal comparison

### Implication

Two non-branch targets in the same slot produce **structurally identical** IncrementalAttachers
(same baseline, same past cone, same coverage). The only difference is pace filtering. This means:
- Attachers can be reused across targets within the same slot
- Pace checks can be deferred to proposal/builder phase
- Clone() becomes even more powerful: one attacher per slot, cloned for different targets

### Refactoring: IA takes (slot, isBranch) instead of targetTs

Implemented: constructor takes `targetSlot` and `isBranch` instead of exact `targetTs`.
`TimestampLowerBound()` method computes the earliest valid timestamp from the inputs/endorsements
already inserted. Pace checks removed from IA — caller's responsibility.

---

## Revised approach: incremental refactoring (not rewrite)

### Why not a full rewrite

The `sequencer2` from-scratch approach was attempted and reverted. Key realizations:

1. **Sequencing is inherently CPU intensive.** With 100s of sequencers in the network, the sequencer
   process will consume the majority of CPU anyway — it is heuristics-guided brute force. The goal
   is not to eliminate CPU usage but to eliminate *wasted* CPU usage (duplicate work).

2. **The current code handles subtle timing correctly.** The relationship between ledger time and
   wall clock is delicate: they are assumed close but may diverge, and incoming transactions may be
   from the ledger past or future indefinitely. The current implementation handles this carefully.
   A rewrite risks breaking these invariants.

3. **Fixed targets are not the problem.** IncrementalAttachers don't rely heavily on the target
   timestamp — the target is mainly used for pace validation and deadline. The real waste comes
   from duplicate attacher creations across proposers, not from the target-setting mechanism.

4. **Node activity must have CPU priority.** Incoming transactions, gossip, and attachment are the
   node's core function. The sequencer takes whatever CPU is left. This is already the case with
   the current architecture.

5. **The submit-wait pattern may be removable.** Currently the sequencer blocks waiting for its
   own milestone to appear in the tippool. Removing or relaxing this could improve throughput
   without architectural changes.

### What we have from the analysis

**Implemented and available:**
- `IncrementalAttacher.Clone()` — deep copies mutable state, shares vertex references.
  Asserts no pending delta. Tested with 6 unit tests. Ready to use in proposers.

**Key findings preserved:**
- e2/r2 win most often; e3/r3 never win (CPU bottleneck, not lack of candidates)
- 100-150 proposals per slot under low load, most losing — duplicate work
- `alreadyCheckedCombination` cache is a patch over redundant proposer work
- Backtracking (conflicting txs) is fundamental and must be preserved
- The extend-endorse pair selection (`ChooseFirstExtendEndorsePair`) is the core decision loop

### TransactionSkeletonFactory (TSF)

The core optimization component. A persistent process that continuously scans the tippool and
produces **transaction skeletons** with strictly increasing coverage. Located in `sequencer/factory/`.

#### Definitions

**TransactionSkeleton** (skeleton): an IncrementalAttacher that extends own milestone and endorses
1 or more milestones of other sequencers. Has a target slot and particular coverage delta.
A skeleton contains only endorsements — no tag-along or delegation inputs. Those are added
later by whoever consumes the skeleton.

**TransactionSkeletonFactory** (TSF): a persistent goroutine that produces skeletons and sends
them to an output channel. The consumer reads skeletons, keeps the one with biggest coverage,
closes the rest.

#### TSF behavior

**Trigger**: a new own milestone appearing in the tippool. When TSF detects a new own milestone,
it starts a new round:

1. Call `ChooseFirstExtendEndorsePair` to find the first valid (extend, endorse) pair.
   This produces skeleton_0 with 1 endorsement.
2. Post skeleton_0 to the output channel.
3. Enter the improvement loop: try adding more endorsements to increase coverage.

**Improvement loop**:
- Clone the current best skeleton.
- Re-query the tippool for fresh endorsement candidates. The candidate set is dynamic — new
  milestones arrive constantly. A candidate sequencer S's milestone may be replaced by a newer
  one mid-check. The checked-combinations set prevents re-checking the same combination, but
  a new milestone from S is a new combination.
- Push `(clone, candidate)` work items into a **job channel** read by N persistent worker
  goroutines (e.g. 3-5). Workers try `InsertEndorsement` on the clone and push results to a
  result channel. Workers are persistent for the lifetime of the round — no goroutine churn.
- Collect results from the result channel. If any worker produced a skeleton with strictly
  higher coverage than the current best, that becomes the new best. Post it to the output
  channel. Close all losers.
- Repeat until all candidates are exhausted or context is cancelled. All candidates are
  eventually tried — the N workers are for congestion control only.

**Strictly increasing coverage**: TSF tracks the best coverage delta it produced in the current
round. It only posts new skeletons that strictly exceed this. An internal filter goroutine
sits between workers and the output channel to enforce this.

**Checked-combinations set**: per-round. Tracks which `(extend, {endorse1, endorse2, ...})`
combinations have been checked. The key is the **set** of endorsed transaction IDs — endorsement
order is irrelevant (swapping two endorsements produces the same skeleton). Reset when a new
round starts (new own milestone detected).

**Round restart**: when a new own milestone appears in the tippool while an improvement loop
is running, the current round is cancelled and a new round starts from `ChooseFirstExtendEndorsePair`.
The new milestone is a better starting point because it is already endorsed by others.

**Cancellation**: the TSF respects a context. When cancelled (slot end, shutdown, round restart),
all in-flight workers are stopped and pending skeletons are closed.
If no new own milestone arrives, the TSF should not block forever — it should poll periodically
or use a timeout to remain responsive to context cancellation.

#### Architecture

```
N persistent worker goroutines:
  read (clone, candidate) from jobCh
  clone -> InsertEndorsement(candidate)
  if success: send clone to resultCh
  else: close clone

TSF main goroutine:
  start N workers (persistent, read from jobCh, write to resultCh)
  loop:
    poll tippool for new own milestone (with timeout/context)
    if new milestone:
      reset checked-combinations set
      ChooseFirstExtendEndorsePair -> skeleton_0
      if skeleton_0 == nil: continue
      currentBest = skeleton_0
      post skeleton_0 -> filterCh

      improvement loop:
        re-query tippool for fresh endorsement candidates
        filter out already-checked combinations
        if no untried candidates: break
        for each untried candidate:
          push (currentBest.Clone(), candidate) -> jobCh
          mark combination as checked
        collect results from resultCh
        pick best by coverage among results
        if best > currentBest coverage:
          close currentBest
          currentBest = best
          post currentBest -> filterCh
          close all other results
          continue
        else:
          close all results
          break
        check for new own milestone -> restart round if changed

Filter goroutine:
  reads from filterCh
  compares against bestCoverageSoFar
  if strictly better:
    forward to outCh
    update bestCoverageSoFar
  else:
    close and discard

outCh (output channel, buffered):
  consumer reads at own pace
  keeps skeleton with biggest coverage, closes rest
```

#### Code organization

- Package: `sequencer/factory/`
- `ChooseFirstExtendEndorsePair` and related extend-endorse selection logic moves here
  from `sequencer/task/proposer.go`. The current proposers will import from factory.
- Environment interface: similar to backlog's — needs tippool, branches, own milestones,
  attacher.Environment
- For now: standalone, not integrated into the sequencer. Test and debug independently.
  Later: integrate as the skeleton source for the sequencer's proposal pipeline.

#### Not in scope for TSF

- Branch transactions (handled by existing b0 proposer)
- Tag-along / delegation input insertion (consumer's responsibility)
- Transaction building and submission (consumer's responsibility)
- Boot/bootstrap proposer (explicit baseline case)

#### Integration plan (later)

When TSF is proven reliable, it replaces e1/e2/e3/r2/r3 proposers in the sequencer.
The existing sequencer loop reads from TSF's output channel instead of running `task.Run`.
The b0 (branch) and x (boot) proposers remain unchanged.
The sequencer adds tag-along inputs to the skeleton and builds the transaction.
