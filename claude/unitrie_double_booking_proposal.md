# Proposal: Refactor `NewMutationsMustNoDoubleBooking` in unitrie

## Problem

`trie.Commit(batch)` panics with `"repetitive SET mutation"` when two different trie keys have identical
large values (>62 bytes, i.e. not embedded in the terminal commitment).

### How it happens

During `TrieUpdatable.Commit()`, the method `commitNode` recursively walks all modified buffered nodes.
For each node with a value too large to embed in the terminal commitment, it writes:

```go
// node.go:58
valuePartition.Set(common.AsKey(n.terminal), n.value)
```

The terminal commitment is a content hash of the value (blake2b with first byte stripped).
Two different trie keys with identical values produce the **same** terminal commitment,
hence the same value store key. The second write triggers the panic in `Mutations.Set()` at
`mutations.go:37`.

### Why identical large values are legitimate

The trie is used as a UTXO ledger state. Two UTXOs can have identical binary representations
(same amount, same lock, same constraints) while being stored at different trie keys (different OutputIDs).
This is normal and valid — there is nothing wrong with sending the same amount to the same address
in two different transactions.

The value partition is **content-addressed**: the key IS the hash of the value. Writing the same
key with the same value twice is idempotent — it's not a logic error, it's deduplication.

### The crash

```
panic: repetitive SET mutation. The key '<32 bytes>' was already set

goroutine ... [running]:
  common/mutations.go:37          Mutations.Set
  badger_adaptor/badgeradaptor.go  badgerAdaptorBatch.Set
  common/partition.go:114          WriterPartition.Set
  immutable/node.go:58             bufferedNode.commitNode  (valuePartition.Set line)
  ...recursive commitNode calls...
  immutable/trie.go:96             TrieUpdatable.Commit
```

## Analysis

The `NewMutationsMustNoDoubleBooking()` batch writer panics on ANY duplicate `Set` call,
regardless of partition or whether the values match.

There are two partitions written during `Commit()`:

| Partition | Prefix byte | Key semantics | Duplicate possible? |
|-----------|-------------|---------------|-------------------|
| `PartitionTrieNodes` (0) | `0x00` | Node commitment hash → serialized node data | No — each node has a unique commitment |
| `PartitionValues` (1) | `0x01` | Terminal commitment hash → value bytes | **Yes** — content-addressed, identical values share the key |

The double-booking check is correct for `PartitionTrieNodes` but incorrect for `PartitionValues`.

## Proposed Fix

### Option A: Allow duplicate SET if value matches (recommended)

In `Mutations.Set()`, when a key is already in the `set` map, check if the new value is
identical to the existing value. If so, skip (idempotent write). If not, call the
`mustNoDoubleBooking` callback (this would be a real bug — same hash, different data = collision).

```go
func (m *Mutations) Set(k, v []byte) {
    ks := string(k)
    if m.mustNoDoubleBooking != nil {
        if len(v) > 0 {
            if existing, already := m.set[ks]; already {
                if bytes.Equal(existing, v) {
                    return // idempotent content-addressed write, skip
                }
                m.mustNoDoubleBooking(fmt.Errorf("conflicting SET mutation. The key '%s' was set with different value", ks))
            } else if _, already = m.del[ks]; already {
                m.mustNoDoubleBooking(fmt.Errorf("repetitive SET mutation. The key '%s' was already deleted", ks))
            }
        } else {
            // delete case — unchanged
            if _, already := m.del[ks]; already {
                m.mustNoDoubleBooking(fmt.Errorf("repetitive DEL mutation. The key '%s' was already deleted", ks))
            }
        }
    }
    // ... rest unchanged
}
```

**Pros**: Minimal change, correct semantics, catches real bugs (hash collision = different values for same key).
**Cons**: `bytes.Equal` comparison on each duplicate — negligible cost since duplicates are rare.

### Option B: Deduplicate in `commitNode`

Track already-written value keys in a set during the commit walk. Skip the `valuePartition.Set`
if the terminal key was already written.

```go
// In Commit(), pass a set to commitNode
written := make(map[string]struct{})
tr.mutatedRoot.commitNode(triePartition, valuePartition, tr.Model(), written)

// In commitNode:
if len(n.value) > 0 {
    vk := string(common.AsKey(n.terminal))
    if _, done := written[vk]; !done {
        valuePartition.Set(common.AsKey(n.terminal), n.value)
        written[vk] = struct{}{}
    }
}
```

**Pros**: No change to `Mutations` API.
**Cons**: Changes `commitNode` signature (all callers), introduces allocation for the tracking set.

### Option C: Partition-aware double-booking

Make `NewMutationsMustNoDoubleBooking` partition-aware — only enforce strict checking for
the trie node partition, allow idempotent writes for the value partition.

**Cons**: Couples the batch writer to partition semantics, more complex API.

## Recommendation

**Option A** is the cleanest. It preserves the safety invariant (catching real bugs like hash
collisions) while correctly handling the content-addressed value deduplication case. The fix is
localized to `Mutations.Set()` and doesn't change any signatures or APIs.

## Test Plan

1. **Unit test: identical large values** — Create a trie, insert two keys with identical values
   (> `terminalCommitmentSizeMax`), commit. Verify no panic, both keys readable.

2. **Unit test: identical small values** — Same but with small values (<= threshold). These are
   embedded in the commitment, so no value store write. Should already work — verify.

3. **Unit test: different values same hash** — If feasible, construct values that hash to the same
   truncated blake2b. The `mustNoDoubleBooking` callback should fire (real collision detection).
   This may be impractical with 31-byte hashes.

4. **Unit test: regression for trie node partition** — Verify that duplicate trie node writes
   (which would indicate a real bug) still trigger the panic.

5. **Existing tests** — All existing `immutable/tests/` must pass unchanged.

6. **Benchmark** — Measure overhead of `bytes.Equal` on the duplicate path. Should be negligible.
