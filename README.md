# Proxima: a DAG-based cooperative distributed ledger

**Warning! This repository contains an ongoing development. Currently, it is in the experimental (prototype) version. It definitely contains bugs.
The code should no be used in production!**

A _distributed_, _decentralized_ and _permissionless_ UTXO ledger 
with DAG-based _cooperative_ and _leaderless_ consensus, also known as the _UTXO tangle_.

Proxima is like Bitcoin/PoW or PoS. Yet it is neither PoW, no PoS system 

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
but without huge energy consumption. 

Sybil protection is achieved similarly to proof-of-stake blockchains, using tokens native to the ledger, 
yet the architecture operates in a leaderless manner without block proposers and committee selection.

Currently, the project is in an **experimental development stage**. 

The repository contains Proxima node prototype intended for experimental research and development. 
It also contains some tooling, which includes rudimentary wallet functionality.

## Highlights of the system's architecture

* **Fully permissionless**, unbounded, and globally unknown set of pseudonymous participants. 
* **Token holders are the only actors** authorized to write to the ledger (issue transactions). There is no need for any kind of permissions, committees or voting processes.
* **Liquidity of the token** is the only prerequisite for the system to be fully permissionless
* **High throughput**, as a result of massive parallelism and no global bottlenecks
* **High level of decentralization**, probably the highest achievable in distributed ledgers 
* **Low energy requirements**, unlike PoW. 
* **Low cost per transaction**
* **Leaderless determinism**. The system operates without a consensus leader or block proposers, providing a more decentralized approach.
* **Asynchrony**. The architecture relies on only weak assumptions of synchronicity.
* **No need for a global knowledge** of the system state, such as composition of the committee, assumptions about node weights, stakes, or other registries.
* **1-tier trust assumptions**. Only token holders are involved in the process, as opposed to the multiple layers of trust required in other blockchain systems such as PoW (which includes users and miners) and PoS (which includes at least users, block proposers, committee(s)).
* **Absence of a deterministic global state**. Due to the non-determinism of the ledger account (a set of outputs), it cannot be used as a single global liquidity pool.
* **Parallelism at the consensus level**. Assets converge to their final state in parallel.
* **Parallelism** at the node level. All transactions are validated in parallel at each node.
* **Simpler than most**, except Bitcoin. The above facilitates clear and relatively simple overall concept and node architecture, much simpler than most PoS systems. It is still pretty complex 

## Further information
* The [technical whitepaper](docs/Proxima_WP.pdf) contains detailed description of the *cooperative ledger* concept
* [TODO list](TODO.md) contains most pressing development topics with their completion status
* here will be instructions how to run small Proxima testnet **TBD**
* here will be series of introductory video presentations about Proxima **TBD**

