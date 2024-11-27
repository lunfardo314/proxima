*Warning! This repository contains an ongoing development. Currently, it is in an early alpha version. It definitely contains bugs.
The code should not be used in production!*

*For up-to-date information see `develop` branch*

# Proxima: a DAG-based cooperative distributed ledger
Proxima is as decentralized and permissionless as Bitcoin (*proof-of-work*, PoW). 
<br>It is similar to *proof-of stake* (PoS), especially because of its energy-efficiency and throughput.
<br>Yet it is neither PoW, nor a PoS system. It is based on **cooperative consensus**. See [whitepaper on arxiv.org](https://arxiv.org/abs/2411.16456) and 
[simplified presentation of Proxima concepts](https://hackmd.io/@Evaldas/Sy4Gka1DC).

## Introduction
Proxima presents a novel architecture for a distributed ledger, commonly referred to as a "blockchain". 
The ledger of Proxima is organized in the form of directed acyclic graph (DAG) with UTXO transactions as vertices, 
rather than as a chain of blocks. 

Consensus on the state of ledger assets, is achieved through the profit-driven behavior of token holders which are the only
category of participants in the network. This behavior is viable only when they cooperate by following the _biggest ledger coverage_ rule, 
similarly to the _longest chain rule_ in PoW blockchains. 

The profit-and-consensus seeking behavior is facilitated by enforcing purposefully designed UTXO transaction validity constraints.
The only prerequisite is liquidity of the token. Thus, participation of token holders in the network is naturally and completely permissionless. 
Proxima distributed ledger does not require special categories of miners, validators, committees or staking, with their functions and trust assumptions.

In the proposed architecture, participants do not need knowledge of the global state of the system or the total order of ledger updates. 
The setup allows to achieve high throughput and scalability alongside with low transaction costs, 
while preserving key aspects of decentralization, open participation, and asynchronicity found in Bitcoin and other proof-of-work blockchains, 
but without unreasonable energy consumption. 

Sybil protection is achieved similarly to proof-of-stake blockchains, using tokens native to the ledger, 
yet the architecture operates in a leaderless manner without block proposers and committee selection.

Currently, the project is in an **ongoing development stage**. 

The repository contains Proxima node prototype intended for experimental research and development. 
It also contains some tools, which includes basic wallet functionality.

## Highlights of the system's architecture
* **Fully permissionless**, unbounded, and globally unknown set of pseudonymous participants. 
Participation in the ledger as a user is fully open, and it is equivalent to participation as "validator". 
There is no need for any kind of permissions, registration, committee selection or voting processes. Nobody tracks existing participants nor a category of it.
* **Token holders are the only actors** authorized to update the ledger. No miners, no validators, no committees nor any other third parties with their interest.
* **Influence of the participant** is proportional to the amount of its holdings, i.e. to it's _skin-in-the-game_. Being a _malicious token holder_ is an oxymoron.
* **Liquidity of the token** is the only condition for the system to be fully permissionless. Ownership of tokens is the only prerequisite for participation in any role. 
**No ASICs, no GPUs, no mining rigs**
* **Leaderless determinism**. The system operates without a consensus leader or block proposers, providing a more decentralized approach.
* **Nash equilibrium**. It is achieved with the optimal strategy of **biggest ledger coverage rule**, which is analogous to the Bitcoin's _longest chain rule_ in PoW blockchains.
* Unlike in blockchains, the optimal strategy is **cooperative**, rather than **competitive**. This facilitates social consensus among participants
* **Auto-healing after network partitioning**. Network partitioning usually leads to forking to several branches with less coverage. 
After network connections is restored, the fork with the biggest ledger coverage prevails, while other forks will be orphaned. Similarly to Bitcoin's _longest chain_.      
* **High throughput**, as a result of **massive parallelism** and **absence of global bottlenecks**
* **High level of decentralization**, probably the highest achievable in distributed ledgers 
* **Low energy requirements**, unlike PoW. 
* **Low cost per transaction**, like PoS
* **Asynchrony**. The architecture relies only on weak assumptions of synchronicity. The only essential assumption is existence of the global time reference. 
Participants are incentivized to maintain their clock approximately synchronized with the global clock. 
* **Probabilistic finality**. Depends on subjective assumptions, similar to 6-block rule in Bitcoin. Normally 1-4 slots (10-40 sec) is enough. 
Due to the UTXO transaction determinism, there's no need to wait for confirmation of the previous transaction, therefore transactions can be issued in batches or streams.
* **Consensus rule is local**, it does not require any global knowledge of the dynamic system state, such as composition of the committee, assumptions about node weights/stakes, or other registries.
* **1-tier trust assumptions**. Only token holders are involved in the process, as opposed to the multiple layers of trust required in other 
blockchain systems such as PoW (which includes users and miners) and PoS (which includes at least users, block proposers, committee selection procedure and the committee).
* **Absence of the deterministic global state**. Due to the non-determinism of the ledger account (a set of outputs controlled by a particular entity), 
it cannot be used as a single global liquidity pool in the smart contract.
* **Parallelism at the distributed consensus level**. Assets converge to their finality states in parallel, yet cooperating with each other.
* **Parallelism** at the node level. All transactions are processed in parallel on each node.
* **Spamming prevention** is based on the transaction rate limits per user (token holder): at the ledger level and at the pre-ledger buffer (_memDAG_) level
* **Simpler than most**, except Bitcoin. The above facilitates clear and relatively simple overall concept and node architecture, 
much simpler than most PoS systems, which are usually complex in their consensus layer and due to synchronicity requirements 

## Further information
* [Technical whitepaper (pdf)](https://arxiv.org/abs/2411.16456) contains detailed description of the *cooperative ledger* concept
* [Simplified presentation of Proxima concepts](https://hackmd.io/@Evaldas/Sy4Gka1DC) skips many technical details and adds more pictures
* Tutorials and instructions (outdated somehow):
  * [CLI wallet program `proxi`](docs/proxi.md)
  * [Running first node in the network](docs/run_boot.md)
  * [Running access node](docs/run_access.md)
  * [Running node with sequencer](docs/run_sequencer.md)
  * [Running small testnet in Docker](tests/docker/docker-network.md)
* Introductory videos:
  * [1. Introduction. Principles of Nakamoto consensus](https://youtu.be/qDnjnrOJK_g)
  * [2. UTXO tangle. Ledger coverage](https://youtu.be/CT0_FlW-ObM)
  * [3. Cooperative consensus](https://youtu.be/7N_L6CMyRdo)
* [TODO list](TODO.md) contains most pressing development topics with their completion status
