# memDAG past-cone growth ↔ coverage determinism (root model + fix tradeoffs)

Status 2026-06-15: WORKING MODEL, INCOMPLETE (after two wrong fixes — do not treat
as final). No fix chosen. The CURRENT shipped code has the ever-growing memDAG —
that is the live symptom to keep in view, not a hypothetical. Leak is slow
(~17 vertices/min) and reset by restart, so it is gradual, not acute.

## Purity-breakers (why "real DAG determinism" ≠ what the node actually runs)

The protocol is deterministic over the *real, complete* DAG. The node never has
that — it has a partial, mutating view. At least THREE things break the purity,
and edge cases live in their interactions:

1. **memDAG GC/pruning under async gossip+pull.** Different nodes hold different
   cache fragments at different times; pruning by age (below) removes parts the
   deterministic walk may still need. (Main thread of this doc.)
2. **Snapshots — a STATIC floor.** A node starts from a snapshot at slot S, yet can
   still receive (gossip/pull) transactions with timestamp < S. This does NOT break
   real-DAG determinism, but it is a rich edge-case source: such a pre-snapshot tx
   must be treated as rooted-via-snapshot (its effects are baked into the snapshot
   UTXO set / its txid may have been TTL-pruned from the trie), and the rooting
   determination (`SnapshotKnowsTransaction` / `TransactionIsInSnapshotState` /
   `txidMayHaveExpiredFromSnapshot`) has corner cases. NOTE: the observed leak's
   `oldestSlot` freezes at/near the committed-state FLOOR (≈ the snapshot/restore
   slot) — strongly hinting the walk gets STUCK at the snapshot boundary (vertices
   at the floor that the walk cannot cleanly root via the snapshot, so it neither
   terminates nor reclaims them). This snapshot-boundary interaction may be the
   actual driver, distinct from generic TTL pruning — to be confirmed empirically.
3. **The static-vs-dynamic seam itself.** The snapshot is immutable committed state;
   the memDAG above it is dynamic/pruned. The seam between them is where rooting,
   eviction, and async arrival all meet — the likeliest home of the real bug.

Do not over-conclude from any single one of these in isolation.

## The tension (the actual root)

Coverage is **deterministic and well-defined over the real DAG + the specific
transaction's chosen baseline**. The attachment walk must descend the not-rooted
past cone until it reaches the baseline (rooted) state — that termination at the
baseline is what makes the computed coverage deterministic.

Two facts make this conflict with the implementation:

1. **Rooted-ness is sequencer-subjective.** A sequencer may pick a sub-DAG whose
   not-rooted part (relative to that tx's baseline) is long. There is no single
   global "this vertex is rooted / safe-to-drop" age — one tx's deep-not-rooted
   vertex is another tx's ancient history.
2. **The memDAG is a TTL-pruned async CACHE of the real DAG, not the DAG itself.**
   It evicts by wall-clock + ledger time.

So: the deterministic computation needs a (possibly deep) not-rooted fragment,
but the cache evicts that fragment by age — cutting exactly what determinism
depends on. The walk then can't terminate at the recent rooted frontier and
reaches back toward the committed-state floor → the cone grows (the "leak"); and
if it computes over an incomplete/stale fragment, coverage deteriorates.

## Code grounding (verified)

- **Deterministic depth budget REMOVED.** `attachVertexUnwrapped` still *documents*
  a deterministic recursion-depth limit ("upon reaching constant limit … returns
  failed … recursions depth … deterministic") but the code has no depth param /
  increment / check. Replaced by the attachment **cost** budget
  (`AttachmentCostBudget`=550, `checkAttachmentCostBudget`). Cost ≠ depth: a deep
  thin not-rooted chain (1 in / 1 out per tx) has low cost but high depth, so depth
  is now effectively unbounded.
- **GC criterion 1 (TTL) prunes regardless of rooted-ness** (`memdag.go` doGC):
  `wallClockExpired` (slot−SlotWhenAdded > `vertexTTLSlots`=24) and
  `ledgerTimeExpired` (txid.Slot()+`vertexLedgerTTLSlots` < latestBranch) both append
  to prune candidates with **no check** that the vertex is still needed by a live
  not-rooted cone. Criterion 2 (`confirmed_deep`) is the rooted path. So TTL cuts
  not-rooted vertices purely by age.
- **The cone's ~600 vertices are flagged `InTheState`** (the trim that removed
  `InTheState && slot<baselineSlot` collapsed the cone to ~2). But those flags are
  set relative to *merge-source* baselines and are NOT reliably rooted-relative-to-
  THIS-tx's-baseline — which is why trimming them broke coverage. Rooted-ness is
  baseline-relative; the merged flag is not a safe drop signal.

## Dead ends (do not retry)

- **Branches store nil past cone** (`856927cc`, reverted): inert — branch stored
  cones are never read by the walk.
- **Trim `InTheState` ancestors below baseline from `CloneImmutable`** (stash@{1}):
  deployed to loc0-acc, ran ~7 min, FATAL `coverage should not decrease along
  endorsement` (computed LC≈909T vs endorsed 1788T). Proves the cone's contents
  feed coverage; you cannot remove vertices from it.
- **reattachCounter tolerate-and-retry** (stash@{0}): inert — the multi-seq test
  flake has 0 reattachments; it is a separate TEST-ONLY startup determinism issue,
  not this leak and not a reattach race.

## Fix directions (the fork — needs a decision)

- **(A) Reachability-gated eviction.** Don't TTL-evict a not-rooted vertex while a
  live tip still reaches it; evict only rooted/confirmed-deep + genuinely-orphaned.
  Keeps the cache faithful to the not-rooted fragment. RISK: "reachable from a live
  tip" = the forward/`consumed` reference graph, whose unbounded retention was the
  ORIGINAL memory leak (`4bfd7041`). Must bound abandoned-not-rooted growth.
- **(B) Reinstate the deterministic depth/ledger-span budget**, aligned with the
  cache TTL window, so a tx's not-rooted cone can't exceed what the cache reliably
  holds. Bounds BOTH the leak and the determinism gap with one rule, and RESTORES a
  prior invariant. RISK: it's a validity/consensus rule (likely hardfork); and a
  naive ledger-span cap would WEDGE post-restart catch-up (where the not-rooted span
  vs a fresh floor is legitimately large until forward-sync commits branches) — so
  it must compose with forward-sync advancing the rooted frontier.
- **(C) Faithful refetch from txstore on cache-miss.** Treat the txstore as the real
  backing store and reconstruct any evicted not-rooted vertex on demand rather than
  computing over an incomplete cone. RISK: deep refetch is exactly what the recursion
  bound (`d4974cfc`, pull.go depth cap) limits to prevent the lagging-node wedge —
  so refetch-for-determinism vs bound-to-avoid-wedge are in direct tension.

## Recommendation

This is a genuine design decision (cache policy vs protocol bound vs refetch), each
with a real failure mode already seen this effort. Do NOT build it unattended /
"subtly." Pick a direction first; then validate on loc0-acc (production-clean, and
an access node so it can't push bad txs). The leak's slowness + restart mitigation
buys time to get it right.
