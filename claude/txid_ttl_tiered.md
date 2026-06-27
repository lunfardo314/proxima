# Tiered txid-state retention + decoupled sync horizon

Status: **SPEC, no code yet.** Breaking ledger change (hardfork). Backward
compatibility explicitly out of scope (user confirmed).

## Decision

Split the single transaction-ID state-retention TTL into two, prune by branch
flag, flip the "old txid ⇒ assume committed" rule to "old txid ⇒ not
committed", and move the sync / too-old-state horizon out of the ledger into
node config.

| Knob | Old | New |
|------|-----|-----|
| Non-branch txid retention | `TxIDStateTTLSlots` = 8640 (all txids) | **60 slots** |
| Branch txid retention | (same 8640) | **17480 slots** (= 8740 × 2) |
| Sync / too-old horizon | `TxIDStateTTLSlots/2` (ledger-derived) + `maxSyncSlotsBehind`=8740 const | **node config, default = branch TTL / 2 = 8740**, decoupled from ledger |

## Why the split is the right shape

A txid record is pruned **only when the transaction has zero surviving unspent
outputs** (`PrunableTxIDsAtSlot` selects empty-set txids; `updateTxUnspentSet`
deletes inline only on an empty set past the GC slot). So a txid with any live
output always reads as committed regardless of TTL. The TTL only ever governs
**fully-consumed** transactions.

Two disjoint populations need to remember fully-consumed txids, with very
different horizons:

- **Non-branch txids** are needed only to detect a fully-consumed-in-delta
  ancestor *while a descendant is still being solidified*. That window is the
  pull / solidification lag — tens of slots, not a day. This population is the
  storage burden: at 100 TPS, ~9M records/day, ~99.9% non-branch.
- **Branch txids** are needed by LRB detection (`FindLatestReliableBranch`,
  `roots.go:533`), baseline-branch resolution (`attach.go:104`), and the
  snapshot/sync boundary. Their horizon = the deepest fork / partition across
  which a common committed ancestor must still be identifiable. Branches are
  rare (~1 per sequencer per slot), so keeping them long is cheap.

Because the long-memory need and the bulk-storage burden are **disjoint sets of
transactions**, one TTL cannot serve both well. Tiering does.

### Flip safety (non-branch)

A fully-consumed non-branch tx V is reachable during attachment only via a
consumer C that spends one of V's outputs. If C spends V's output *now*, V's
unspent set was not empty until now, so V's record is still present (pruning
requires an empty set aged past the horizon). Hence whenever a descendant can
actually reach V, `BranchKnowsTransaction(V)` is already true. The
"pruned-but-reached" case does not arise in forward attachment; 60 slots is
margin for late consumption and the solidification window. Therefore the
"trust-by-age" rule for non-branch txids is dead weight and is removed: old +
not-in-state ⇒ provisionally not-in-state (solidify), which is the correct
answer (txids are collision-free and never re-occur, so a wrongly-forgotten
commit cannot be re-solidified and cannot corrupt the state).

### Branch trust rule becomes unnecessary within retention

The snapshot-boundary branch trust rule (`attach.go:108-120`,
`txidMayHaveExpiredFromSnapshot`) exists *only* because branches get pruned at
8640. With branch retention 17480 and the sync horizon capped at 8740 (half),
every branch the sync path can legitimately reference still has its record →
`KnowsCommittedTransaction` returns true → no trust-by-age needed. Beyond
retention the node refuses and resyncs from a younger snapshot (the existing
`else` → BAD branch path at `attach.go:116-119`). No genesis cascade: a pruned
branch is marked not-in-state, pull fails (no raw tx), the dependent times out —
bounded.

## Fork healing

Rooting is decided against a specific baseline's UTXO set. A tx committed only
on a losing fork has no outputs in the winning baseline's state, so it is simply
not rooted there — orphaned. The TTL plays no part. A fork persisting beyond the
branch TTL still heals: each branch prunes records in its own per-branch state;
the winning baseline never held the loser's records. The flip removes the
latent unsoundness where an old losing-fork tx was declared in-state on the
winning baseline purely for being old.

## Storage impact

- Non-branch: 60 slots vs 8640 → ~144× reduction → ~60K records (was ~9M).
- Branch: 17480 slots × branches-per-slot. With 100 sequencers ≈ **1.75M**
  (cap ~1.8M). Now branch-dominated. Two records each (trie txid +
  `RootRecord`), both bounded at this horizon — vs today's `RootRecord`s, which
  grow forever.
- Total live txid+root records ≈ ~1.8M vs ~9M (and `RootRecord`s now bounded
  rather than unbounded).

## Code sites

### 1. Ledger constants (hardfork)

- `ledger/def/def_constants0.json` — change `constTxIDStateTTLSlots` source to
  the new non-branch value; **add** `constBranchTxIDStateTTLSlots`
  (`u64/{{.BranchTxIDStateTTLSlots}}`).
- `ledger/def_constants0.go` — `defaultTxIDStateTTLSlots = 60`; add
  `defaultBranchTxIDStateTTLSlots = 17480`; add field to `InitParameters`,
  `constantsTemplateData`, and `ConstantsJSONFromParamsUpgrade0`/
  `DefaultParameters`.
- `ledger/constants.go` — parse `constBranchTxIDStateTTLSlots` (mirror
  lines 103-106); add to the `Add(...)` dump at line 226.
- `ledger/txbuildercore/constants.go` — add `BranchTxIDStateTTLSlots uint32`
  to the `Constants` struct (77), the raw JSON struct (119), and both
  copy directions (154, 204).

### 2. Tiered pruning

- `core/core_modules/branches/branches.go`
  - struct field (69): add `BranchTxIDTTLSlots uint32` beside `TxIDTTLSlots`.
  - GC driver (352-364): compute two GC slots —
    `gcSlotNonBranch = branchID.Slot() - TxIDTTLSlots` and
    `gcSlotBranch = branchID.Slot() - BranchTxIDTTLSlots`; scan each horizon
    slot for its kind; delete both sets; set both inline-GC slots on `muts`.
- `ledger/multistate/state.go` — `PrunableTxIDsAtSlot` becomes kind-aware:
  either two methods (`PrunableNonBranchTxIDsAtSlot` /
  `PrunableBranchTxIDsAtSlot`) or a flag param. Filter the scanned txids by
  `txid.IsBranchTransaction()`.
- `ledger/multistate/mutate.go` — `updateTxUnspentSet` inline GC: pick the GC
  slot by `txid.IsBranchTransaction()`. `Mutations.GCSlot` becomes two values
  (`GCSlotNonBranch`, `GCSlotBranch`); `DeleteTxIDs` unchanged.

### 2a. Branch record deletion (atomic with the trie prune)

A branch transaction has **two** DB representations: the txid record inside the
state trie (`TriePartitionLedgerState`) and a separate **`RootRecord`** in a flat
KV partition (`rootRecordDBPartition` = `PartitionOther`, byte 2), keyed by
branch txid (`roots.go:30-34`, `WriteRootRecord`). The `RootRecord` holds the
trie root commitment + sequencer ID and is what `IterateBranchChainBack` /
`FindBranchesFromLatestHealthySlot` / `FetchBranchData` iterate.

Today **`RootRecord`s are never deleted** — they accumulate forever (a second
unbounded growth, ~1 per branch). Pruning a branch's trie txid without its
`RootRecord` also splits the two horizons: `IterateBranchChainBack` walks
`RootRecord`s while the LRB check inside it (`roots.go:533`) reads the trie txid
record — leaving `RootRecord`s would let the iterator walk to branches whose
in-trie record is gone.

So: **when a branch txid is pruned, atomically delete its `RootRecord` in the
same batch.** This is natural — `WriteRootRecord` already runs in the single
`batch` of `updateUTXOLedgerDB` (`state.go:629-657`) alongside the trie commit.

- `ledger/multistate/roots.go` — add `DeleteRootRecord(w common.KVWriter,
  branchTxID)` (mirror of `WriteRootRecord`; `Delete` on
  `rootRecordDBPartition || branchTxID`).
- `ledger/multistate/state.go` `updateUTXOLedgerDB` — after the trie commit and
  `WriteRootRecord`, for each pruned **branch** txid call `DeleteRootRecord(batch,
  txid)` in the same batch. The pruned-branch-id list is the branch subset of
  the GC set, threaded down from the branch GC driver (via `Mutations` /
  `RootRecordParams`).
- **`earliestSlot` marker** (`earliestSlotDBPartition`): once old branches are
  deleted, advance it to the new earliest retained branch slot so state-range /
  snapshot-serve bounds don't claim branches that were pruned.

Out of scope (pre-existing, separate problem): trie **node** version GC. The
unitrie is persistent — deleting a key in a new root does not reclaim old node
versions, and `RootRecord` deletion does not either. Pruning keeps the *live*
state small (iteration, proofs, cached nodes, future copies); reclaiming
historical trie-node storage needs ref-counting / copying GC and is not
addressed here.

### 3. Remove trust-by-age (the flip)

- `core/attacher/attacher.go` — delete the `txidMayHaveExpired` branches in
  `defineInTheStateStatus` (466-470, 479-484) so the path falls through to
  `MarkVertexNotInTheState`; delete `txidMayHaveExpired` (493-501).
- `core/attacher/attach.go` — delete the TTL branch (108-120); keep
  record-present → Good, else → BAD.
- `core/core_modules/branches/branches.go` — simplify `SnapshotKnowsTransaction`
  (723-732) and `TransactionIsInSnapshotState` (763-772) to pure
  `BranchKnowsTransaction`; delete `txidMayHaveExpiredFromSnapshot` (734-749).

### 4. Decouple the sync / too-old horizon (node config, not ledger)

- `core/core_modules/forward_sync/sync.go` — replace the `maxSyncSlotsBehind`
  const (57) with a node-config value (e.g. `sync.max_slots_behind`), default
  `branchTTL/2` = 8740. Drop the comment tying it to the txid TTL.
- `core/core_modules/snapshot_restore/too_old_recovery.go` — re-base
  `snapshot_restore.max_state_age_slots` default from `TxIDStateTTLSlots/2` to
  the branch-retention-derived horizon; clamp strictly below **branch** TTL
  (forward-building needs branch baselines, not non-branch records).
- Bootstrap-from-known-snapshot stays an explicit config override (trust this
  snapshot regardless of age) — existing flags should suffice.

### 5. dag_semantics.md

Requires a section update (retention model is now tiered; the "Good ⇒
in-state by age" relaxation at the snapshot boundary is replaced by branch
retention + refuse-and-resync). **Do not edit `dag_semantics.md` without
explicit user approval** (it is a hard constraint doc, evolved only with
approval). Flag the exact section once the code lands.

## Best-effort degradation (acceptable)

`OutputIsConsumed` (`sugared.go:225`, explorer "is this output spent") can no
longer distinguish "consumed" from "never existed" for non-branch txs older
than 60 slots. API convenience, not consensus. Note in API docs.

## Future option (NOT in this spec — risky)

Cut the ~1.8M branch population with a second branch-pruning constant: delete
branch txids that have **no successor** after K slots (orphaned/dead-end fork
branches), keeping only main-lineage branches. Reduces branch count toward
`slots × main-lineage-width`. Risky: "no successor" depends on the forward set;
a branch pruned now that gains a late successor would be wrong, and it
interacts with fork-healing depth. Documented as a direction, not adopted.

## Test plan

- `go test ./ledger/...` for the constant + pruning changes.
- `go test -race ./core/...` (core change — race detector mandatory; the
  flip removes a path, the lock-free past-cone assumptions must still hold).
- Pruning unit test: a fully-consumed non-branch txid GC'd at +60, a branch
  txid retained to +17480; a non-branch txid with a live output never pruned.
- Branch-record atomicity test: after a branch is pruned, both its trie txid
  record AND its `RootRecord` are gone in the same committed batch;
  `IterateBranchChainBack` stops at the retained horizon; `earliestSlot` has
  advanced.
- Flip test: a descendant attaching across the 60-slot horizon still solidifies
  (record present because the consumed output kept the producer non-empty).
- Sync test: state between 60 and 8740 slots behind still builds forward (branch
  baselines resolvable); beyond 8740 refuses and resyncs.
