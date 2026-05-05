# Refactoring of the UTXO indexing in the state

This breaking refactoring does **not** change base semantics of the ledger. It changes
the low-level plumbing of how UTXOs are indexed in the trie state: the index
keys come from the UTXO tuple itself rather than from Go-side `Lock`
methods, which removes a hardcoded coupling between Go and EasyFL.

---

## 1. Current architecture

To answer questions like "list all UTXOs unlockable by holder H" or "list all
UTXOs targeting chain C", the multistate maintains a **controller index**
inside the trie. For each output the trie holds, in addition to the UTXO
record itself, one auxiliary entry per *controller* declared by the lock.

### 1.1 Index key format

`ledger/multistate/mutate.go:522-528` — current key format:

```go
func makeAccountKey(id ledger.ControllerID, oid base.OutputID) []byte {
    // <TriePartitionControllers> || <byte len(id)> || id || oid
    return common.Concat([]byte{TriePartitionControllers, byte(len(id))}, id[:], oid[:])
}
```

Iterating with prefix `<TriePartitionControllers> || <len(id)> || id` returns
all UTXOIDs sharing controller `id`.

### 1.2 Where index entries get written / deleted

- **Write**: `addOutputToTrie` (`mutate.go:411-431`) calls
  `out.Lock().Controllers()` and writes one `accountKey` per controller.
- **Delete**: `deleteOutputFromTrie` (`mutate.go:327-353`) does the symmetric
  delete using the same `Controllers()` list.

### 1.3 The Go surface that supplies the controllers

`ledger/constraints_serde.go:46-58`:

```go
Controller interface { Constraint; ControllerID() ControllerID; AsLock() Lock }
Lock interface       { Constraint; Controllers() []Controller; Master() Controller }
```

Each lock implements `Controllers()` and a sometimes-nil `Master()`:

| Lock           | `Controllers()` returns                               | Notes                                  |
|----------------|-------------------------------------------------------|----------------------------------------|
| `SigLock`      | `[SigLock self]`                                      | `ControllerID = bytecode of a(holder)` |
| `ChainLock`    | `[ChainLock self]`                                    | `ControllerID = bytecode of c(chainID)`|
| `StemLock`     | `[StemLock self]`                                     | `ControllerID = []byte{0}`              |
| `TagAlongLock` | `[ChainLock(target), SigLock(sender)]`                | two controllers per UTXO                |
| `DelegateLock` | `[SigLock(master), ChainLock(target)]`                | two controllers per UTXO                |

Note that `ControllerID()` returns the **full constraint bytecode** for
sigLock/chainLock (prefix + length-tagged data + 32 bytes), not just the
underlying 32-byte hash. This means the trie key always carries the
constraint name. Two different lock types with the same 32-byte payload land
under different keys.

---

## 2. The problem

Every constraint that wants to be indexable has to be hardcoded in Go through
the `Lock` / `Controller` interfaces. New lock shapes — for instance a
multi-target distribution lock, a time-locked drawer, a custom covenant —
require a Go release; the EasyFL data layer alone is not enough. This breaks
the otherwise-clean property that EasyFL is the single source of truth for
UTXO semantics.

---

## 3. Goal

Make the index keys derivable from the **UTXO tuple itself**, with no Go
help. After the refactor, the on-chain data answers "what controllers does
this UTXO have" by direct inspection of the tuple. Adding a new lock then
only requires writing EasyFL — the trie indexing, the lookup APIs, the
mutation generator all keep working unchanged.

Trie partition layout, `makeAccountKey` shape, and the *behaviour* of all
existing locks stay the same. What changes is purely **where the indexable
values come from**.

---

## 4. New UTXO tuple layout

| Index | Old           | New                           |
|-------|---------------|-------------------------------|
| 0     | `amounts`     | `amounts` *(unchanged)*        |
| 1     | lock          | **index-value tuple** *(new)*  |
| 2     | chain (opt.)  | lock                            |
| 3     | —             | chain (opt.)                    |
| 4..   | extras        | extras                          |

The lock and chain constraints both shift up by one. The new slot at index 1
carries an EasyFL **tuple of byte slices**, each element a value to index
this UTXO under. Empty elements (`0x`) are silently skipped — they don't
produce trie entries and they aren't a validation error. So an entirely
empty tuple, or a tuple where every entry is `0x`, is equivalent to "this
UTXO is not indexed at all". This lets locks use fixed-arity tuples even
when some positions are not meaningful in a given UTXO instance.

Each non-empty element of the tuple becomes one trie index entry:

```
<TriePartitionControllers> || <byte len(value)> || value || <UTXOID>
```

The format of `accountKey` is unchanged; only its supplier changes (data, not
Go).

### 4.1 Per-lock examples

| Lock          | Index-value tuple             | Notes                                  |
|---------------|-------------------------------|-----------------------------------------|
| `sigLock`     | `(holderID)`                  | 32-byte holder, no `a(...)` prefix      |
| `chainLock`   | `(chainID)`                   | 32-byte chainID, no `c(...)` prefix     |
| `stemLock`    | `(0x00)`                      | single zero byte (matches today's marker) |
| `delegateLock`| `(masterHolderID, targetChainID)` | always two 32-byte values, master first |
| `tagAlong`    | `(senderHolderID, targetChainID)` | always two 32-byte values, sender first |
| custom lock   | any tuple of up to 256 byte values, empty entries skipped | author's choice, framework-agnostic |

The hard limit comes from the EasyFL tuple format itself: at most 256
elements. Individual elements may be empty bytes (`0x`); the indexer
silently skips them rather than rejecting the output. The tuple as a
whole may also be empty — equivalent to a tuple of all-empty entries:
"this UTXO is not indexed at all".

**Convention**: when a lock has multiple controllers, position 0 is the
*master* (the unconditionally-unlocking party — wallet holder, delegation
master) and position 1 is the *target* (chain or counterparty). This is just
a community convention for lock authors; the framework treats all positions
identically.

> **Index-space collision note**: under the new scheme, `sigLock(H)` and
> `chainLock(H)` for the same 32-byte hash `H` produce identical trie keys.
> In practice `H` is either `blake2b(sigType || pubkey)` or
> `blake2b(genesisOutputID)` — independent random hashes — and a collision is
> astronomically unlikely. The lookup API becomes "find me UTXOs indexed
> under this 32-byte value" without distinguishing chain vs. holder, which
> we consider a feature.

---

## 5. EasyFL surface

```easyfl
// $0 — index of the element in the index-value tuple
func selfIndexValue : atPath(concat(selfOutputPath, 1, $0))
```

`atPath` already descends nested tuples, so this returns the `$0`-th element
of the index-value tuple at output position 1.

The existing locks keep their semantics; we just rename them with an
underscore and parameterise them on `selfIndexValue(...)`:

```easyfl
func sigLock      : _sigLock(selfIndexValue(0))
func chainLock    : _chainLock(selfIndexValue(0))
func stemLock     : _stemLock(<existing args, no index reference — fixed shape>)
func delegateLock : _delegateLock(selfIndexValue(1), selfIndexValue(0), <existing args 3 onward>)
func tagAlong     : _tagAlong(selfIndexValue(1), selfIndexValue(0))
```

Note the swap on the last two: the public convention puts master/sender at
position 0, but the existing `_tagAlong($0=target, $1=sender)` and
`_delegateLock($0=target, $1=master, $2=maxFrozen, $3=share)` read
target-first. The wrapper translates between the two.

Internally (`_sigLock`, `_chainLock`, `_stemLock`, `_delegateLock`, `_tagAlong`)
the EasyFL is **byte-for-byte unchanged** from today's `sigLock` / `chainLock`
/ `stemLock` / `delegateLock` / `tagAlong`. Every existing test for unlock
semantics, signature checks, frozen-coverage rules, recurrences, etc. keeps
passing without modification.

The lock at fixed tuple position 2 is now arbitrary EasyFL bytecode. From the
framework's point of view, the only required property of "the lock" is
`require(equal(selfBlockIndex, 2), ...)` — i.e., it's at the right slot.
EasyFL authors can write entirely new locks that read `selfIndexValue(N)` for
their indexable fields, and the trie indexer will pick those values up
automatically.

---

## 6. Code analysis — what changes

### 6.1 Tuple-position constants

`ledger/def_constants_path0.go:92-94`:

```go
ConstraintIndexAmounts = byte(iota) // 0
ConstraintIndexLock                  // 1
ConstraintIndexChain                 // 2 — chain constraint is always at index 2
```

Becomes:

```go
ConstraintIndexAmounts     = byte(iota) // 0
ConstraintIndexIndexValues              // 1 (new)
ConstraintIndexLock                     // 2
ConstraintIndexChain                    // 3
```

EasyFL constants in `def_constants_path0.yaml` (`amountsConstraintIndex`,
`lockConstraintIndex`, `chainConstraintIndex`) shift to match.

Every `selfBlockIndex == 1` check inside the locks (`equal(selfBlockIndex,1),
!!!locks_must_be_at_block_1`) becomes `selfBlockIndex == 2`.

### 6.2 Output construction (`ledger/output.go`)

Direct callers of `ConstraintIndexLock` / `ConstraintIndexChain`:

- `OutputBuilder.WithLock` (line 236) writes lock at `ConstraintIndexLock`.
- `OutputFromBytesMainWithLib` (lines 116-129) parses amounts at index 0 and
  lock at index 1.
- `OutputBuilder` various places (236, 279, 284, …): chain constraint logic
  guarded by `ConstraintIndexChain`.
- `Lock()`, `ChainConstraint()`, `NumChainConstraints()` (~10 call sites in
  `output.go`).

All of these become positional — moving each by +1 — but no semantic change.

In addition, `OutputBuilder` gains a step that writes the index-value tuple
at the new index 1. Each lock's "build helper" (e.g. `WithLock(SigLock)`)
needs to populate both index 1 (index-value tuple) and index 2 (lock
bytecode).

### 6.3 Mutation generation (`ledger/multistate/mutate.go`)

- Line 349-353 (`deleteOutputFromTrie`) and line 422-430 (`addOutputToTrie`)
  switch from `out.Lock().Controllers()` to **iterating the index-value tuple
  at output[1]** and emitting one `makeAccountKey(value, oid)` per entry.
- An empty index tuple → no index entries (matches current behaviour for
  locks that return an empty `Controllers()` list, though today no lock does
  that).
- `makeAccountKey` itself stays unchanged.

This is the load-bearing simplification: the only piece of Go code that
talks to "Controllers" is gone.

### 6.4 The `Lock` Go interface (`ledger/constraints_serde.go`)

`Lock.Controllers()`, `Lock.Master()`, `Controller.ControllerID()` all become
**dead** on the indexing path. Two options:

- **Drop them entirely** — clean but ripples into wallet/UX code that uses
  `Master()` for "default unlock controller" display.
- **Keep them as wallet-side helpers**, with a clear note that they are no
  longer authoritative for indexing. New custom locks would need wallet-side
  support to be browsable, but indexing itself works regardless.

Recommendation: drop `ControllerID()`, drop `Controllers()` (no longer
load-bearing), keep `Master()` if and only if there is real wallet-side
demand; if there isn't, drop it too and have callers special-case the known
locks they actually need (`sigLock`, `chainLock`).

### 6.5 Indexed-lookup API (`ledger/multistate/state.go`)

- `GetUTXOIDsForController(addr ControllerID)` (line 268) and friends — keep
  the same signature but `addr` is now the bare 32-byte (or N-byte) value,
  not a bytecoded controller. Type alias `ControllerID = []byte` already
  models that, only callers' construction changes.
- `accountPrefix := common.Concat(TriePartitionControllers, byte(len(addr)),
  addr)` (lines 279, 341, 392) — works unchanged.

### 6.6 Callers that build lookup keys

- `multistate/scandb.go:261-263` — uses `o.Output.Lock().Controllers()` to
  rebuild expected keys during DB scan. Switch to iterating output[1].
- `proxi/node_cmd/setup_seq.go`, `proxi/node_cmd/allchains.go`,
  `api/server/txapi.go`, `api/server/server.go`, `api/api.go`,
  `api/client/client.go` — most of these consume the *result* of
  `GetUTXOsForController` and don't care about the key format, but a few
  build keys themselves; sweep and adjust.
- `lock_tag_along.go:71` and `lock_delegate.go:79` (`Controllers()`
  implementations) — go away if §6.4 drops the interface, otherwise they
  become unused.

### 6.7 Locks themselves

For each of `lock_signature.easyfl`, `lock_chain.easyfl`, `lock_stem.easyfl`,
`lock_delegate.easyfl`, `lock_tag_along.easyfl`:

1. Rename the public function (`sigLock` → `_sigLock`, etc.).
2. Add a thin public wrapper of the **original** name that bridges from the
   index-value tuple. For instance:

   ```easyfl
   func sigLock : _sigLock(selfIndexValue(0))
   ```

3. Update `equal(selfBlockIndex, 1)` → `equal(selfBlockIndex, 2)`.
4. For chainLock/sigLock — drop the body's "must be 32 bytes" length check
   since the wrapper guarantees the source via `selfIndexValue(0)`. Keep the
   non-zero check.

`lock_stem.easyfl` is special: stem has a fixed 9-arg shape and a fixed
"index value" of `0x00`. The wrapper just constructs that:

```easyfl
func stemLock : _stemLock($0,$1,$2,$3,$4,$5,$6,$7,$8)
// (no selfIndexValue — stem's index tuple is fixed at construction time
//  to 0x00, written by the Go output builder)
```

### 6.8 Genesis (`ledger/genesis.go`)

`GenesisOutput` writes amounts and lock; the upgraded builder now also
writes `(<holderID>)` at index 1. `GenesisStemOutput` writes `(0x00)` at
index 1. Genesis-stem-values pre-flight (just landed in `lock_stem.easyfl`)
keeps working — its conditions only touch fields by position, all of which
shift consistently.

### 6.9 Tests

- `ledger/tests/*` — most tests construct outputs through `OutputBuilder`
  and don't care about absolute positions; they rebuild after the constants
  shift. Spot-check tests that hardcode `byte(2)` / `byte(1)` for chain
  constraint placement (genesis builder, chain-origin tests).
- `ledger/lock_*.go` inline `init()` round-trip tests need the index tuple
  populated. Trivial fix in `Bytes()` / `Source()` if those helpers stay.
- `multistate/*_test.go` — verify trie key construction still matches.

### 6.10 Persistent state compatibility

This is a **breaking** change to the trie key layout: keys built from the
old `ControllerID = bytecode of a(holder)` will not equal keys built from
the new `value = holder` for the same logical controller. There is no
on-the-fly conversion available. The new ledger version cuts a fresh
testnet (consistent with v0.8 conventions, no v0.7 backcompat).

---

## 7. Migration plan

Phased to keep `go test ./ledger/...` green at every step.

### Phase A — tuple-position shift, no semantic change

Goal: introduce index 1 as a no-op carrier of an empty tuple, shift lock to
2 and chain to 3, leave indexing logic untouched.

A1. Bump `ConstraintIndexLock = 2`, `ConstraintIndexChain = 3`, add
    `ConstraintIndexIndexValues = 1` in `def_constants_path0.go` and the
    corresponding YAML.
A2. `OutputBuilder` writes an empty tuple at index 1 by default.
A3. Locks' `selfBlockIndex` checks: `1` → `2`. Chain references in
    `lock_stem.easyfl` (`chainConstraintIndex`) automatically pick up the new
    constant value.
A4. Sweep `output.go` and any direct positional access in tests.

After A: all locks still work, trie indexing still uses `Lock.Controllers()`,
but every output now reserves index 1 for the index-value tuple.

### Phase B — populate the index-value tuple

B1. Each `lock_*.go` Go builder fills the index-value tuple at construction
    time:
    - `SigLockBuilder`: `(holderID)` — 32 bytes
    - `ChainLockBuilder`: `(chainID)` — 32 bytes
    - `StemLockBuilder`: `(0x00)` — 1 byte
    - `DelegateLockBuilder`: `(master, target)` — 2×32 bytes
    - `TagAlongBuilder`: `(sender, target)` — 2×32 bytes
B2. Inline tests confirm round-trip.

After B: index-value tuple is populated but nobody reads it yet. Trie
indexing still uses `Lock.Controllers()`, which still returns the
bytecoded `ControllerID`. Behaviour identical.

### Phase C — flip the indexer

C1. In `mutate.go`, replace the
    `for _, accountable := range o.Lock().Controllers()` loop in
    `addOutputToTrie` and `deleteOutputFromTrie` with an iteration over the
    index-value tuple at `output[1]`. New helper, e.g.
    `Output.IndexValues() [][]byte`, lives in `output.go`.
C2. **At this point the trie key length changes** — keys go from
    `<partition>||<len(bytecode)>||<bytecode>||<oid>` to
    `<partition>||<len(value)>||<value>||<oid>`. This is the breaking
    moment; cut testnet here.
C3. `scandb.go:261-263` (the consistency-scan path) shifts to the same
    iteration.
C4. Update `GetUTXOIDsForController` and friends' callers to pass the bare
    value, not the bytecoded controller.

After C: the Go `Lock.Controllers()` is no longer called by indexing. It can
be removed.

### Phase D — drop dead Go interface

D1. Remove `Lock.Controllers()`, `Controller.ControllerID()`, and
    `Lock.Master()` (or keep `Master()` only if a wallet caller needs it).
D2. Remove the `Controller` interface itself if `Master()` goes.
D3. Remove `ControllerFromBytesWithLib`, `LockIsControlledBy`,
    `EqualControllers` if no longer used.
D4. Sweep `proxi/`, `api/`, wallet helpers; replace any remaining
    `Lock.Controllers()` reads with positional iteration of the output
    tuple, or with explicit type-switches when the caller actually needs to
    know "is this a sigLock?".

### Phase E — EasyFL renames + wrappers

E1. Rename `sigLock` → `_sigLock`, etc. in all five `lock_*.easyfl` files.
E2. Add `selfIndexValue` helper in a shared file (e.g. `ledger/def/output.easyfl`
    or extend `def_constants_path0.yaml`).
E3. Add the public wrappers (`sigLock`, `chainLock`, `delegateLock`,
    `tagAlong`, `stemLock`) that bridge to `selfIndexValue(N)`.
E4. Drop the now-redundant length checks inside the underscored primitives
    (`equal(len($0), u64/32)`) where the wrapper guarantees the shape.

After E: a third-party EasyFL author can write a custom lock that reads
`selfIndexValue(N)` and write whatever they want at index 1; the trie picks
it up automatically.

### Phase F — docs, cleanup

F1. Refresh `docs/txdocs/intro.md` (or wherever the UTXO model lives) to
    describe the new tuple layout.
F2. CLAUDE.md: add a `## 4. UTXO tuple layout` section documenting indices
    0..3 as fixed framework slots, 4+ as freeform.
F3. Drop "metadata-refactor" cross-references that mention old indices.

---

## 8. Risks / open questions

- **EasyFL constants vs Go constants drift**: today `lockConstraintIndex` is
  declared in YAML and `ConstraintIndexLock` in Go; both bump in lockstep
  (Phase A). A test should pin them together to prevent silent skew.
- **Wallet UX**: the loss of `Lock.Master()` might force wallets to type-switch
  on known locks. Acceptable but tracked.
- **Cost-of-indexing**: each output now carries a slightly larger tuple
  (extra empty-or-small element at index 1). For sigLock UTXOs this is +33
  bytes (1-byte tuple header + 32-byte holder) but minus the bytecode
  prefix overhead from the previous `ControllerID = bytecode`. Net: roughly
  break-even or smaller.
- **Custom locks at index 2**: nothing prevents an author from writing an
  index 1 tuple that does *not* correspond to controllers their lock at
  index 2 actually checks. This is the same family of bug as a malformed
  amounts vector — caught by the lock's own validation, not by the
  framework. Document in the "writing custom locks" section.
- **Stem index value**: `(0x00)` is a magic single-byte placeholder.
  Alternative: empty tuple → no indexing. Stem already has a special
  partition (`StemAccountID = []byte{0}`) which is what
  `getStemOutput` queries (`state.go:392`). Keep the placeholder for
  source compatibility.

---

## 9. What this refactoring does **not** do

- No change to ledger semantics (validity rules, inflation, healthiness,
  coverage bounds).
- No change to trie partition layout (`TriePartitionControllers`,
  `TriePartitionLedgerState`, `TriePartitionChainID` unchanged).
- No change to `chainID` index (`makeChainIDKey`) or to the chain-output
  uniqueness rules.
- No change to amounts encoding, signature schemes, transaction wire
  format outside the output tuple positions.

---

## 10. Related cleanup: user-facing lock syntax

Today the wallet (and other CLIs accepting a lock as input — e.g.
`proxi node transfer -t a(0x0102..)`) takes lock arguments as **EasyFL
source** and runs them through the full EasyFL compiler to produce the
constraint bytecode. That is heavier than necessary for what is morally a
one-arg constructor, and it bakes EasyFL surface syntax into every wallet
input path.

This refactor is a natural moment to switch user-typed lock arguments to a
strictly human-readable parser-only format. Each lock kind has a **short**
prefix (one letter) and a **long** prefix (the lock's name), accepted as
synonyms — pick whichever reads better in context:

| Old (EasyFL source)        | New (short)                    | New (long, synonym)                            |
|----------------------------|--------------------------------|------------------------------------------------|
| `a(0x0102..)`              | `a/0102..`                     | `sig/0102..`                                   |
| `c(0x0102..)`              | `c/0102..`                     | `chainLock/0102..`                             |
| `tagAlong(0xCC.., 0xSS..)` | `t/<targetChainID>`            | `tagAlong/<targetChainID>`                     |
| `delegateLock(...)`        | `d/<targetChainID>[/<maxFrozen>[/<inflationShareInPromille>]]` | `delegate/<targetChainID>[/<maxFrozen>[/<inflationShareInPromille>]]` |
| stem lock                  | not user-typeable (system-only)| not user-typeable (system-only)                |

The shapes for `t/` and `d/` are deliberately shorter than the underlying
constraint suggests: the wallet is always the sender (for tag-along) or the
master (for delegate), so it fills those fields from the active wallet
identity rather than asking the user to retype them. The bracketed
suffixes on `d/` are optional — the wallet substitutes its configured
defaults for `maxFrozen` and `inflationShareInPromille` when absent.

Wallet recognises the prefix (short or long form), parses the
slash-separated hex payload, fills in any wallet-derived fields, and
assembles the output positionally — populating the index-value tuple at
slot 1 and the corresponding underscored bytecode (e.g. `_sigLock(...)`)
at slot 2. The EasyFL compiler no longer sits at the user-input boundary.

Benefits

- No EasyFL compiler dependency on every CLI invocation that takes a lock.
- Stable string format independent of how EasyFL surface syntax evolves.
- Trivial to validate (regex-level), trivial to autocomplete in shells.
- Better error messages (parser knows the lock kind from the prefix).

Tradeoffs

- Each user-typeable lock kind needs a wallet-side parser entry. Small
  fixed cost per known lock. Genuinely custom (third-party) locks that a
  wallet does not know about can still be passed as raw bytecode hex if a
  use case ever needs it.
- Mild migration burden in scripts, docs, and tests that hardcode the
  old `a(0x..)` form. Sweep with grep.

Scope and ordering

This is a wallet/CLI concern, not a ledger concern: nothing on-chain
changes for §10. It can land independently of the indexing refactor (the
underlying constraint bytecode is the same in both schemes), but doing it
together keeps the two breaking surface areas aligned in one testnet
cut. Add as Phase G after the migration plan.

---

## 11. Related: human-readable transaction dumps

`Output.Lines()` and the various transaction pretty-printers (proxi
`db txstore get`, the API `/api/v1/get_*` JSON variants that include a
human-readable section, the DAG explorer view) currently print each
constraint inline by its compiled name, e.g. `a(0x0102..)`.

After the refactor the lock at index 2 is a thin public wrapper over an
underscored primitive that reads from the index-value tuple at index 1.
Printing only the public name (`sigLock`) hides where the indexed value
actually lives; printing only the underscored primitive (`_sigLock(...)`)
hides the abstraction. Print **both**, on dedicated lines.

Suggested layout for an output:

```
output 0xCAFE..[2]
  amounts:        [tokenBalance=1_000, ...]
  index values:
    [0] 0x0102.. (32 bytes)
    [1] 0xDEAD.. (32 bytes)
    [2] (empty, not indexed)
  lock:           sigLock                ← public wrapper at slot 2
                  _sigLock(0x0102..)     ← underlying primitive with index values inlined
  chain:          ...                    (slot 3, when present)
  extras: ...
```

The "underlying primitive with index values inlined" line is a printing
convenience: the formatter walks the wrapper's bytecode, finds each
`selfIndexValue(N)` call, and substitutes the bytes from element `N` of
the index-value tuple. This is **not** a stored representation — the
bytecode on disk is the wrapper call. Calling `Output.Bytes()` round-trips
unchanged.

For unknown / third-party wrappers the formatter falls back to printing
the public name only (it has no way to know which arguments are meant to
be index-value substitutions). The index-value tuple is always shown
verbatim, so a human reader can still see the indexed values regardless
of whether the wrapper is recognised.

Empty entries in the index-value tuple are labelled explicitly
(`(empty, not indexed)`) so a reader doesn't mistake an absent line for
an indexable absence. This makes the silent-skip rule from §4 visible at
the dump layer.

Touch points: `ledger/output.go` (`Output.Lines()`), proxi
`db txstore get`, the API server's pretty-printer, the DAG explorer
template. Sweep at the same time as Phase D, or earlier — earlier helps
when debugging Phases B/C because the index-value tuple becomes inspectable
immediately.

---

## 12. Related: drop unused unlock-parameter slots 0 and 1

Each input today carries an unlock-parameter array indexed by the
**output constraint index**. Two slots are structurally empty:

- slot 0 (`amounts`) — pure data, nothing to unlock.
- slot 1 (after §4, the **index-value tuple**) — also pure data.

The array stores them as padding. A standard input pays at least 2 bytes
just for the empty placeholders before the first meaningful unlock.

Re-base the unlock array so its index `j` corresponds to output
constraint `j + 2`. The two empty slots disappear; the wire encoding of
each input shrinks by 2 bytes.

| Output constraint index `i` | Old unlock-array index | New unlock-array index |
|-----------------------------|------------------------|-------------------------|
| 0 (amounts)                 | 0 (always empty)       | — (no slot)             |
| 1 (index-value tuple)       | 1 (always empty)       | — (no slot)             |
| 2 (lock)                    | 2                      | 0                       |
| 3 (chain)                   | 3                      | 1                       |
| 4 (first extra)             | 4                      | 2                       |
| ...                         | ...                    | ...                     |

### 12.1 Touch points

**Go transaction builder** (`ledger/txbuilder/txbuilder.go`):

- `PutUnlockParams(inputIndex, constraintIndex, data)` does the `i - 2`
  subtraction internally so call sites keep speaking output-constraint
  indices (`ConstraintIndexLock`, `ConstraintIndexChain`).
- `PutSignatureUnlock`, `PutUnlockReference`, `PutStandardInputUnlocks`,
  `PutUnlockParams` for chain transitions, delegate-lock unlock, etc.
  all remain unchanged at the call site — only the internal storage
  shifts.
- The `UnlockParams` struct (`txbuilder.go:40-…`) still holds a
  `tuples.MustPutAtIdxWithPadding`-backed array; its length now starts
  at 0 for the first meaningful constraint.

**Transaction model** (`ledger/transaction/`):

- `Transaction.MustUnlockDataAt(idx)` (`tx.go:277`) and the
  `TxUnlockData` validator branch (`validate.go:285`) read the array
  by output-constraint index too — they get the same internal `i - 2`
  shift, transparent to callers.
- The pretty-printer (`util.go:148`) gains a label so dumps still
  show "Unlock data at constraint i = ..." with `i` in
  output-constraint terms (avoids surprising output by silently
  re-numbering).

**Context interface** (`ledger/def_embed.go:38`):

```go
UnlockParameters(inputIdx, constraintIdx byte) ([]byte, error)
```

`constraintIdx` stays in output-constraint coordinates; the
implementation adjusts internally. The single Go call site in
`ledger/lock_delegate.go:277` keeps using `ConstraintIndexLock`.

**EasyFL surface**:

- `selfUnlockParameters` (the current constraint's unlock bytes) — the
  primitive's binding fetches by `selfBlockIndex - 2` instead of
  `selfBlockIndex`. No EasyFL source needs editing.
- `selfSiblingUnlockParams(N)` (used by the delegate lock at
  `lock_delegate.easyfl:60` and elsewhere) likewise translates
  output-constraint `N` to array index `N - 2` internally.
- `consumedConstraintByIndex` is unrelated — it reads constraint
  bytecode, not unlock data.
- `ensure.easyfl:5,12` reads `selfUnlockParameters` as a byte index;
  the byte values are unchanged, only the storage location shifts.

### 12.2 Validation

- Inline transaction round-trip tests already check `tx.Bytes()` is
  byte-equal after re-encoding; verify total tx size shrinks by
  `2 × len(inputs)`.
- `ledger/tests/...` tx-validation tests cover all locks (sig, chain,
  stem, tag-along, delegate); the constraint-index translation must
  preserve every unlock check.
- A negative test: a transaction crafted with a 2-slot-padded unlock
  array (old format) must be rejected as malformed.

### 12.3 Scope and ordering

This is a **wire-format** change for transactions — the byte layout of
inputs shrinks. Like Phase C of the indexing refactor it is the kind of
breaking surface that wants to ride the same testnet cut.

Add as **Phase H**, sequenced after Phase E (EasyFL renames) so the
underscored primitives and wrapper layout are settled before tweaking
the unlock binding underneath them. Phase H is touch-light at call
sites — Go callers all use `PutUnlockParams(idx, ConstraintIndexLock,
...)` style and don't see the internal shift — but it requires
careful binding work in the `selfUnlockParameters` /
`selfSiblingUnlockParams` primitives so EasyFL semantics are
preserved.

Open question: keep `consumedConstraintByIndex` and friends in
output-constraint coordinates (consistent with `selfBlockIndex`) so
EasyFL authors never see the internal shift. That's the recommendation
above. The alternative — exposing the shifted index in EasyFL — would
let primitives drop the internal subtraction at the cost of breaking
every existing reference.
