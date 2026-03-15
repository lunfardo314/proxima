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
