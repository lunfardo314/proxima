# Refactor transaction records in the trie

## Current
- UTXO is stored in the trie partition with the key of UTXO ID and with value the UTXO itself
- Transaction ID is stored in the trie with the key txID and value of data that is not used

Note that UTXO ID is txID+<output index>

Old transactions are deterministically pruned from the state. Apparently, sometimes it happens that some UTXOs remai in the state,
while transaction record is gone. That leads to some inconsistencies and edge case which is difficult to handle.

## Goal
Instead of existing model, introduce different model how existence of UTXOs and transaction are checked in the state and how
old transactions are pruned.

That will change `HasTransaction` and related low-level logic a bit.

## New model
DB-wise it only changes value of the transaction record in the trie: the one with the txID as a key. 

The transaction record in the state (trie partition) will be:
- key - txid
- value a serialized Set256 of indices of UTXOs of this transaction, that are not consumed in this state. When set of unspent UTXOs of the transaction
becomes empty, in order to prevent empty value, we put []byte{0}, that corresponds to and empty set anyway

In the mutations, consuming or producing of UTXOs means corresponding txid entries are always updated accordingly and atomically.

### Checking if transaction is known to the state
TxID key (32 bytes) must be in the state.
If the record is not in the state and its slot if later than pruning slot, then definitely not in the state
Otherwise transaction maybe were in the state before pruning slot, so we must handle this situation.

### Checking of presence of UTXO in the state
1. check if there's tx record with txid (first 32 bytes of UTXO ID). If no, UTXO is not in the state
2. if txid has record, if index of the UTXO (last byte) is in the set of unconsumed UTXOs, then UTXO is in the state, otherwise no. 

### Pruning
Like currently, all transaction records that are older than some threshold, may be deleted from the state.
We delete the tx record only if its set of unspent UTXOs is empty

### Caching
The L2 cache of the 'Readable' must store requested transaction records with their sets256 of unspent UTXOs for fast checking.  