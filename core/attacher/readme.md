The package `attacher` contains core functions dedicated to the construction of the _UTXO tangle_.

Each transaction if first parsed and then is **attached** to the UTXO tangle. 
The _attachment_ process means _solidification_ of the past cone of the transactions and checking validity of it:

* _Solidification_: making sure all transactions in the past cone are valid
* _Determining the baseline of the sequencer transaction_: solidification of the whole transaction path which leads 
to the baseline branch 
* Transaction is _solid_ when it is _rooted_ (with terminal outputs belonging to the ledger state):
  * in the baseline branch for sequencer transactions
  * in any branch for non-sequencer transactions
* Each transaction is _validated_ by running all validation scripts for all outputs (consumed/inputs and produced ones) upon solidification
* Each sequencer transaction is checked for absence of conflicts (double_spends) in the past cone in the context of the baseline state.
* Each non-sequencer transaction is checked for absence of conflict in the past cone when tagged-along a particular sequencer transaction
* Sequencer transaction is _GOOD_ when it is _solid_, _validated_ and does not contain conflicts in the past cone, otherwise it is marked _BAD_
* Solid and validated non-sequencer transaction is always _UNDEFINED_, otherwise it is _BAD_

The construction of the UTXO tangle is highly parallel. Each sequencer transaction is attached by separate goroutine: the **attacher**.

The _milestone attacher_ handles all main tasks of the sequencer transaction: pulls missing inputs from other nodes, determines baseline, validates the output scripts.

The _milestone attacher_ finishes the task and leaves go routine by:
* marking sequencer transaction as _GOOD_
* marking sequencer transaction as _BAD_
* committing the ledger state into the DB for _GOOD_ branch transactions
* leaving with _BAD_ in case of solidification timeout or global shutdown

The _incremental attacher_ is a utility for the sequencer. It allows construction of the past cone by 
incrementally adding consumed and endorsed inputs and controlling consistency of the past cone.  

