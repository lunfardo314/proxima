# TODO list

The list contains main pressing topics not covered yet in the code. 

**The list does not cover full scope of further development, it is never complete.**

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

* Docker-ize small testnets

## Node components
* Auto-peering
  * Concept: currently only manual peering is implemented. It means node's config mush be changed and node restarted. The goal would be to implement usual auto-peering
  * Implementation: 0%
* Metrics subsystem
  * Concept: Prometheus metrics for node, ledger and sequencer. 
  * Implementation 10% (basic framework)
* RocksDB database
  * Currently, Badger is used. Replace it with RocksDB
* Spam prevention
  * Concept: in head plus described in WP, 30%. Needs experimental development and design
  * Implementation: 0% ( except transaction pace constraints in the ledger which is fully implemented)
* TxStore as separate server 
  * Concept: currently, TxStore is behind very simple interface. The whole txStore can be put into separate 
server to be shared by several nodes and ledger explorer. In head 60%
  * Implementation: 0%
* Multistate snapshots
  * Concept: saving multi state DB starting from given slot. Restoring it and starting node from it as a baseline. In head 70%
  * Implementation: %0
* Multistate pruning
  * Concept: most of the branch roots quickly become orphaned -> can be deleted from DB. In head 40%
  * Implementation: 0%
* Transaction store pruning
  * Concept: most of the transactions are not present into the final state -> can be deleted . In head 40%
  * Implementation: %0
* State pruning
  * Concept: currently transaction ID of every transaction is stored in the state root. If transaction contains unspent outputs,
it is OK and not redundant. After all outputs of the transaction are spent in the state, transaction ID is needed for some time to be able to
detect replay attempts. After some time it can be deleted from the state. It must be deleted deterministically, i.e. the same way in all nodes
  * Implementation: 0%

## Ledger
General status: the Proxima ledger definitions are based on standard _EasyFL_ script library and its extensions.  
[EasyFL](https://github.com/lunfardo314/easyfl) itself is essentially completed, however its extension in the Proxima ledger requires improvement in several areas, 
mostly related to the soft upgradability of the ledger definitions.

* Make ledger library upgradable  
  - Concept: currently any library modifications are breaking. The goal would be to make it incrementally extendable 
with backward compatibility via soft forks. Must be discussed. In head maybe 20%.
  - Implementation: _EasyFL_ part is mostly done. On the node: implemented simple version, which allows soft forks of the network. Maybe 30%-40%

* Make ledger library upgradable with stateless computations for new cryptography and similar
  - Concept: explore some fast, deterministic, platform-agnostic VMs (e.g. RISC V, maybe even LLVM). Only vague ideas, some 10% in head
  - Implementation: 0%

* Delegation implementation
  * Concept: (a) enable token holders delegate capital to sequencer with possibility to revoke it. It would be a lock to the chain-constrained output.
    (b) implement sequencer part. In head 50%
  * Implementation: 0%

* Tag-along lock implementation
  * Concept: modification of the _chain lock_, which conditionally bypass storage deposit constraints. In head: 80%
  * Implementation: 0%

* Practically reasonable storage deposit constrains and constants: 
  * Concept: needs simulation
  * Implementation: rudimentary version, maybe 10%

## Sequencer

* Enhance modularity of the sequencer
  * Concept: currently sequencer is composed of _proposer_ modules. Needs further improvement of the architecture. In head 20%
  * Implementation: 0%

* More advanced sequencer strategies with multiple endorsements
  * Concept: multi-endorsement strategies would contribute to the consensus convergence speed. In head 40%
  * Implementation: 30% (implemented proposer strategy with 2 endorsements)

## Docs
- Whitepaper 80-90%
- How to run small testnet: 80% 
- Introductory video series: 0%