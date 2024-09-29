# TODO list

The list contains main pressing topics not covered yet in the code. 

**The list does not cover full scope of further development. It is by no means complete.**

## Tooling
* Node dashboard:
  * Implementation: started
  
* UTXO tangle visualizer
  - Concept: something similar to existing. Dynamic visualization of UTXO tangle: transaction memDAG, 
all types of transactions, highlighting chains, stems, branches, orphanage, etc
  - Implementation: 0%
  
* Ledger explorer
  - Concept: web browser-based explorer. Mostly interacts with **TxStore**. 
Search and explore UTXO tangle along various links, view each all kinds of transactions down to individual decompiled _EasyFL constraint source level_
  - Implementation: 0%. Also lacks proper API. Another problem is that currently transaction can be fully parsed only in Go environment, Because of EasyFL.
EasyFL library needs rewrite to Rust and use as binary library or Wasm in other projects

* Docker-ize
  * Significant progress

## Node components
* RocksDB database
  * Currently, Badger is used. Suboptimal. Replace it with RocksDB
* Spam prevention
  * Concept: in head plus described in WP, 30%. Needs experimental development and design
  * Implementation: 10-20% (transaction pace constraints in the ledger is fully implemented)
* Multi-state pruning
  * Concept: most of the branch roots quickly become orphaned -> can be deleted from DB
  * Implementation: 0%
* Transaction store pruning
  * Concept: most of the transactions are not present into the final state -> can be deleted 
  * Implementation: %0
* Download past transactions to txStore from other nodes. Note, that normally nodes do not have (and do not need) pre-snapshot 
transactions
* State pruning
  * Concept: currently transaction ID of every transaction is stored in the state root. If transaction contains unspent outputs,
it is OK and it is not redundant. After all outputs of the transaction are spent in the state, transaction ID is still needed for some time to be able to
quickly detect replay attempts. After some time it becomes redundant and can be deleted from the state (trie). 
It must be deleted deterministically, i.e. the same way in all nodes
  * Implementation: 0%

## Ledger
General status: the Proxima ledger definitions are based on standard _EasyFL_ script library and its extensions.  
[EasyFL](https://github.com/lunfardo314/easyfl) itself is essentially completed, however its extension in the Proxima ledger requires improvement in several areas, 
mostly related to the soft upgradability of the ledger definitions.

* Make ledger library upgradable  
  - Concept: currently any library modifications are breaking. The goal would be to make it incrementally extendable 
with backward compatibility via soft forks. 
  - Implementation: _EasyFL_ part is mostly done. On the node: implemented simple version, which allows soft forks of the network. Maybe 50%

* Make ledger library upgradable with stateless computations for new cryptography and similar
  - Concept: explore some fast, deterministic, platform-agnostic VMs (e.g. RISC V, maybe even LLVM). Only vague ideas, some 10% in head
  - Implementation: 0%

* Delegation implementation
  * Concept: (a) enable token holders delegate capital to sequencer with possibility to revoke it. It would be a lock to the chain-constrained output.
    (b) implement sequencer part. In head 50%
  * Implementation: 0% <- high priority

* Tag-along lock implementation
  * Concept: modification of the _chain lock_, which conditionally bypass storage deposit constraints. In head: 80%
  * Implementation: 0%

* Practically reasonable storage deposit constrains and constants: 
  * Concept: needs simulation
  * Implementation: rudimentary version, maybe 10%

## Sequencer

* More advanced sequencer strategies with multiple endorsements
  * Concept: multi-endorsement strategies would contribute to the consensus convergence speed. In head 40%
  * Implementation: 60%. Currently, 4 different proposer strategies are used 
## Docs
- Whitepaper 90%
- How to run small testnet with outopeering: 80% 
- Introductory video series: 0%

## Priorities
1. Tooling
2. Delegation
3. Storage deposit
4. Spam prevention
5. Tag-along lock

