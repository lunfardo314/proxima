# TODO list

The list contains main pressing topics not covered yet in the code. **The list is never complete.**

## Tooling
* UTXO tangle visualizer
  - Concept: something similar to existing. Dynamic visualization of UTXO tangle: transaction memDAG, 
all types of transactions, highlighting chains, stems, branches, orphanage, etc
  - Implementation: 0%
  
* Ledger explorer
  - Concept: web browser-based explorer. Mostly interacts with **TxStore**. 
Search and explore UTXO tangle along various links, view each all kinds of transactions down to individual decompiled _EasyFL constraint source level_
  - Implementation: 0%

* Proxi CLI, wallet
Needs love ant attention. Currently rudimentary only. Also API

## Node components
* Auto-peering
  * Concept: currently only manual peering is implemented. It means node's config mush be changed and node restarted. The goal would be to implement usual auto-peering
  * Implementation: 0%
* Metrics subsystem
  * Concept: Prometheus metrics for node, ledger and sequencer. Needs design.
  * Implementation 0%
* Spam prevention
  * Concept: in head plus described in WP, 30%. Needs experimental development and design
  * Implementation: 0%
* TxStore as separate server. 
  * Concept: currently, TxStore is behind very simple interface. The whole txStore can be put into separate 
server to be shared by several nodes and ledger explorer. In head 60%
  * Implementation: 0%

## Ledger
General status: the Proxima ledger definitions are based on standard _EasyFL_ script library and its extensions.  
[EasyFL](https://github.com/lunfardo314/easyfl) itself is essentially completed, however its extension in the Proxima ledger requires improvement in several areas, 
mostly related to the upgradability of the ledger definitions.

* Put full ledger script library into the multi-state database 
  - Concept: currently sources of the ledger *EasyFL* library extensions are hardcoded in the node. It is not necessary. 
Better to put it all into the state, as immutable part of the ledger identity. In head 30%
  - Implementation: 0%
* Make ledger library upgradable  
  - Concept: currently any library modifications are breaking. The goal would be to make it incrementally extendable 
with backward compatibility via soft forks.
Library upgrades may be in the form of special immutable outputs and/or special immutable leafs in the merkle tree. In head maybe 20%.
  - Implementation: only library components in _EasyFL_. On the node: 0%
* Make ledger library upgradable with stateless computations for new cryptography or similar
  - Concept: explore some fast, deterministic, platform-agnostic VMs (e.g. RISC V, maybe even LLVM). Only vague ideas, some 10% in head
  - Implementation: 0%
* Make model of output locks fully in _EasyFL_ 
  - Concept: Currently, only hardcoded list of lock constraint scripts is possible. 
This is because the lock concept is not fully abstracted in the node. This is suboptimal not necessary. 
All ledger constraint and account indexing logic can be expressed in _EasyFL_ 100%. The lock model must be revisited and fully abstracted in the node. 
Then locks will be fully expandable programmable, especially in combination with ledger library upgrade feature

# Docs
- Whitepaper 80-90%
- How to run small testnet: 0% 
- Introductory video series: 0%