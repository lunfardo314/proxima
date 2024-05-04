Package `memdag` implements the `MemDAG`, a a data structure, containing part of the UTXO tangle being constructed and validated.

Transactions first appear as vertices of _MemDAG_, only after validation they are persisted to the `multistate` database.

_Proxima_ node do not have (and do not need) a mempool like the one used in the blockchain architectures with block proposers. 
_MemDAG _ is effectively plays the role of the buffer before including transaction into the ledger state.

_MemDAG_ is constantly cleaning (orphaning) invalid, obsolete or irrelevant transactions.