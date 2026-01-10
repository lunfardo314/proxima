*Warning! This repository contains an ongoing development. Currently, it is in an alpha version. It definitely contains bugs.
The code should not be used in production!*

# Proxima: a DAG-based cooperative distributed ledger
Proxima is as decentralized and permissionless as Bitcoin (*proof-of-work*, PoW). 
It runs under common Nakamoto-assumptions (permissionless decentralization). 
<br>It a sort of *proof-of stake* (PoS) protocol, because because it uses Sybil protection by token holdings.
<br>It is not a BFT-PoS because it is Nakamoto-permissionless and does not run on a committee, neither static, nor dynamic. 
<br>Proxima is based on **cooperative consensus**. See: 

- [Proxima whitepaper](https://arxiv.org/abs/2411.16456) 
- other [Proxima documents](https://lunfardo314.github.io/), which include:
   - [Overview of Proxima concepts](https://lunfardo314.github.io/#/overview/intro)
   - [Transaction model](https://lunfardo314.github.io/#/txdocs/intro)
   - [UTXO scripting](https://lunfardo314.github.io/#/ledgerdocs/library)

## Testnet 

Please read instructions [how to join the open testnet](docs/testnet.md).

## Introduction
Proxima presents a novel architecture for a distributed ledger, commonly referred to as a "blockchain." 
The ledger of Proxima is organized in the form of directed acyclic graph (DAG) with UTXO transactions as vertices, 
rather than as a chain of blocks. It is a _transaction DAG_ (as opposed to the _blockDAG)_. Proxima does not use blocks and does not have a _mempool_. 
There's no need for block proposers. Due to the UTXO determinism, canonical ordering of transactions is natural, and there's no need for _sequencing_ of them in the sense of blockchains. 

Consensus on the state of ledger assets is achieved through the profit-driven behavior of token holders that are the only
category of participants in the network. This behavior is viable only when they cooperate by following the _biggest ledger coverage_ rule, 
similarly to the _longest chain rule_ in PoW blockchains. Consensus in Proxima is _probabilistic_, i.e., the finality is non-deterministic and subjective. 
Hence, **cooperative consensus**

Currently, the project is in an **ongoing development stage**. 

The repository contains a testnet version of the Proxima node. It is intended for experimental research and development. 
It also contains some tools, which includes basic wallet functionality.

## Highlights of the system's architecture
* **Fully permissionless**. The system supports an open, undetermined, and unbounded set of pseudonymous consensus participants. Anyone can participate in the ledger as a user simply by holding tokens—no permissions, registration, committee selection, or voting processes are required.
* **Token holders are the only participants**. There are no miners, validators, committees, or other paid third parties with their own interests.
* **Sybil resistance** is token-based: similar to PoS, influence is proportional to token holdings — i.e., one’s _skin in the game_.
* **Token liquidity**: The existence of a liquid market price for the token is the sole requirement for the system to remain permissionless. Token ownership is the only prerequisite for participating in any role. 
* **No ASICs, no GPUs, no mining pools**.
* **Multi-leader (leaderless)**: The system does not rely on selecting a consensus leader or block proposer, resulting in a more decentralized approach.
* **Nash equilibrium**: Achieved through the optimal strategy known as the biggest ledger coverage rule, analogous to Bitcoin's longest chain rule in PoW.
* **"Oblivious" consensus protocol**: Consensus participants operate without "rounds" and without knowledge of all participants, peer states, voting, or communication history.
* **Cooperative strategy**: Unlike traditional blockchains, consensus emerges through **cooperation rather than competition**, eliminating the need for leader election and promoting social consensus.
* **Conflict resolution is the primary goal of consensus**: There is no requirement for sequencing. Canonical transaction ordering (e.g., among conflicting UTXOs) naturally results from conflict resolution. This contrasts with blockchains, where strict sequencing is needed to ensure determinism and prevent double spending. 
* **High throughput**: Enabled by **massive parallelism** and the **absence of global bottlenecks**.
* **High decentralization**: Among the highest achievable in distributed ledger systems, on par with PoW.
* **Low energy consumption**: Unlike PoW-based systems.
* **Low cost per transaction**: Comparable to PoS systems.
* **Single message type**: Participants exchange only raw transactions.
* **Asynchronous operation**: The network may temporarily partition or fork due to communication delays or even complete disconnection. Upon reconnection, forks are resolved using the **biggest ledger coverage rule**, enabling the network to **self-heal**. 
* **Peer cooperation incentives**: Participants benefit by staying closely connected to large token holders. Isolation may result in orphaned transactions or missed opportunities, such as periodic inflation rewards. 
* **Approximate clock synchronization**: Participants are incentivized to maintain local clocks roughly aligned with the global time. 
* **Probabilistic finality**: Similar to Bitcoin’s six-block rule. In practice, finality is usually achieved within 1 to 3 slots (10–30 seconds), depending on network conditions. Thanks to the deterministic nature of UTXO transactions, confirmations can occur in batches or streams without waiting for prior confirmations.
* **Single-tier trust model**: Only token holders participate. This differs from PoW (users + miners) and PoS (users + proposers + committees), reducing complexity of trust assumptions. 
* **Aligned incentives**: Having a single participant class avoids conflicts of interest (e.g., between holders and miners).
* **Consensus-level** parallelism: Assets reach finality independently yet in a cooperative manner.
* **Node-level parallelism**: Each node processes all transactions concurrently.
* **Spam prevention**: Enforced through transaction rate limits per token holder, both at the ledger level and in the pre-ledger buffer (memDAG).
* **Simplicity**: Aside from Bitcoin, this system is simpler than most PoS and DAG-based architectures, which tend to involve complex consensus mechanisms. The overall concept and node design are relatively straightforward. 

## Further information
* [Technical whitepaper (pdf)](https://arxiv.org/abs/2411.16456) contains a detailed description of the *cooperative ledger* concept
* [Simplified presentation of Proxima concepts](https://hackmd.io/@Evaldas/Sy4Gka1DC) skips many technical details and adds more pictures
* Tutorials and instructions (outdated somehow):
  * [CLI wallet program `proxi`](docs/proxi.md)
  * [Running access node](docs/run_access.md)
  * [Running node with sequencer](docs/run_sequencer.md)
  * [Running small testnet in Docker](tests/docker/docker-network.md)
  * [Delegation in `proxi`](docs/delegate.md)
  * [How to join testnet?](docs/testnet.md)
* 
* Introduction to the cooperative consensus ([videos](https://youtu.be/XT6GBSLCbZo))
