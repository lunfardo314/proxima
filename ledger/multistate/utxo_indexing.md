# UTXO indexing

How a UTXO gets indexed in the trie state, and how a lock author controls it.

The principle: **index keys come from the UTXO tuple itself, not from Go**.
There is no hardcoded coupling between the indexer and any particular lock, so
an EasyFL author can write a new lock with indexable fields and the indexer
picks them up with no Go change.

## The tuple layout

| Index | Content |
|-------|---------|
| 0 | `amounts` — token balance, inflation, frozen coverage |
| 1 | **index-value tuple** — the values this UTXO is indexed under |
| 2 | lock bytecode |
| 3 | chain constraint (optional; present iff this is a chain output) |
| 4.. | per-lock extras |

## The index-value tuple

Position 1 holds an EasyFL tuple of byte slices, each element a value to index
this UTXO under. Each non-empty element produces exactly one trie entry:

```
<TriePartitionControllers> || <byte len(value)> || value || <UTXOID>
```

Empty elements (`0x`) are **silently skipped** — not a validation error. So a
tuple whose entries are all empty, or an empty tuple, means "this UTXO is not
indexed at all". That is what lets a lock use a fixed-arity tuple even when
some positions are not meaningful for a given instance.

The ceiling is the EasyFL tuple format's own: at most 256 elements.

### What the library locks index

| Lock | Index-value tuple |
|------|-------------------|
| `sigLock` | `(holderID)` — 32 bytes, no `a(...)` prefix |
| `chainLock` | `(chainID)` — 32 bytes, no `c(...)` prefix |
| `stemLock` | `(0x00)` — a single zero byte |
| `delegateLock` | `(masterHolderID, targetChainID)` |
| `tagAlong` | `(senderHolderID, targetChainID)` |
| a custom lock | any tuple of up to 256 values; empty entries skipped |

**Convention for lock authors:** when a lock has several controllers, position 0
is the *master* — the unconditionally-unlocking party, such as the wallet holder
or delegation master — and position 1 is the *target*, a chain or counterparty.
The framework treats every position identically; this is convention only, so
that lookups mean the same thing across locks.

Note that some internal locks read target-first (`_tagAlong($0=target,
$1=sender)`, `_delegateLock($0=target, $1=master, …)`). The public wrapper
translates, which is why the public and underscore forms take their arguments
in different orders.

## Reading index values from EasyFL

```easyfl
// $0 — index of the element in the index-value tuple
func selfIndexValue : atPath(concat(selfOutputPath, 1, $0))
```

`atPath` descends nested tuples, so this returns the `$0`-th element of the
tuple at output position 1. The library locks are thin wrappers over their
underscore forms parameterised on it:

```easyfl
func sigLock      : _sigLock(selfIndexValue(0))
func chainLock    : _chainLock(selfIndexValue(0))
func delegateLock : _delegateLock(selfIndexValue(1), selfIndexValue(0), …)
func tagAlong     : _tagAlong(selfIndexValue(1), selfIndexValue(0))
```

From the framework's point of view the lock at position 2 is **arbitrary EasyFL
bytecode**, and the only property required of it is that it sits at the right
slot (`require(equal(selfBlockIndex, 2), …)`). A new lock reading
`selfIndexValue(N)` for its indexable fields is indexed automatically.

## Two consequences worth knowing

**Index-space collisions are possible and are treated as a feature.**
`sigLock(H)` and `chainLock(H)` for the same 32-byte `H` produce identical trie
keys. In practice `H` is either `blake2b(sigType || pubkey)` or
`blake2b(genesisOutputID)` — independent random hashes — so a real collision is
astronomically unlikely. The lookup API is therefore "find UTXOs indexed under
this 32-byte value", without distinguishing chain from holder.

**Indexing costs storage deposit.** The dust rule charges an output's
*effective* size, which includes one trie row per index-value entry, not just
the UTXO's own bytes (`ledger/sdeposit.go`). Adding an index value to a lock
makes every output using it more expensive to create.

## Adding a new indexed lookup

Encode the value into the index-value tuple at construction time. Do **not**
add cases to the mutation path — the indexer is deliberately generic, and a
special case there re-introduces the Go coupling this design removed.

---

The full refactor that produced this — problem, goal, per-file code analysis,
migration plan and the rejected alternatives — is archived at
[`claude/archive/shipped/utxo_indexing_refactor.md`](../../claude/archive/shipped/utxo_indexing_refactor.md).
