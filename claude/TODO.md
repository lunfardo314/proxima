# TODO — backlog

This file contains TODO list for future Claude sessions.

## Attachment time regression

- **Attachment time is long and volatile.** Grafana "Attachment time
  milliseconds" on 2026-04-22/23 shows values in the 500–3000 ms range with
  high variance; in an idle network this metric should be ~1 ms. Something on
  `develop07-peering` has introduced a significant regression on the attacher
  hot path. The effect is visible even before the halt (post-halt the metric
  plateaus because nothing new attaches, but the pre-halt variance is already
  far above normal). Bisect against recent `develop07-peering` commits to find
  which change increased attacher wall-clock; likely candidates are the
  peering refactor steps, LRB-depth pruning tweaks, and the sibling-commit
  path on access nodes. Screenshot: `attachment_time.png`.

## Snapshot protocol

- **Don't rush snapshotting right after start.** Wait at least ~30 slots after the
  node is synced before writing a snapshot. A snapshot taken before the node's own
  chain state has settled can lock the node onto a short-lived branch that peers
  never confirm.
- **Snapshot selection rules (at restore time):**
  1. Reject snapshots younger than ~60 slots relative to wall-clock — too recent
     to be safely common across the network.
  2. When a state DB already exists locally, the chosen snapshot must be
     compatible with it (sits on the same mainchain); otherwise prefer an older
     snapshot that is.
  Context: 2026-04-23 testnet reset — each node had snapshots from different
  slots, none common. After a DB wipe, every node restored from its own latest
  snapshot and formed a divergent mainchain. Forward-sync rejected peers'
  branches as "not in main chain (possible fork)". Resolved manually by copying
  boot's snapshot to the other three nodes.

## Tools

- APIs exposed by `proxi db txstore dagviz` should be exposed by the node
- limit number of dagviz connection (it is already the case). Add clear message for the user if that is the case
- Default of the dagviz connection time let be 20 min
- remove `proxi multispam` from `proxima' repo` and move to separate private repo
- introduce metrics in sequencer: average miliseconds (oper slot) between submit milestone and appearing it in the tippool  

# Upcoming ledger refactor

## Scan-and-prune txID records periodically, not every commit

Current behaviour is correct but slow: every branch commit runs
`PrunableTxIDsAtSlot(gcSlot)` inside `_commitPendingBranchUnlocked`, a trie
sub-iteration that dominated idle CPU in the 2026-04-24 pprof (~40 % on boot)
and landed on the attacher wall-clock. Don't remove the scan — change its
cadence so it runs once per **N slots** (proposed N = 30) rather than per
commit, pruning the window covered since the last scan. The schedule must be
deterministic (same slots on every node) so branch roots stay identical across
the network. Breaking change — requires coordinated upgrade.

## Revisit trie sub-trie iteration performance

`PrunableTxIDsAtSlot` uses `trie.Iterator(keyPrefix).Iterate(...)` with a
5-byte prefix (partition + 4-byte slot). A prefix iteration should be
O(subtree size) with minimal I/O, but pprof shows most cost in `unitrie`
`NodeStore.FetchNodeData` → BadgerDB reads. Independent of the scan cadence,
the iterator path in `unitrie` deserves an optimisation pass — confirm it
walks only the matching subtree, uses the node cache effectively, and
doesn't touch sibling subtries.

## Keep TriePartitionLedgerState shared between TX and UTXO records

Splitting txID records into a separate trie partition would duplicate the
shared 32-byte prefix (txid = first 32 bytes of OutputID) across two subtries
and undo the current reuse optimisation. Don't do this as a workaround for
the iteration cost — fix the iterator path instead.