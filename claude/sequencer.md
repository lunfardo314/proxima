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

### Incremental optimization plan

Refactor the existing `sequencer` step by step. Each step is independently testable on testnet.
No new package, no new architecture — just targeted improvements.

**Step 1 — Use Clone() to eliminate duplicate attacher creations**

The main source of waste: e1, e2, e3, r2, r3 all call `ChooseFirstExtendEndorsePair` independently,
creating new IncrementalAttachers from scratch for the same (extend, endorse) pairs.

Instead: e1 finds the first valid 1-endorsement attacher. If it wins the proposal round, e2 can
**clone** it and try adding a 2nd endorsement, rather than recreating from scratch. Similarly e3
clones e2's result.

This requires changing how proposers share state within a `task.Run` round. Currently they are
independent goroutines. The change: let e1 publish its best attacher; e2 picks it up and clones.

**Step 2 — Profile and measure**

Before further optimization, instrument the sequencer with metrics:
- Time spent in attacher creation vs input insertion vs tx building
- Number of attacher creations per slot (before and after Step 1)
- Endorsement count distribution per submitted milestone
- CPU time per proposal by strategy

This will show where the real bottlenecks are.

**Step 3 — Evaluate submit-wait pattern**

Currently `submitMilestone` → `waitMilestoneInTippool` blocks the sequencer loop. This means
the sequencer cannot start working on the next target while waiting. Evaluate whether:
- Submission can be decoupled (submit in background, continue proposing)
- The wait provides essential back-pressure that should be preserved
- A hybrid approach works (wait with timeout, continue if slow)

**Step 4 — Reduce proposer count**

Based on metrics from Step 2, consider:
- Dropping e3/r3 if they still never win after Step 1
- Merging r2 into e2 (alternate sorted/shuffled within the same proposer)
- Making proposer count configurable

**Step 5 — Further clone-based optimizations**

With metrics guiding decisions:
- Clone across targets: if the next target is close to the previous one, clone the previous
  round's best attacher instead of starting fresh
- Clone for input insertion: build the endorsement skeleton first, clone it, insert different
  sets of tag-alongs/delegations to compare

Each step is a small, testable change to the existing codebase.
