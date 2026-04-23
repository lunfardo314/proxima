# TODO — backlog

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
