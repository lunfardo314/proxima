*Warning! This repository is under ongoing development. It is an alpha version and
definitely contains bugs. Do not use it in production.*

# Proxima: a DAG-based cooperative distributed ledger

Proxima is permissionless and decentralized like Bitcoin, under the usual Nakamoto
assumptions, but without proof-of-work. Sybil resistance comes from token holdings, as in
proof-of-stake — yet there is no committee, static or dynamic, so it is not BFT-style PoS.
Instead, Proxima is based on **cooperative consensus**.

- [Proxima whitepaper](https://arxiv.org/abs/2411.16456)
- [Proxima documents](https://lunfardo314.github.io/), including:
   - [Overview of Proxima concepts](https://lunfardo314.github.io/#/overview/intro)
   - [Transaction model](https://lunfardo314.github.io/#/txdocs/intro)
   - [UTXO scripting](https://lunfardo314.github.io/#/ledgerdocs/library)

## Testnet

See [how to join the open testnet](https://lunfardo314.github.io/#/participate/testnet).

## Introduction

Proxima organizes its ledger as a directed acyclic graph (DAG) of UTXO transactions,
rather than a chain of blocks — a _transaction DAG_, not a _blockDAG_. There are no
blocks, no mempool, and no block proposers. Because UTXO transactions are deterministic,
their canonical ordering is natural and needs no separate sequencing step.

Token holders are the only participants. Consensus on the ledger state emerges from their
profit-driven behavior, which is viable only when they cooperate by following the
**biggest ledger coverage** rule — the analogue of the _longest chain_ rule in
proof-of-work. Finality is _probabilistic_: non-deterministic and subjective. Hence
**cooperative consensus**.

The repository contains an experimental testnet version of the node, intended for research
and development, along with basic tools including a wallet.

## Highlights

* **Fully permissionless.** Anyone can take part simply by holding tokens — no
  registration, committee selection, or voting. The set of participants is open and
  unbounded.
* **Token holders are the only participants.** No miners, validators, or committees — and
  so no conflicting interests between participant classes. Sybil resistance is token-based
  ("skin in the game"), like PoS. No ASICs, GPUs, or mining pools.
* **Leaderless.** No block proposer or consensus leader. Participants exchange a single
  message type: raw transactions.
* **Cooperative, not competitive.** Consensus emerges from cooperation. Following the
  biggest-coverage rule is the optimal (Nash-equilibrium) strategy, analogous to Bitcoin's
  longest-chain rule.
* **No rounds, no global view.** Participants operate without rounds and without knowing
  all peers, their states, or voting history.
* **Conflict resolution, not sequencing.** Canonical ordering of transactions follows
  naturally from resolving conflicts between UTXOs; no block sequencing is needed.
* **Self-healing.** The network may partition or fork under communication delays. On
  reconnection, the biggest-coverage rule resolves the forks automatically.
* **High throughput and decentralization.** Massive parallelism with no global bottleneck;
  each node processes transactions concurrently, and assets reach finality independently.
  Decentralization is on par with PoW.
* **Probabilistic finality.** Usually reached within 1–3 slots (about 10–30 seconds).
  Thanks to UTXO determinism, confirmations can stream in batches without waiting for
  earlier ones.
* **Low cost and energy.** Per-transaction cost comparable to PoS; energy use far below
  PoW.
* **Spam prevention.** Transaction rate limits per token holder, enforced both at the
  ledger level and in the in-memory buffer (memDAG).
* **Simplicity.** Aside from Bitcoin, the design is simpler than most PoS and DAG-based
  systems, which tend to rely on complex consensus machinery.

## Further information

* [Technical whitepaper (pdf)](https://arxiv.org/abs/2411.16456) — detailed description of
  the cooperative ledger concept.
* [Simplified presentation of Proxima concepts](https://hackmd.io/@Evaldas/Sy4Gka1DC) —
  fewer technical details, more pictures.
* [Introduction to cooperative consensus (videos)](https://youtu.be/XT6GBSLCbZo).
* Tutorials and instructions:
  * [CLI wallet program `proxi`](https://lunfardo314.github.io/#/participate/proxi)
  * [Running a standalone single-node network](https://lunfardo314.github.io/#/participate/run_standalone)
  * [Running an access node](https://lunfardo314.github.io/#/participate/run_access)
  * [Running a node with a sequencer](https://lunfardo314.github.io/#/participate/run_sequencer)
  * [Running a small testnet in Docker](tests/docker/docker-network.md)
  * [Delegation in `proxi`](https://lunfardo314.github.io/#/participate/delegate)
  * [How to join the testnet](https://lunfardo314.github.io/#/participate/testnet)
