Package `memdag` implements the `MemDAG`, a a data structure, containing part of the UTXO tangle being constructed and validated.

Transactions first appear as vertices of _MemDAG_, only after validation they are persisted to the `multistate` database.

_Proxima_ node do not have (adn do not need) a mempool used in the blockchain architectures with block proposers. 