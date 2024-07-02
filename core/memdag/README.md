Package `memdag` implements the `MemDAG`, a data structure which represents in-memory part of the UTXO tangle 
for being constructed and validated.

Transactions first appear as vertices of _MemDAG_, only after validation they are persisted to the TxBytesStore.

_Proxima_ node does not have (and does not need) a mempool like the one used in the blockchain architectures with block proposers.
_MemDAG _ plays similar role of the buffer before storing transaction and committing the ledger state.

Invalid, obsolete or irrelevant transactions ar constantly cleaned in the background.