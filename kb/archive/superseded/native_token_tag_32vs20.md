# Native-token tag: 32 bytes vs a 20-byte prefix — analysis & decision

**Status: SHELVED (keep 32-byte tags).** This records the analysis so we don't
re-litigate it. Revisit only on concrete signal that a high-volume native token
(e.g. a stablecoin) is landing.

## The idea

A native token's *tag* is currently the full 32-byte foundry chain ID. The tag
appears in three places per use:

- `tokenAmount(tag, amount)` — the UTXO-level constraint on every token-bearing
  output (`ledger/native_token.go`, embedded Go `evalTokenAmount`).
- `token(tag, foundryProducedIdx)` — the tx-level declaration, once per tx
  (embedded Go `evalToken`).
- the compound index-values entry `holderID‖tag` at output constraint index 1,
  built wallet-side by `appendCompoundIndexValue`
  (`ledger/txbuildercore/helpers_native_token.go`).

Proposal: shorten the tag to a fixed 20-byte prefix of the chain ID to save
bytes on the transaction and in ledger state (~12 bytes per occurrence; ~24 per
token-bearing UTXO once both the constraint and the index entry are shortened;
~12 per tx for the `token()` declaration). A native-token UTXO is ~150 bytes, so
~15% off *those specific* UTXOs.

## "Does not change the library hash" — confirmed

`token`/`tokenAmount` are Go-embedded (`ledger/def/def_embed0.json`:
`sym: token → embeddedAs: evalToken`, `sym: tokenAmount → embeddedAs:
evalTokenAmount`). `LibraryHash` is taken over the compiled library JSON
(`ledger/multistate/genesis.go` `lib.LibraryHash()`; `ledger/upgrade_utxo.go`),
which references embedded functions by name + arity only — not by Go body.
Relaxing the Go logic (same arity, same declaration) keeps the hash identical.

### But: that is a *silent* relaxation (the consensus caveat)

Because the hash does not change, there is no upgrade signal in the ledger. The
change would be a pure relaxation: existing 32-byte-tag txs validate identically
on old and new binaries, but a short-tag tx is valid on new nodes and **rejected
by old ones** (they re-run constraints on consumed outputs too —
`validate.go _runOutputs(PathToConsumedOutputs,…)`). Mixed binaries therefore
fork the moment a short tag appears. All validators must be on the new binary
before any short tag is emitted. Within one binary version it stays fully
deterministic (pure byte comparison).

## Security: is 20 bytes enough?

Chain ID = `blake2b256(originOutputID)` — effectively uniform 256-bit. A k-byte
prefix is `8k` bits.

- **Accidental collision** (N foundries share a k-byte prefix; birthday)
  ≈ N²/2^(8k+1). At 20 B (160 bit): N=10⁶ → ~3·10⁻³⁷; 10⁹ → ~3·10⁻³¹;
  10¹² → ~3·10⁻²⁵. Negligible at any realistic scale.
- **Malicious / mined collision** — grind a foundry's `originOutputID` so its
  chain ID matches a *target's* k-byte prefix (a targeted second-preimage; the
  exploit is counterfeiting: make foundry A collide with victim B's prefix so
  A-tokens pass as B-tokens at the balance equation). Cost ≈ 2^(8k):
  16 B → 2¹²⁸ (the usual floor), **20 B → 2¹⁶⁰**, 24 B → 2¹⁹².

**Verdict:** 20 bytes (160 bits) is secure — 2¹⁶⁰ targeted resistance, well above
the 2¹²⁸ norm, and astronomically safe against accidental collision. 24 bytes
only adds unused margin. If ever implemented, enforce **≥20** as the floor.

## The indexing concern, and why it is separable

Variable-length tags *in the index* fragment lookups: UTXOs of the same token
would sit under different-length keys, so one
`get_outputs?index_value=…` could not find them all.

Key fact that decouples it: **the index-values tuple (constraint index 1) is
never evaluated by the ledger** — `runTuple` skips it ("pure data, never
evaluated as bytecode"). Nothing ties the index's `holderID‖tag` to the
`token`/`tokenAmount` tag args; the balance equation reads the tag only from the
constraints. So the constraint tag and the index tag are independent, and one
could shorten the constraint while leaving the index at the full 32-byte tag
(zero indexing change). A *fixed* 20-byte tag everywhere also avoids
fragmentation (uniform length), at the cost of being breaking.

## Design options considered

- **Variable ≥20-byte prefix (hierarchy: chainID ⊇ token.tag ⊇ tokenAmount.tag).**
  Maximal saving, backward-compatible, but fragments the index and needs a
  prefix-scan with an ambiguity rule in the aggregator. Rejected (index mess).
- **Constraint-only relaxation, index stays full 32.** No indexing change, saves
  ~12 B on the constraint only. Variable constraint length is fine because
  constraints are not indexed. Modest.
- **Fixed 20 bytes everywhere (constraint + index).** Cleanest end-state:
  uniform, no prefix-scan, exact-match aggregator. Saves ~24 B/UTXO. **Breaking**
  — existing 32-byte-tag UTXOs become unspendable on consume-side re-validation.
  Requires changing the aggregator key (`base.ChainID` → 20-byte/string) and
  `TokenAmount.Tag`'s type.

## tag → foundry resolution (mint/burn)

With a 20-byte tag, can you still find the foundry chain record?

- **The ledger never needs to.** `token(tag, foundryProducedIdx)` reads the
  produced foundry at an explicit output **index**, takes its full 32-byte chain
  ID from the chain constraint in the tx, and compares `[:20]`. No trie lookup.
- **Token holders never need it.** send/receive/balance use only the 20-byte
  tag; the `token(tag, 0xFF)` sentinel references no foundry.
- **Foundry owners (mint/burn) reference the foundry by its full 32-byte chain
  ID, which they already have.**
- **And yes, prefix lookup is feasible** if a wallet/explorer ever wants
  "which foundry is this token": chain tips live under `TriePartitionChainID`
  keyed `[partition]‖chainID[32]` (`ledger/multistate/mutate.go`), and the trie
  already supports prefix iteration (`IterateChainTips` does
  `r.trie.Iterator([]byte{TriePartitionChainID})`). Iterating with prefix
  `[partition]‖tag20` yields the unique foundry (collision 2⁻¹⁶⁰). A thin
  `GetChainOutputByTagPrefix(tag20)` (state-reader interface + API + proxi) is a
  small additive, non-breaking add.

## Why shelved (cost/benefit)

The saving is real but modest, and it is currently pure speculation (no
native-token volume yet; foundries are expected to be few). Against it:

1. **It dissolves a clean invariant.** Today *the tag IS the full chain ID* —
   zero resolution logic; `tokenAmount` tag is directly a chain ID. Fixed-20
   makes the tag a prefix and forces a `GetChainOutputByTagPrefix` resolution
   path with new failure modes (0 matches = discontinued/unknown foundry;
   >1 = collision → must reject deterministically), plus a new API and a new
   tag type rippling through parsers/views.
2. **Storage deposits already price those bytes.** The extra 24 B/UTXO are paid
   for by the holder via the storage-deposit floor — not an unpriced ledger
   externality. So there is no "protect the ledger from bloat" urgency.
3. **YAGNI.** Building a resolution path + breaking migration + new API for a
   UTXO class that does not yet exist is premature.

The one legitimate pro is **migration cost scales with adoption**: the clean
breaking Fixed-20 variant is free now (drop the one test foundry) and grows
expensive once a token has many UTXOs. But waiting does **not** burn the bridge:
a future introduction can be made non-breaking (existing 32-byte tokens keep
validating; new foundries opt into short tags). Waiting only risks ending up
with the messier variable-length variant instead of the clean uniform one.

## Decision

Keep 32-byte tags (tag == full chain ID, simplest possible). Revisit only if a
high-volume native token is concretely planned/landing — and if so, prefer doing
the clean Fixed-20 early in that token's life, while its UTXO set is still small.

## If revived, the implementation sketch (Fixed-20, R2)

- `evalToken`: arg-0 tag length must be 20; transit form compares
  `tag == producedFoundry.ChainID[:20]`; sentinel declares the 20-byte tag.
- `evalTokenAmount`: arg-0 tag length must be 20; exact-match lookup.
- `NativeTokenAggregator` map key `base.ChainID` → 20-byte key (`string`).
- `TokenAmount.Tag` → 20-byte type; update `TokenAmountFromBytes`,
  `ParseTokenAmountBytecode`, `String()`.
- Keep wallet helper signatures taking `base.ChainID` (callers have the full
  ID); truncate to `[:20]` only at the emit/index/compare points
  (`TokenFoundryBytecode`, `NewTokenAmountBytecode`, `appendCompoundIndexValue`).
- Add `GetChainOutputByTagPrefix(tag20)` (state-reader interface + `Readable` +
  dummy + sugared + API endpoint + client) for mint/burn and display, returning
  the unique chain or erroring on 0/>1 matches.
- Breaking: drop existing 32-byte native-token UTXOs; coordinate the binary
  rollout (no library-hash change → no in-ledger upgrade signal).