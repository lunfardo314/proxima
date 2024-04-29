# TODO list

The list contains main topics not covered yet in the code. **The list is never complete.**

## Tooling
* UTXO tangle visualizer
  - Concept: similar to exiting. Dynamic visualization of UTXO tangle: transaction mam DAG, sequencer and other transactions, branches, orphanage, etc
  - Implementation: 0%
  
* Ledger explorer
  - Concept: web browser-based explorer. Mostly interacts with **TxStore**. 
Allows search and explore UTXO tangle along various links, view each all kinds of transactions down to individual decompiled _EasyFL constraint source level_
  - Implementation: 0%

## Node components
* Metrics subsystem
  * Concept: Prometheus metrics for node, ledger and sequencer.
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
[EasyFL](https://github.com/lunfardo314/easyfl) itself is essentially completed, however its extension in the Proxima ledger needs love.
It requires improvement in several areas.

* Full ledger script library with sources in the ledger state 
  - Concept: currently sources of the ledger *EasyFL* library extensions are hardcoded in the node. It is not necessary. 
Better put it all into the state. In head 30%
  - Implementation: 0%
* Make ledger library upgradable  
  - Concept: currently all library modifications are breaking. Make it incrementally extendable with backward compatibility 
Library upgrades may be in the form of special immutable outputs. In head maybe 20%.
  - Implementation: only library components in _EasyFL_. On node: 0%
* Make ledger library upgradable with stateless computations for new cryptography on similar
  - Concept: explore some fast, deterministic, platform-agnostic VMs (e.g. RISC V, maybe even LLVM). Only vague ideas, some 10% in head
  - Implementation: 0%
* Model of output locks fully in _EasyFL_ 
  - Concept: Currently, only hardcoded list of lock constraint scripts is possible. 
This is because the lock concept is not fully abstracted in the node. This is not necessary. 
All ledger constraint and account indexing logic can be expressed in _EasyFL_ 100%. The lock model must be revisited and fully abstracted in the node. 
Then locks will be fully expandable programmable, especially in combination with ledger library upgrade feature 