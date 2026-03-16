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

## High-level plan for `sequencer2`

### Core architectural shift

From **"fixed-target racing"** (pick timestamp -> race 7 proposers -> submit best)
to **"strategy-driven pipeline with incremental building"** (strategies build and clone ->
worker finalizes -> coordinator submits on tick grid).

### Architecture

```
sequencer2
+-- strategies/           -- static plugins, each a concrete type implementing Strategy interface
|   +-- safety            -- ensures branch tx + at least 1 milestone per slot (combines b0/x roles)
|   +-- endorsement       -- incremental endorsement building with clone-and-extend
|   +-- reactive          -- responds to high-coverage tips arriving in backlog
|
+-- worker                -- single goroutine: receives skeletons, inserts tag-alongs/delegations
+-- coordinator           -- tick-based sampling of proposal pool, decides submit/skip
+-- shared/
|   +-- extend-endorse pair selection (extracted from current proposer.go)
|   +-- IncrementalAttacher + Clone()
|
+-- backlog               -- reuse existing (CandidatesToEndorseSorted, etc.)
+-- own_milestones        -- reuse existing (FutureConeOwnMilestonesOrdered, etc.)
```

### Pipeline flow

**Strategy -> Proposer -> Worker -> Coordinator**

The strategy is persistent and autonomous. It polls the backlog for endorsement candidates,
selects (extend, endorse) pairs using the shared selection logic, and builds IncrementalAttachers.

Each proposer receives a **bounded task** from its strategy: target timestamp (or bounds), number of
endorsements to attempt, max inputs. The proposer executes deterministically against that target.
It does not poll or adapt -- it runs the IncrementalAttacher to completion and returns.

The key insight: **an IncrementalAttacher with 1 endorsement is already a valid proposal.**
The strategy hands this skeleton to the worker for finalization AND simultaneously clones the attacher
to spawn another proposer that tries adding more endorsements. This means no work is wasted -- every
intermediate result is submittable, and the clone-and-extend is purely additive.

```
Strategy (persistent, one per type):
  polls backlog for endorsement candidates
  selects (extend, endorse) pair using shared logic
  builds IncrementalAttacher with 1 endorsement    <-- VALID PROPOSAL
    |-- sends skeleton to Worker
    +-- clones attacher
          |-- spawns Proposer(clone, candidate2, target) -> skeleton -> Worker
          +-- optionally spawns Proposer(clone, candidate2b, target) -> skeleton -> Worker
                (bounded fan-out: try different 2nd endorsement candidates in parallel)

Worker (single goroutine):
  receives skeleton from proposer
  inserts tag-alongs / delegations greedily (sorted by value from backlog)
  sends finished proposal to Coordinator

Coordinator (tick-based sampling):
  maintains proposal pool
  at each tick checkpoint within the slot: evaluate pool
    - compare coverage, endorsement count, timing
    - submit best if threshold met or deadline approaching
  flush at pre-branch consolidation boundary
```

### Slot timing structure

The coordinator's tick grid respects the slot's structural constraints:

```
slot start -- post-branch consolidation (ticks 0..11) -- active period -- pre-branch consolidation -- slot end
               no sequencer tx allowed                    full operation    no tag-alongs allowed
```

Early in the active period, the coordinator waits (more proposals arriving). As the slot progresses,
urgency increases. Near pre-branch consolidation, it must submit what it has. The time grid also
determines when strategies should stop spawning new proposers.

### IncrementalAttacher cloning

Cloning is the critical enabler. It does not exist today and must be implemented.

**Clone semantics:**
- **Share** (immutable from attacher's perspective): pointers to WrappedTx vertices, baseline state reader,
  transaction data
- **Deep copy** (mutable state): consumed output set, conflict tracking set, endorsement/input lists,
  coverage accumulators
- **Fork**: delta transaction context -- the clone starts a fresh delta layer from the same base state

The existing delta transaction mechanism (`InsertEndorsement` wraps in a delta with rollback on failure)
already implies a layered state model. Cloning is essentially "snapshot the current layer, start a new
one on top."

### Strategies as static plugins

Each strategy is a concrete type implementing a common interface, registered at compile time.
The set can grow by adding code but is fixed at compile time. No runtime discovery.

```go
type Strategy interface {
    Name() string
    Run(ctx context.Context, env StrategyEnv)
}
```

Where `StrategyEnv` provides access to backlog, own milestones, the skeleton channel to the worker,
and the shared extend-endorse pair selection logic.

**Planned strategies:**

- **Safety**: ensures the sequencer has at least one milestone per slot and a branch transaction at the
  slot boundary. Uses the simplest possible transaction (own chain extension, maybe one endorsement).
  Combines the roles of current `b0` and `x` strategies.

- **Endorsement**: the main optimization strategy. Builds 1-endorsement attacher, clones and tries
  adding more endorsements. The clone-chain produces multiple proposals of increasing quality arriving
  over time. Separate instances can target different endorsement counts (like current e1/e2/e3 but
  sharing work via cloning instead of duplicating it).

- **Reactive**: responds to high-coverage tips appearing in the backlog. When a valuable endorsement
  opportunity arrives, immediately tries to grab it. Fast, opportunistic.

### Coordinator heuristics (open research question)

The coordinator's decision logic is a core part of the research task. It must balance:
- Coverage quality of proposals in the pool
- Time remaining in the slot
- Whether a milestone has already been submitted this slot (safety satisfied?)
- Properties of the proposal (endorsement count, input count, coverage delta)

Some redundancy in submitted transactions is inevitable and acceptable -- the sequencer issuing
conflicting transactions is normal behavior for coverage maximization. The coordinator's job is to
avoid submitting clearly inferior proposals while not holding back good-enough ones for too long.

### Implementation phases

**Phase 1 -- Scaffold and IncrementalAttacher cloning**

Scaffold `sequencer2` package with the new architecture (not a clone of `sequencer`). Reuse
startup/config infrastructure from `sequencer`. Implement `IncrementalAttacher.Clone()` in
`core/attacher/` -- this is shared infrastructure that benefits both sequencer versions.

**Phase 2 -- Pipeline: strategies, worker, coordinator**

Implement the strategy interface, the single worker goroutine, and the tick-based coordinator.
Start with the safety strategy only, to validate the pipeline end-to-end.

**Phase 3 -- Endorsement and reactive strategies**

Implement the endorsement strategy with clone-and-extend. Implement the reactive strategy.
Extract the shared extend-endorse pair selection logic from current `sequencer/task/proposer.go`.

**Phase 4 -- Test and iterate on testnet**

Deploy sequencer2 alongside sequencer on the 4-node testnet. Compare:
- Coverage growth rate per slot
- Endorsements per milestone (expect increase)
- CPU utilization per milestone (expect decrease due to fewer wasted attachers)
- Branch inclusion rate (must not regress)
- Tag-along and delegation consumption rates
- Behavior under varying spammer load

Iterate on heuristics (coordinator thresholds, strategy timing, clone fan-out) based on observations.
This is expected to be an ongoing process -- the plan is a starting direction, not a final design.
