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

We will start `sequencer2` as a clone of the `sequencer` and in the process of optimization will improve it step-by-step.
The `sequencer` can be seen as a reference implementation for the `sequencer2`

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
- freezing and tag-along is less priority, however those transactions can bring huge amount of coverage
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

### Metrics observations (5-node testnet, 217 senders, ~10-15 TPS)

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

6. **Inflation urgency vs coverage quality.** The sequencer must secure at least one milestone in the
   branch to capture the slot's inflation. Waiting too long for optimal coverage risks being orphaned
   by other sequencers, especially under high load. The strategy must balance "secure inflation early"
   with "maximize coverage when possible."

7. **Sequencer timings are likely naturally staggered** across different sequencers within a slot,
   though the extent is not precisely known.

### Refined requirements

- Keep the proposer/task framework: multiple strategies submit proposals to a queue, coordinator decides
- **Flexible opportunism**: don't commit rigidly to endorsement count or timing; grab what's available,
  keep improving until time budget expires
- **Minimize attacher creations**: prefer incremental improvement of existing candidates over throwaway speculation
- **Adaptive pace**: slower and richer under low load (more endorsements), faster under high load (avoid orphaning)
- **Inflation-first, then optimize**: secure a good-enough milestone early in the slot, then improve
  with follow-up milestones if time and coverage opportunity permit
- **Coexist with `sequencer`**: config-selectable, shared components must serve both
- Designed as a stable, extensible reference implementation — not over-optimized for current testnet size
- Assume the least possible minimal requirements for node hardware

---

## High-level plan for `sequencer2`

### Core architectural shift

From **"fixed-target racing"** (pick timestamp → race proposers → submit best)
to **"incremental candidate building with adaptive submission"** (build and improve candidates → submit on trigger).

Proposers still compete via the proposal queue, but the focus shifts from generating many
throwaway proposals to incrementally building fewer, better ones.

### Phase 1 — Clone and restructure the loop

Create `sequencer2` package as a clone of `sequencer`. Restructure the main loop:

**Current**: `pickTarget() → task.Run(deadline) → submitBest()`
**New**: `maintainCandidates() → submitWhen(trigger)`

The submission trigger fires when:
- Coverage gain exceeds a threshold (adaptive to current load), OR
- Time budget for the current target expires, OR
- Slot boundary approaches (must secure branch transaction)

The "inflation-first" principle: early in the slot, submit threshold is low (secure *something*).
As more time passes without submission, threshold drops further. Near slot end, submit whatever is best.

### Phase 2 — Consolidate proposer strategies

Replace 7 overlapping strategies with fewer, smarter ones:

- **Branch proposer**: dedicated handler for slot boundary transactions (currently b0's dual role)
- **Greedy endorsement builder**: starts with the best single endorsement, incrementally tries adding
  2nd, 3rd, etc. One attacher, extended step by step. Replaces e1/e2/e3/r2/r3
- **Bootstrap proposer**: retained for the explicit-baseline case (currently x/boot)
- **Opportunistic proposer**: reacts to newly arrived tips — when a high-coverage tip appears in
  the backlog, immediately tries to endorse it

Each proposer submits candidates to the queue. The coordinator tracks the current best and decides
when to submit based on the adaptive trigger.

### Phase 3 — Adaptive timing

Replace fixed pace with load-adaptive behavior:

- **Low load** (few endorsable tips arriving): wait longer, aim for more endorsements per milestone.
  Coverage gain per extra endorsement is exponential, worth the wait.
- **High load** (many tips, risk of orphaning): submit faster with fewer endorsements.
  Being included matters more than being optimal.
- **Inflation pressure**: as time passes in a slot without a submitted milestone, urgency increases.
  Near slot end, submit the best available regardless of quality.

The adaptation can be driven by observable signals: rate of new tips in backlog,
time since last own milestone, time remaining in slot.

### Phase 4 — Test and iterate on testnet

Deploy sequencer2 alongside sequencer on the 5-node testnet. Compare:
- Endorsements per milestone (expect increase)
- Coverage growth rate per slot
- CPU utilization per milestone (expect decrease due to fewer wasted attachers)
- Branch inclusion rate (must not regress)
- Behavior under varying spammer load

Iterate on heuristics (thresholds, timing parameters) based on observations.
This is expected to be an ongoing process — the plan is a starting direction, not a final design.
