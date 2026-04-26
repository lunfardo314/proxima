# Trie prefix iteration — analysis and fix proposal

Date: 2026-04-25.

Context: `PrunableTxIDsAtSlot` dominates idle CPU (~40 % on boot per the
2026-04-24 pprof, see `claude/TODO.md:46-65`). The intent of trie
prefix iteration is **O(matching sub-trie size) with minimal I/O**;
the implementation should walk only the matching sub-trie and never
touch sibling sub-tries. This document confirms what the current
implementation does, identifies the inefficiencies, and proposes a
two-layer fix (unitrie + proxima).

## What the call looks like end-to-end

`PrunableTxIDsAtSlot(slot)` (`ledger/multistate/state.go:425`):

```go
keyPrefix := append([]byte{TriePartitionLedgerState}, base.Slot2Bytes(slot)...) // 5 bytes
r.trie.Iterator(keyPrefix).Iterate(func(k, v []byte) bool { ... })
```

Trie configuration: `PathArity16` (`ledger/commitment.go:17`). A 5-byte
prefix is unpacked to **10 nibbles**.

unitrie path (`immutable/kvstore.go:280-296`):

```go
func (tr *TrieReader) iteratePrefix(f, prefix, extractValue) {
    var root common.VCommitment
    var triePath []byte
    unpackedPrefix := common.UnpackBytes(prefix, tr.Model().PathArity())
    tr.traverseImmutablePath(unpackedPrefix, func(n, trieKey, ending) {
        root = n.Commitment   // last visited node wins
        triePath = trieKey
    })
    tr.iterate(root, triePath, func(k, v) bool {
        if bytes.HasPrefix(k, prefix) {
            return f(k, v)
        }
        return true
    }, extractValue)
}
```

Two phases:
1. `traverseImmutablePath` walks down the prefix path, fetching one
   node per descend step (~10 fetches for the 5-byte slot prefix).
2. `iterate(root, triePath, …)` recursively walks every node in the
   sub-trie rooted at `root`, fetching each via
   `nodeStore.FetchNodeData`.
3. A `bytes.HasPrefix(k, prefix)` filter on emitted keys is the
   safety net.

`traverseImmutablePath` ending codes
(`immutable/traverse.go:39-77`):

| Ending | Meaning | What `iteratePrefix` should do |
|---|---|---|
| `EndingTerminal` | full prefix path exists, ends at this node | iterate sub-trie under `root` (correct) |
| `EndingExtend` | next required child doesn't exist | nothing matches → return immediately |
| `EndingSplit` case 1 (`len(triePath) < len(keyPlusPathFragment)`) | prefix runs out mid-pathFragment | iterate iff partial prefix matches partial pathFragment |
| `EndingSplit` case 2 (lengths equal, bytes differ) | node's terminal key ≠ prefix | nothing matches → return |

## What the implementation actually does

**Correct** for `EndingTerminal` and the matching variant of
`EndingSplit` case 1: `root` is the right sub-trie root; `iterate`
walks only that sub-trie. **Sibling sub-tries are never touched.** The
hypothesis that the iterator should walk only the matching sub-trie
is satisfied for the happy path.

**Wasteful** for the other cases:

- `EndingExtend`: `traverseImmutablePath` returns with `n` set to the
  *parent* and `trieKey` set to where the missing child would have
  been. `iteratePrefix` blindly iterates from this parent's
  commitment. Generated keys differ from the prefix; the
  `bytes.HasPrefix` filter rejects every emission. The whole
  parent's sub-tree is fetched from BadgerDB for nothing.
- `EndingSplit` case 2: same shape — single node's sub-tree iterated
  uselessly.
- `EndingSplit` case 1: `traverseImmutablePath` does **not** check
  whether the partial prefix matches the partial pathFragment. If it
  doesn't match, the sub-tree iteration is again wasted.

These are correctness-preserving (the filter masks them) but
needlessly expensive when the prefix isn't fully present in the trie.

## The bigger performance problem: cold caches per call

Even when the iteration is correctly scoped to the slot's sub-trie
(the common case for `PrunableTxIDsAtSlot`), it is slow because every
`FetchNodeData` is a cold BadgerDB read.

Why:

**A. Per-call fresh node-store cache.**
`branches.go:387` (in `_commitPendingBranchUnlocked`):
```go
baselineReader := multistate.MustNewReadable(b.StateStore(), baselineRoot, 0)
```
The `0` argument means `clearCacheAtSize=0`, which **disables the
trie node cache entirely** (`nodestore.go:53-56`):
```go
if ret.clearCacheAtSize > 0 {
    ret.cache = make(map[string]*common.NodeData)
}
```
Every `FetchNodeData` is a BadgerDB `Get`.

**B. The `PrunableTxIDsAtSlot` path goes via a fresh `Updatable`,
not through `b.GetStateReaderForTheBranch`.**
`branches.go:379-404`:
```go
upd := multistate.MustNewUpdatable(b.StateStore(), baselineRoot)
…
gcTxIDs := upd.Readable().PrunableTxIDsAtSlot(gcSlot)
```
`NewUpdatable → immutable.NewTrieUpdatable(…)` is called without
`clearCacheAtSize`, so it defaults to `defaultClearCacheEveryGets =
1000`. Cache is *enabled* but starts cold and is wiped on every
threshold hit.

**C. The cache is flush-on-overflow, not LRU.**
`nodestore.go:67-70`:
```go
if len(ns.cache) > ns.clearCacheAtSize {
    clear(ns.cache)
}
```
For a sub-trie with > 1000 nodes, the cache repeatedly fills, gets
flushed entirely, refills. Within a single iteration cache hit-rate
approaches 0 % anyway (DFS visits each node exactly once), so the
flush-vs-LRU difference doesn't hurt one iteration — but it kills
locality across consecutive queries on the same Readable.

**D. Each `Updatable`/`Readable` is short-lived.**
`_commitPendingBranchUnlocked` creates a fresh `Updatable` per
commit. Its node cache starts empty. Whatever cache built up during
one commit is thrown away.

For a slot with ~1000 txID records, the sub-trie has ~1500–2500
nodes (16-ary trie, mixed compressed-path + branch nodes). At ~1
commit/sec under load, that's 1500–2500 BadgerDB `Get`s every
second, every commit — matching the ~40 % idle CPU pprof finding.

## Proposed fix — two layers

### Layer 1: unitrie

**1.a. Skip iteration when the prefix is provably absent.**

Capture the ending code from `traverseImmutablePath` and short-circuit
the spurious cases. Sketch:

```go
func (tr *TrieReader) iteratePrefix(f, prefix, extractValue) {
    var root common.VCommitment
    var triePath []byte
    var node *common.NodeData
    var ending common.PathEndingCode
    unpackedPrefix := common.UnpackBytes(prefix, tr.Model().PathArity())
    tr.traverseImmutablePath(unpackedPrefix, func(n, trieKey, e) {
        root = n.Commitment
        triePath = trieKey
        node = n
        ending = e
    })
    switch ending {
    case common.EndingExtend:
        return // child doesn't exist → no matching keys
    case common.EndingSplit:
        if len(unpackedPrefix) >= len(triePath)+len(node.PathFragment) {
            return // case 2: lengths equal, bytes differ → no match
        }
        // case 1: prefix runs out mid-pathFragment; check it matches the partial
        partial := node.PathFragment[:len(unpackedPrefix)-len(triePath)]
        if !bytes.Equal(unpackedPrefix[len(triePath):], partial) {
            return
        }
        // matches → fall through and iterate
    }
    tr.iterate(root, triePath, func(k, v) bool {
        if bytes.HasPrefix(k, prefix) { return f(k, v) }
        return true
    }, extractValue)
}
```

Effect: zero wasted I/O for prefixes not present in the trie. The
`bytes.HasPrefix` filter stays as cheap insurance against any edge
case.

**1.b. Replace flush-on-overflow with bounded LRU in the node store.**

Current `clear(ns.cache)` on threshold (`nodestore.go:67-70`) drops
hot entries along with cold ones. Replace with a fixed-capacity LRU
(~1000 entries):

- O(1) get/put via hashmap + doubly-linked list (`container/list`).
- On miss + insert: evict the back of the list when over capacity.
- On hit: move entry to the front.

Hot nodes (top-of-trie, branch points near the LRB) stay cached
across queries. Cold nodes age out one at a time instead of as a
group. Helps any workload that re-queries the same root, including
the slot-prune case if/when we reuse the same Readable across
commits.

### Layer 2: proxima

**2.a. Reuse the cached state reader for `PrunableTxIDsAtSlot`.**

Currently `_commitPendingBranchUnlocked` (`branches.go:377-409`)
creates a fresh `Updatable` and reads via `upd.Readable()`. The
`Updatable` is necessary for the commit (`upd.Update(muts, ...)`),
but the read-only `PrunableTxIDsAtSlot` step can go through the
already-cached `b.GetStateReaderForTheBranch(baselineID)`, whose
underlying `*Readable` has `clearCacheAtSize=stateReaderCacheLimit=3000`
and accumulates a warm cache across the readers' TTL window
(2 slots, hard cap 100 readers).

Effect: subsequent commits with the same or related baselines find
many sub-trie nodes already cached. Combined with Layer 1.b, hit
rate climbs significantly.

**2.b. Run the prune scan once per N slots (already in TODO).**

Documented at `claude/TODO.md:46-55`. A coordinated upgrade — every
node must use the same scan cadence so branch roots stay identical.
Orthogonal to the iteration speedup but compounding: fewer
iterations × faster iterations.

**2.c. (Optional, longer term) Maintain a per-slot index of new
txID records.**

When `Mutations.Apply` adds a txID record, also append `(slot, txid)`
to a small auxiliary structure persisted in a separate BadgerDB
partition. `PrunableTxIDsAtSlot(slot)` then reads via a contiguous
BadgerDB range scan instead of walking the trie sub-tree.

Trade-off: extra writes per commit; benefit: prune iteration becomes
O(records-in-slot) BadgerDB reads against contiguous keys with high
block-cache locality. The auxiliary index is **not** part of the
merkle commitment, so there's no consensus / determinism concern at
the hash level — but each node must maintain its own copy
deterministically.

## Recommendation order

In order of impact / effort:

1. **2.a first.** Single-line-ish change in
   `_commitPendingBranchUnlocked`: route `PrunableTxIDsAtSlot`
   through `b.GetStateReaderForTheBranch(baseline)` rather than the
   fresh `Updatable`. Biggest immediate win on the live testnet —
   reuses warm cache.
2. **1.a.** unitrie correctness fix. Small, eliminates the
   `EndingExtend` and `EndingSplit` waste cases. Safe — the existing
   `bytes.HasPrefix` filter remains as belt-and-braces.
3. **1.b.** LRU in `NodeStore`. Moderate-size unitrie change. Helps
   generally — any iteration over a trie of size > `clearCacheAtSize`
   becomes more cache-friendly across calls.
4. **2.b.** Coordinated upgrade to scan-and-prune cadence. Network
   change, not a code-only one — schedule with a testnet reset.
5. **2.c.** Auxiliary slot index. Biggest refactor, biggest payoff
   if 1+2 aren't sufficient.

## Verification plan

For each fix that ships:

- **Bench in unitrie.** Before/after: `go test -bench` on a synthetic
  trie with N=10k, 100k records, prefix iteration over a slot-sized
  sub-trie. Expect 1.a to remove a measurable spike when iterating
  absent prefixes; 1.b to flatten cache-hit ratio across repeated
  iterations on the same root.
- **Production signal.** `proxima_glb_attachmentDurationMs` and the
  branch-commit timing. The pprof attribution on `unitrie.NodeStore`
  / `badger.(*Txn).Get` should drop substantially. Compare 30 min
  windows under similar TPS.
- **Determinism.** 1.a is a pure performance fix, no observable
  behaviour change. 1.b is also semantically transparent (LRU still
  serves the same NodeData). 2.a needs to verify that the cached
  state reader returned by `GetStateReaderForTheBranch` for a
  pending-baseline behaves identically to a freshly-built one
  (corner case: pending branch with no committed root yet — currently
  handled by the `getCachedStateReader` early-return path).
