*Warning! This repository contains an ongoing development. Currently, it is in an experimental (prototype) version. It definitely contains bugs.
The code should not be used in production!*

# Proxima: a DAG-based cooperative distributed ledger

Proxima is as decentralized and permissionless as Bitcoin (*proof-of-work*, PoW). 
<br>It is similar to *proof-of stake* (PoS), especially because of its energy-efficiency and throughput.
<br>Yet it is neither PoW, nor a PoS system. It is based on **cooperative consensus**. See [whitepaper](docs/Proxima_WP.pdf).

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

Currently, the project is in an **experimental development stage**. 

The repository contains Proxima node prototype intended for experimental research and development. 
It also contains some tooling, which includes rudimentary wallet functionality.

## Highlights of the system's architecture

* **Fully permissionless**, unbounded, and globally unknown set of pseudonymous participants. Participation in the ledger as a user is equivalent to participation as "validator", it is fully open. 
There is no need for any kind of permissions, registration, committee selection or voting processes. Nobody tracks existing participants nor a category of it.
* **Token holders are the only actors** authorized to write to the ledger (issue transactions). No miners, no validators, no committees.
* **Influence of the participant** is proportional to the amount of its holdings, i.e. to it's _skin-in-the-game_. 
* **Liquidity of the token** is the only prerequisite for the system to be fully permissionless
* **Leaderless determinism**. The system operates without a consensus leader or block proposers, providing a more decentralized approach.
* **Nash equilibrium** is achieved with the optimal strategy of **bigger ledger coverage rule** (analogous to the _longest chain rule_ in PoW blockchains).
* Unlike in blockchains, the optimal strategy is **cooperative**, rather than **competitive**. This facilitates broad social consensus among participants
* **High throughput**, as a result of **massive parallelism** and **absence of global bottlenecks**
* **High level of decentralization**, probably the highest achievable in distributed ledgers 
* **Low energy requirements**, unlike PoW. 
* **Low cost per transaction**, like PoS
* **Asynchrony**. The architecture relies only on weak assumptions of synchronicity.
* **Probabilistic finality**. Depends on subjective assumptions, similar to 6-block rule in Bitcoin. Normally 1-3 slots (10-30 sec) is enough. 
Due to the determinism, strong finality is not needed to issue follow-up transactions, therefore transactions can be issued in deterministically-bundled batches
* **No need for a global knowledge** of the system state, such as composition of the committee, assumptions about node weights/stakes, or other registries.
* **1-tier trust assumptions**. Only token holders are involved in the process, as opposed to the multiple layers of trust required in other blockchain systems such as PoW (which includes users and miners) 
and PoS (which includes at least users, block proposers, committee selection and committee).
* **Absence of the deterministic global state**. Due to the non-determinism of the ledger account (a set of outputs), 
the account cannot be used as a single global liquidity pool in the smart contract.
* **Parallelism at the distributed consensus level**. Assets converge to their final states in parallel.
* **Parallelism** at the node level. All transactions are processed in parallel on each node.
* **Spamming prevention** is based on the transaction rate limits per user (token holder): at the ledger level and at the pre-ledger buffer (_memDAG_) level
* **Simpler than most**, except Bitcoin. The above facilitates clear and relatively simple overall concept and node architecture, 
much simpler than most PoS systems, which are usually complex in their consensus layer and due to synchronicity requirements 

## Further information
* The [technical whitepaper](docs/Proxima_WP.pdf) contains detailed description of the *cooperative ledger* concept
* [TODO list](TODO.md) contains most pressing development topics with their completion status
* here the [tutorial how to run a small Proxima testnet](tests/nodes/README.md)
* Introductory videos:
  * [1. Introduction. Principles of Nakamoto consensus](https://youtu.be/qDnjnrOJK_g)
  * [2. UTXO tangle. Ledger coverage](https://youtu.be/CT0_FlW-ObM)
  * [3. Cooperative consensus](https://youtu.be/7N_L6CMyRdo)
 
 
