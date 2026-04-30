# TODO — backlog

This file contains TODO list for future Claude sessions.

## Snapshot protocol

- **Don't rush snapshotting right after start.** Wait at least ~30 slots after the
  node is synced before writing a snapshot. A snapshot taken before the node's own
  chain state has settled can lock the node onto a short-lived branch that peers
  never confirm.
- **Snapshot selection rules (at restore time):**
  1. Reject snapshots younger than ~60 slots relative to wall-clock — too recent
     to be safely common across the network.
  2. ..

## Sync
Revisit weird behavior with syncing after warm restart. 
- forward syncing may be interfering when there's no need of it

## Tools

- limit number of dagviz connection (it is already the case). Add clear message for the user if that is the case
- Default of the dagviz connection time let be 20 min

# Upcoming ledger refactor

## Needed for bridging
(needs refinement)
- include coverage delta, supply, baseline root into the stem. Enforce at the node level. Remove it from the metadata. Probably remove the persistent metadata as such
- remove persistent TxMetadata as concept
- refactor locks and indexing in the ledger, replace with tuple of indices pos 1 + the lock constraint pos 2 + chain pos 3. Remove lock serialization   
- Expose Merkle proof in the Readable
- Remove plain data list element at the tx tuple level
- I implement evidenceHash(hashPrefix, data) enforcer hasPrefix(hash(data), hashPrefix). Use it in the enforced script list at the txLevel.
- Implement validateWithRedeemed(index of evidenceHash() bytecode, redeemed lib hash prefix, lib tuple index called function, args …). It will compare hashes and call library. The idea is not to run hash function for each revocation
- Library compilation caching
- Inclusion proof validation embedded opcode
- Implement open lock as plain index data list value. The index will be the evaluated data. Unlockable by anybody. Consider randomization of the unlock slot, e.g. by hash(public key||UTXO ID||slot) mod 5 == 0
Another option. Interpret open lock data as tuple of index values

## Misc
- 24 byte VCommitment


