# Output-kind index

## Status

**SPEC / PROPOSAL — not implemented.** Pre-testnet structural change; no
backward compatibility required (hardfork). Awaiting sign-off before any code.

## Problem

"Enumerate all outputs of a given kind" (all foundries, all sequencers, all
delegations, all DEX orders) currently has no index. Kind is *derived* by
parsing an output's constraints (sequencer/foundry at constraint index 4,
delegate lock at index 2, …), so every kind query is O(all chains):

- `multistate.SugaredStateReader.GetSequencersWithDelegations` is literally
  commented "scans all chains" (`sugared.go:440`).
- `api/chain_explorer` `serveList` with a `kind` filter walks every chain via
  `IterateChainedOutputs` and classifies each in memory.

This does not scale. At a projected hundreds of thousands of chains, finding
5 foundries means examining all 300k outputs. Result caps (`max`) do **not**
help: for a rare kind you never reach the cap, so you still scan everything.

The only structural fix is an **index keyed by kind**, maintained per ledger
state (the kind set is branch-specific).

## Existing precedent: DEX orders already do this

`ledger/def/lock_dex_orders.easyfl` puts a 4-byte ASCII role tag into the
index-values tuple and gets prefix-scannable enumeration for free:

```
func _ordrPrefix : 0x4f524452   // "ORDR" — 4-byte ASCII prefix
func _sideBuy    : 0x00
func _sideSell   : 0x01
// index_values[1] = "ORDR" || tag || side   (4 + 32 + 1 = 37 bytes)
//   bytes 0..3 = "ORDR", 4..35 = tag, 36 = side
```

and enforces it in the order constraint:

```
require(equal(len(selfIndexValue(1)), u64/37),         !!!...)
require(equal(_entryPrefix(selfIndexValue(1)), _ordrPrefix), !!!...)
require(equal(_entrySide(selfIndexValue(1)), _sideSell),     !!!...)
```

This proposal generalizes that proven, in-tree idiom into a single **output-kind
registry** covering every indexable output kind.

## How indexing works today (facts the design relies on)

- The index-values tuple is a **literal stored element** at constraint index 1
  (`ConstraintIndexIndexValues`), written by the builder, e.g.
  `b.PutConstraint(EncodeIndexValuesTuple([][]byte{holderID[:]}), ConstraintIndexIndexValues)`
  (`txbuildercore/helpers.go:66`).
- The indexer **iterates that stored tuple** — `mutate.go:351` calls
  `o.IndexValues()` which decodes slot 1; it does **not** call the lock's Go
  `IndexValues()`. So adding a tag = builder writes one more entry; `mutate.go`
  is untouched (matches the `feedback_indexing_via_slot1` rule).
- Each non-empty tuple entry produces one trie row under
  `TriePartitionControllers`, keyed:

  ```
  [ TriePartitionControllers | byte(len(value)) | value | outputID ]   // mutate.go:531 makeAccountKey
  ```

  Two consequences that shape this design:
  1. **The key does not encode the entry's position** in the tuple — only its
     value and length. So a kind tag can sit at any tuple position; enumeration
     is by value, not position.
  2. **The key is length-sensitive**: `[…|4|"ORDR"…]` and `[…|37|"ORDR"…]` are
     not under a common prefix (the length byte precedes the value). A given
     kind therefore has a *fixed entry length*, and enumeration prefix-scans
     `[Controllers | thatLength | kindConstant]`.

- Locks read their own index values in EasyFL via `selfIndexValue(n)`
  (`sigLock : _sigLock(selfIndexValue(0))`), so enforcing a tag is a one-line
  `require` in the owning constraint.

## Design

### Kind registry

A single static table — the source of truth — of 4-byte ASCII kind constants,
mirrored in Go and EasyFL (named functions, like `_ordrPrefix`). Proposed
constants (names are bikeshed-able):

| Kind | 4-byte tag | hex | Family | New entry? |
|------|------------|-----|--------|-----------|
| stem | `STEM` | `0x5354454D` | standalone (replaces current `{0}`) | no (1→4 B value) |
| chain, generic | `$GEN` | `0x2447454E` | chain (`$`) | +1 entry |
| chain, foundry | `$FND` | `0x24464E44` | chain (`$`) | +1 entry |
| chain, sequencer | `$SEQ` | `0x24534551` | chain (`$`) | +1 entry |
| chain, delegation | `$DLG` | `0x24444C47` | chain (`$`) | +1 entry |
| DEX order | `ORDR` | `0x4F524452` | compound `ORDR\|\|tag\|\|side` (existing) | no |

Three encoding shapes — the chain `$`-family answers the "generic isn't
enforceable" problem directly:

- **Namespaced (chain family, `$xxx`)** — a fixed 4-byte tag whose first byte is
  the family marker `$` (0x24) and whose remaining 3 bytes are the role code.
  Enforcement is **split by who can see the role**:
  - The **chain constraint** (constraint index 3, present in *every* chain
    incl. generic) enforces only the family invariant: `len == 4 && byte0 == '$'`.
    This guarantees every chain carries a `$xxx` tag — so "all chains, any role"
    is a 1-byte-prefix scan `[Controllers | 4 | '$']`, and generic chains are
    covered without needing a constraint that fires only for them.
  - The **role constraint** (foundry/sequencer/delegate), where present, enforces
    the exact value (`$FND` etc.). Generic = chain constraint only; the builder
    writes `$GEN`, not pinned to that exact value (read-side reclassifies — see
    Enforcement). Enumerate a role = full-value scan `[Controllers | 4 | $FND]`.

  So yes — this is the structured/compound idea from the review, applied to the
  chain family: a family prefix (`$`) the shared constraint enforces, plus a
  role suffix the role constraint enforces.
- **Standalone** (`STEM`) — a 4-byte tag with no family prefix. Enumerate =
  exact-value scan `[Controllers | 4 | STEM]`.
- **Compound** (`ORDR`) — the 4-byte constant prefixes a longer entry carrying
  sub-discriminators (DEX `tag`, `side`, 37 B total). Enumerate the whole kind =
  prefix-scan `[Controllers | 37 | ORDR]`; sub-queries narrow the value. DEX
  keeps `ORDR||tag||side` so bids/asks for a tag sort together — sell/buy are
  sub-kinds via the side byte, filtered read-side, not separate kind rows.

### Tuple position

- **Position 0 stays the controller/master** everywhere (load-bearing: sigLock
  unlock reads `selfIndexValue(0)`; the chain-explorer `controller` filter and
  every controller query assume `index_values[0]` is the controller). The kind
  tag never goes at position 0.
- The kind tag goes at a documented, role-specific position (it can be any free
  slot since the trie key is position-agnostic; the *constraint* asserts it at
  the position it placed it). Note delegation pins `delegateLockState` to the
  **last** position (`project_delegation_epoch_params`, Option C) — the kind
  tag must claim a fixed earlier slot, not the last.

### Enforcement (EasyFL)

Tags are **named function constants** (a raw `#FNDY` is not valid — `#name` is
the func-reference form, so the constant must be a defined function whose body is
the literal), plus a generic enforcement helper so each constraint is one line:

```
// kind-tag constants (binary literals)
func _stemTag      : 0x5354454d   // "STEM"
func _chainFamily  : 0x24         // '$' — 1-byte chain-family marker
func _genChainTag  : 0x2447454e   // "$GEN"
func _foundryTag   : 0x24464e44   // "$FND"
func _seqTag       : 0x24534551   // "$SEQ"
func _delegateTag  : 0x24444c47   // "$DLG"

// generic helpers
// $0 = tuple position of the kind tag, $1 = expected 4-byte tag value
func _enforceKindTag :
    require(equal(selfIndexValue($0), $1), !!!wrong_or_missing_kind_tag)

// chain-family invariant the chain constraint enforces for EVERY chain
// $0 = tuple position of the chain tag
func _enforceChainFamily : and(
    require(equal(len(selfIndexValue($0)), u64/4),                !!!chain_tag_must_be_4_bytes),
    require(equal(slice(selfIndexValue($0), 0, 0), _chainFamily), !!!chain_tag_must_start_with_dollar)
)
```

Then: the foundry constraint calls `_enforceKindTag(KIND_POS, _foundryTag)`; the
chain constraint calls `_enforceChainFamily(KIND_POS)`. (DEX already does the
equivalent inline at `selfIndexValue(1)`; it can adopt `_enforceKindTag` or stay
as-is.)

The index is only useful if **complete** — every foundry must carry `$FND`, so
the foundry constraint must `require` it. *Pollution* (a generic chain injecting
`$FND`) is tolerable: the read side re-classifies each candidate by parsing its
constraints (same "index narrows, predicate confirms" pattern the chain-explorer
already uses for the `controller`/`delegation_target` filters), so a false tag
is dropped. We enforce presence, not exclusivity.

### Enumeration / read API

`TriePartitionControllers` already supports prefix scans. Rename the
account-flavoured reader to reflect that it now scans arbitrary index values,
not just controller/account IDs: **`IterateOutputsForAccount` →
`IterateOutputsForIndexValue`** (and consider the underlying
`IterateUTXOsForController` / `IterateUTXOIDsForController` for the same rename).
This touches all existing callers (`sugared.go`, etc.).

- Standalone / namespaced kinds (`STEM`, `$xxx`): `IterateOutputsForIndexValue(tagBytes)`
  works directly for an exact-value scan (len(value)=4). "All chains" =
  prefix-scan the 1-byte `'$'` within the len-4 group.
- Compound kinds (DEX): need a thin helper that fixes the entry-length byte and
  scans `[Controllers | entryLen | kindPrefix]` (the iterator takes arbitrary
  byte prefixes; only a small wrapper is missing).

Callers that become O(kind-count) instead of O(all chains):
`chain_explorer serveList` kind filter, `GetSequencersWithDelegations`, any
"all DEX orders" query.

## Contracts

### Collision / reserved-length contract

Index-value entry lengths currently in use:

| Length | Used by |
|--------|---------|
| 1 | stem `{0}` (removed by this proposal — becomes 4) |
| 32 | holderID, chainID, master, target, sender |
| 37 | DEX `ORDR\|\|tag\|\|side` |

**Reserve length 4 for kind tags.** Contract: no real (non-kind) index value
may be exactly 4 bytes; all 4-byte entries are kind tags — either standalone
(`STEM`) or chain-family (`$xxx`, first byte `'$'`). Compound kinds (`ORDR`)
embed the 4-byte constant as a prefix at offset 0 of a distinctly-lengthed entry
(37 B). Within the len-4 group, `'$'` is reserved as the chain-family first byte
(so `[Controllers | 4 | '$']` = all chains); standalone tags must not start with
`'$'`. Document and assert in the relevant constraints.

### Cost contract

Effective storage size (and thus min deposit) counts `N * 33` where N = number
of index-values entries (`def_constants0.json` `storageDeposit`). The 33 bytes is
the output-ID suffix every trie index entry carries (`[Controllers | len | value
| outputID]`) — intrinsic, not avoidable overhead. So a **new** index entry costs
+33 (trie) + the tag bytes (4) in the slot-1 tuple; it buys exactly one thing —
enumerability of that value by prefix scan.

Consequences for this design:

- **Chains** — each chain gains one new `$xxx` entry → **+37 effective bytes
  per chain** (33 + 4), mandatory under the uniform family (see Scope). This is
  the real, permanent cost; it scales with chain count, not UTXO count
  (`feedback_utxo_vs_tx_bytes`).
  - **Delegation chains are expected to be by far the most numerous** chain kind
    (every delegator holds one), so they dominate the aggregate cost: the ~40
    bytes (`$DLG` entry: 33 trie suffix + 4 tag + tuple framing) lands on every
    delegation UTXO, permanently. This is the single biggest line item of the
    whole scheme — at, say, hundreds of thousands of delegations it is a few MB
    of trie plus a bump to each delegation's min storage deposit. Worth a
    deliberate ack: we pay it to make per-kind enumeration uniform and to avoid
    a special-cased index. (Mitigation if it ever bites: delegations are already
    enumerable by master/target, so `$DLG` is the *most* skippable tag — but
    skipping it means abandoning the uniform `$` family, which the chain
    constraint enforces. Flagged, not recommended.)
- **Stem** — *no* new entry: its existing 1-byte `{0}` entry widens to the 4-byte
  `STEM`. Net +3 tuple bytes, no extra +33. (And one stem per state, so trivial.)
- **DEX** — *no* new entry: reuses the existing `ORDR||tag||side` entry. Free.
- General rule: prefer folding a tag into an existing entry (DEX/stem) over
  adding a standalone entry; pay the +33 only when there is no entry to reuse
  (the chain case).

### Scope (which kinds actually emit a tag)

Decision: **uniform family — tag all chains** (`$GEN`/`$FND`/`$SEQ`/`$DLG`),
plus `STEM` and DEX.

- **All chains (generic / foundry / sequencer / delegation)** — tagged. The
  chain constraint enforces the `$` family on *every* chain; each role constraint
  enforces its full tag. This is **not** opt-in per kind: a "foundry-only tag"
  scheme is incoherent, because the chain constraint requires every chain to
  carry a `$xxx` tag — generic/delegation/sequencer must all comply. So the cost
  is one extra index entry (+33, see Cost contract) on every chain, full stop.
  What it buys: enumerate any chain kind by full-value scan, and "all chains" by
  the 1-byte `$` prefix scan. The +33 scales with **chain count** (heavy,
  long-lived outputs), not total UTXO count — the bulk ephemeral sigLock UTXOs
  stay untagged.
- **DEX sell/buy** — already tagged (`ORDR`), free. Fold into registry: yes,
  keeping one `ORDR` family + side byte (decision: keep).
- **Stem** — bare standalone `STEM` tag (decision: not namespaced). Uniqueness is
  free: exactly one stem output exists per state (the existing single-stem-
  per-branch invariant), so `IterateOutputsForIndexValue(STEM)` returns exactly
  that one output.
- **Plain sigLock / tag-along / timelock / HTLC / send-with-deadline** — the
  residual bulk of the UTXO set; **no tag** (tagging all of them is prohibitive
  and they're enumerable by controller already).

## Migration

Pre-testnet: **no backward compatibility, single hardfork.** Steps:

1. Define the registry: Go constants (e.g. `ledger/output_kinds.go`) + EasyFL
   tag functions + `_enforceKindTag` / `_enforceChainFamily` helpers in a shared
   `.easyfl`.
2. Builders (`txbuildercore/helpers_*.go`) append the kind tag to the slot-1
   tuple for each in-scope kind (`$xxx` for chains, `STEM` for stem).
3. Constraints add the assertion: role constraints call
   `_enforceKindTag(KIND_POS, _foundryTag)` etc.; the **chain constraint** calls
   `_enforceChainFamily(KIND_POS)` so every chain (incl. generic) is tagged
   (`ledger/def/*.easyfl`).
4. Stem: replace `StemAccountID = []byte{0}` and the stem lock's index value
   with `"STEM"`; update `GetStem()`'s prefix lookup (`state.go:392`).
5. Rename `IterateOutputsForAccount` → `IterateOutputsForIndexValue` (+ the
   `*ForController` underlying methods) and update all callers; add the compound
   prefix-scan helper to `multistate` for DEX-style kinds.
6. Repoint kind queries to prefix scans: `chain_explorer serveList`,
   `GetSequencersWithDelegations`, DEX enumeration.
7. Read side keeps re-classifying candidates (index narrows, predicate confirms).
8. Tests: per-kind, assert the indexed scan equals the full-scan set (the
   `api/chain_explorer/chain_explorer_test.go` equivalence test is the template);
   plus an "all chains via `$` prefix" test.

## Affected code

- `ledger/def/*.easyfl` — tag constants + `_enforceKindTag` / `_enforceChainFamily`
  helpers; chain constraint enforces the `$` family, role constraints enforce the
  full tag (foundry, sequencer, delegate, stem; DEX already has it).
- `ledger/txbuildercore/helpers_*.go` — builders write the tag.
- `ledger/lock_stem.go` (`StemAccountID`), `multistate/state.go` (`GetStem`).
- `multistate` — rename `IterateOutputsForAccount` → `IterateOutputsForIndexValue`
  (+ underlying `*ForController`); compound prefix-scan helper; update all callers.
- `api/chain_explorer/chain_explorer.go` — kind filter → prefix scan.
- `multistate/sugared.go` — `GetSequencersWithDelegations` → prefix scan.

## Decisions

Resolved:

- **Scope (3)** — **uniform family**: tag all chains (`$GEN`/`$FND`/`$SEQ`/`$DLG`),
  plus `STEM` and DEX. Foundry-only is not an option — the chain constraint
  enforces the `$` prefix on every chain, so all chains carry a tag by
  construction. Cost: +37 effective bytes per chain (see Cost contract).
- **DEX granularity (4)** — keep one `ORDR` family + side byte (preserves
  order-book trie ordering); sell/buy are sub-kinds filtered read-side.
- **Tag position (5)** — **per-role** (each role's constraint asserts the tag at
  its own documented position; must avoid position 0 and delegation's
  last-position `delegateLockState`).
- **Stem (6)** — bare standalone `STEM` tag (not namespaced); uniqueness comes
  free from the single-stem-per-state invariant.
- **Tag names (1)** — confirmed: `$GEN` / `$FND` / `$SEQ` / `$DLG` for chains,
  `STEM`, `ORDR` for DEX; `$` (0x24) is the chain-family marker.
- **Reserved length (2)** — confirmed: **4 bytes** for kind tags.

All decisions resolved. Spec is implementation-ready.

## Related

- `claude/dex_orders.md` — the `ORDR` precedent this generalizes.
- `claude/chain_explorer.md` — primary consumer (kind filter).
- `claude/native_token.md` — foundry / `holderID || tag` compound index.
- `feedback_indexing_via_slot1`, `feedback_utxo_vs_tx_bytes`,
  `feedback_index_values_framing` (memory) — the indexing rules this follows.
