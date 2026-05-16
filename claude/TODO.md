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

## Dust attack vector from arbitrary locks

After the UTXO indexing refactor (slot 2 = arbitrary EasyFL bytecode), any
EasyFL author can ship a lock that bypasses `selfRequireEnoughStorageDeposit` /
`selfEnforceZeroAmountsInNonChainedOutput`. That opens a dust spam vector:
cheap-to-create UTXOs accumulate indefinitely in the trie state.

The library locks (`sigLock`, `chainLock`, `tagAlong`, `delegateLock`,
`stemLock`, and the new `htlc`) all enforce these checks themselves, but we
cannot rely on the lock to police itself once arbitrary locks are admitted.

Action: enforce a minimum-storage-deposit and zero-non-chain-amounts rule on
every produced UTXO at the **Go level** (i.e. unconditionally in the
transaction validator), with a small exemption set (chained outputs already
allow non-zero inflation/frozen coverage; stem may need its own carve-out).
Likely lives next to `EnoughAmountForStorageDeposit` in `output.go` /
`txbuilder/`. Drop the per-lock `selfRequireEnoughStorageDeposit` calls once
the framework rule is authoritative.

# Upcoming ledger refactor

## Needed for bridging
(needs refinement)
- include coverage delta, supply, baseline root into the stem. Enforce at the node level. Remove it from the metadata. Probably remove the persistent metadata as such
- remove persistent TxMetadata as concept
- refactor locks and indexing in the ledger, replace with tuple of indices pos 1 + the lock constraint pos 2 + chain pos 3. Remove lock serialization   
- Expose Merkle proof in the Readable
- delegation constants per chained account rather than global — spec at [claude/delegation_epoch_params.md](delegation_epoch_params.md). DEFERRED. Current thinking: bumping the two global constants (`constDelegationEpochSlots`, `constDelegationMaxFrozenEpochs`) may be sufficient. If we do move them, possibly only `maxFrozenEpochs` needs to be per-target (epochSlots could stay global). Revisit when there's a concrete need.
- support native token constraints on the amounts vector
- Remove plain data list element at the tx tuple level
- I implement evidenceHash(hashPrefix, data) enforcer hasPrefix(hash(data), hashPrefix). Use it in the enforced script list at the txLevel.
- Implement validateWithRedeemed(index of evidenceHash() bytecode, redeemed lib hash prefix, lib tuple index called function, args …). It will compare hashes and call library. The idea is not to run hash function for each revocation
- Library compilation caching
- Inclusion proof validation embedded opcode
- Implement open lock as plain index data list value. The index will be the evaluated data. Unlockable by anybody. Consider randomization of the unlock slot, e.g. by hash(public key||UTXO ID||slot) mod 5 == 0
Another option. Interpret open lock data as tuple of index values

## Audit conditional locks: delegate to `sigLock` where fallback is sigLock-equivalent

When a lock's conditional fallback path is meant to behave "like an ordinary
sigLock for the issuer" (e.g. timeout reclaim, master-reclaim, etc.), the body
should invoke `sigLock` (the public 0-arg constraint) — or `_sigLock($holder)`
— rather than hand-rolling a `txHolderID == issuer` comparison. Calling the
real thing picks up unlock-by-reference for free, keeps semantics in lockstep
with sigLock as it evolves, and shrinks the lock body.

Sweep candidates (read each, check fallback path):
- `lock_tag_along.easyfl` — reclaim already uses `_sigLock($1)` ✓
- `lock_send_with_deadline.easyfl` — master reclaim already uses `_sigLock($1)` ✓
- `lock_delegate.easyfl` — open-window master path, on-hold paths, frozen paths
- `timelock.easyfl` (htlc) — after-cutoff path
- `lock_chain.easyfl`
- Anywhere else with explicit holder-ID equality checks driving an unlock

Reference: how it ended up in `examples/dex/dex.easyfl` — sell/buy order reclaim
windows just call `sigLock`. Bundle shrank ~110 bytes vs. the hand-rolled
version.

