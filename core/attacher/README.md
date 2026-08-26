# attacher

The package `attacher` contains core functions dedicated to the construction of the _UTXO tangle_.

**[`claude/dag_semantics.md`](../../claude/dag_semantics.md) is the authority**
on everything below — vertex status, the past cone, the flag-monotonicity
contract and the pruning criterion. This file is an orientation to the package;
where the two differ, that one is right. For the attachment gates and the bounds
on how much work one transaction can cause — the cost budget, the depth cap,
pull patience — see [`../resilience.md`](../resilience.md).

Each transaction first is parsed and then is **attached** to the UTXO tangle. 
The _attachment_ process means _solidification_ of the past cone of the transactions and checking validity of it (as per transaction validity rules):

* _Solidification_: making sure all transactions in the past cone exists and are valid.
* _Determining the baseline of the sequencer transaction_: solidification of the whole transaction path which leads 
to the baseline branch 
* Transaction is _solid_ when it is _rooted_ (with terminal outputs belonging to the ledger state):
  * in the baseline branch for the sequencer transactions
  * in any branch for non-sequencer transactions
* Each transaction is _validated_ by running all validation scripts for all outputs (consumed/inputs and produced ones) upon solidification
* Each sequencer transaction is checked for absence of conflicts (double_spends) in the past cone in the context of the baseline state.
* Each non-sequencer transaction is checked for absence of conflicts in the past cone
* Sequencer transaction is _GOOD_ when it is _solid_, _validated_ and does not contain conflicts in the past cone, otherwise it is marked _BAD_
* Solid and validated non-sequencer transaction is always _UNDEFINED_, otherwise it is _BAD_ (never _GOOD_).

The construction of the UTXO tangle is highly parallel. Each sequencer transaction is attached by separate goroutine: the **attacher**.

The _milestone attacher_ handles all main tasks of the sequencer transaction: pulls missing inputs from other nodes, determines baseline, validates the output scripts.

The _milestone attacher_ finishes the task and leaves go routine by:
* marking sequencer transaction as _GOOD_
* marking sequencer transaction as _BAD_
* persisting the ledger state into the DB for _GOOD_ branch transactions
* leaving with _BAD_ in case of solidification timeout or global shutdown

The _incremental attacher_ is a utility for the sequencer. It allows construction of the past cone by 
incrementally adding consumed and endorsed inputs and controlling consistency of the past cone.

Attachment is bounded work, not best-effort: the attachment cost budget, the
recursive-pull depth cap and pull patience each terminate it, and a milestone
attacher also self-aborts once its own vertex ages past the memDAG TTL. After
any change here, run the relevant tests under `-race` — the lock-free past-cone
traversal relies on "Good ⇒ immutable", and a clean functional run is not
evidence that the assumption still holds.
