# Native tokens — design strategy

Status: **open design discussion**, no implementation. Started 2026-05-12.
Updated 2026-05-13: foundry + `token(...)` / `tokenAmount(...)` shape.

Goal: add native (tagged) tokens to the Proxima transaction model. A native
token is identified by a **tag equal to a chain ID**. The chain whose ID is
the tag is called the **foundry** of that token. The foundry's controller
is the issuance authority: only a transaction that transits the foundry can
mint or burn its tag. Outside of mint/burn, per-tag amounts are conserved
across every transaction.

The current single-asset PRXI preservation is enforced in `validateOutputs`
(the "mismatch between token amounts" check). Native tokens generalise
this to per-tag preservation, with the foundry as the single point that
can introduce a non-zero supply delta.

---

## Shape of the design

### 1. Tag identity — the chain ID is the tag

A native token's tag **is** a chain ID. There is no separate policy object,
no script hash, no registry. The chain whose ID equals the tag is the
foundry for that tag.

Consequences:
- Tag uniqueness is inherited from chain-ID uniqueness — no new collision
  surface.
- The foundry chain's existing constraints (sigLock / EasyFL covenants)
  already gate who can authorise mint/burn — no new authority model.
- Burning the foundry chain freezes total supply forever (or, depending on
  the issuance-policy choice below, is forbidden while supply > 0). Either
  rule gives us NFT-like and fixed-supply behaviour with no extra
  machinery.

### 2. Foundry constraint — the per-tag header

A foundry is a chained UTXO that carries, in addition to its chain
constraint, a `foundry(...)` constraint at a fixed extras slot. The
foundry constraint holds the global state of the tag:

- total supply currently in circulation
- issuance policy parameters (see open questions below)
- any bookkeeping the policy needs (e.g. last-inflation slot, cap)

A transaction that consumes the foundry produces a new foundry output for
the same chain ID. The transit may change the recorded supply — that
change is the **signed supply delta** for the tag in this transaction.

### 3. UTXO-side: `tokenAmount(tag, amount)`

A regular (non-foundry) UTXO carries native tokens via an extended EasyFL
constraint:

```
tokenAmount(<tag>, <amount>)        amount > 0
```

`tag` is a 32-byte chain ID; `amount` is the carried quantity.

Placement: `tokenAmount` does **not** live at a fixed extras slot. A UTXO
may carry any number of `tokenAmount` constraints at any non-reserved
positions in its tuple. The dominant case is zero (no constraint, zero
overhead); the second-most-common case is one. Multi-tag UTXOs are
allowed and need no special model — the storage-deposit minimum already
disincentivises gratuitous UTXO bloat, so a hard "one tag per UTXO"
restriction is unnecessary.

Both `tag` and `amount` must be **inline literals** in the bytecode — not
results of EasyFL expressions. This keeps the Go reconciler trivial (the
tag key is the raw bytes; the amount is read directly without
evaluation).

Where this is enforced: **inside the `token(...)` builtin**, not by
`tokenAmount` itself. When the reconciler walks inputs/outputs looking
for `tokenAmount(tag, amount)` instances for a declared tag, it checks
that both args are literal-shaped bytecode and fails validation
otherwise. Concentrating the rule in `token(...)` keeps `tokenAmount`'s
own bytecode trivial and means there is exactly one place that defines
what a "valid native-token-bearing UTXO" looks like.

### 4. Tx-side: `token(tag, foundryProducedIndex)`

Preservation is enforced by a **transaction-level** constraint:

```
token(<tag>, <produced foundry index>)
```

Both arguments are inline literals. Semantics:

- The constraint traverses the transaction's consumed and produced outputs
  and sums `tokenAmount(tag, ...)` quantities on each side.
- If `<produced foundry index>` is absent (a sentinel value, e.g.
  `0xFF`), the rule is **pure conservation**:
  `Σ consumed(tag) == Σ produced(tag)`.
- If `<produced foundry index>` points to a produced foundry output for
  this `tag`, the rule is:
  `Σ consumed(tag) + supplyDelta(foundry) == Σ produced(tag)`,
  where `supplyDelta` is `producedFoundry.supply − consumedFoundry.supply`
  (positive = mint, negative = burn). The constraint also verifies that
  the indicated produced output is in fact the foundry for `tag` and that
  the corresponding foundry input is consumed.

Auditability — same pattern as `redeem(...)`:
- A transaction must declare, at the `TxConstraints` level, every `tag`
  that appears anywhere in its inputs or outputs.
- If a `tokenAmount(tag, ...)` is present in any UTXO but no matching
  `token(tag, ...)` exists at the tx level, validation fails.
- This makes per-tag reasoning local and indexable: a verifier can list
  the tags touched by a tx by scanning its tx-level constraints alone.

### 5. Implementation — Go, not EasyFL

`token(...)` aggregates over the full input/output set and matches by
tag. Pure EasyFL cannot iterate efficiently over arbitrary slot
positions; the constraint is therefore a **Go builtin** registered as an
EasyFL function. Same model that `redeem()` uses today.

Requirements:
- Simple, well-documented Go: per-tag running totals on consumed and
  produced sides, plus the foundry delta lookup.
- The aggregation results are **cached** on the transaction context (one
  pass for all tags in the tx), same caching pattern as `redeem()`.
- Behaviour must be reproducible on other platforms — the spec is the Go
  code plus a normative prose description; no platform-specific
  arithmetic, fixed 64-bit semantics.

`tokenAmount(...)` and `foundry(...)`, by contrast, are ordinary
extended-EasyFL constraints — they are read by the Go reconciler, not
evaluated for side effects.

---

## Issuance policy — embedded immutable script

The simplest mechanism is the most general one: an **optional EasyFL
script slot on the foundry output**, enforced immutable across foundry
transits. Concretely:

- The foundry has a reserved fixed extras index for a **policy script**.
- If the slot is **empty**, the foundry's controller has full discretion
  over mint/burn (subject only to the chain-controller's lock).
- If the slot is **non-empty**, two things hold:
  - The bytecode at this slot is **immutable** — every foundry transit
    must produce the same bytes (same shape as how the chain origin slot
    is fixed across the chain's life).
  - The script is **evaluated on every foundry transit** and must
    succeed. It receives access to the consumed and produced foundry
    state (including the new supply value), so it can express rules
    like:
    - `<circulating supply> <= S` (hard cap)
    - `produced.supply == consumed.supply` (sealed / no further
      mint/burn, useful after retirement)
    - rate-limited inflation (against the foundry's tracked
      last-mint slot)
    - burn-only, mint-only, etc.

This collapses what was previously a `policyKind` byte plus several
hard-coded variants into a single uniform mechanism. No new wire
extension is needed to ship inflation caps, freeze-on-delete, or any
other policy — it is all expressible as an EasyFL script. A foundry with
no script is the minimum-viable launch: the controller's signature is
the only gate, and the `<= S` cap, retirement freeze, etc. are
applications that add a script.

How the policy script reads foundry state:
- No new tx-context accessors are needed. The script reads its sibling
  `foundry(tag, supply)` constraint (and the consumed-side counterpart)
  using standard EasyFL bytecode-parsing functions already in the
  library — `parseBytecode`, `parseInlineData`,
  `parseInlineDataArgument`, etc. The policy script is just a regular
  constraint that happens to be invoked on the foundry transit and that
  walks the input/output tuples it cares about by index.

Foundry deletion (no produced foundry for a consumed one):
- Whatever the policy script encodes wins. A script that wants to
  forbid retirement encodes that itself (e.g. requires the produced
  foundry to exist); a script that permits it does nothing special.
  Empty script ⇒ deletion is governed only by the chain-controller's
  signature. No hard-coded default in the Go reconciler.

---

## Wire layout

Three new constraint shapes, all read by the Go reconciler:

- `tokenAmount(tag, amount)` — extended EasyFL constraint, inline-literal
  args. Carried by any non-foundry UTXO that holds native tokens. Lives
  at **any non-reserved position** in the UTXO tuple; multiple
  occurrences allowed. ~41 bytes per instance (32 tag + 8 amount +
  overhead).
- `foundry(tag, supply)` — extended EasyFL constraint at **tuple index 4**
  on the foundry output (the first extras slot after the chain
  constraint at index 3). Carries the current circulating supply.
- `policyScript` — **optional** EasyFL bytecode at **tuple index 5**,
  immediately after the foundry constraint. Immutable across foundry
  transits. Absent ⇒ no on-chain issuance policy beyond the
  controller's signature.

`token(tag, foundryProducedIndex)` lives at the **transaction-constraint**
level (alongside `redeem`), not at any UTXO slot. Both args inline
literals.

Foundries do **not** need to be sequencer outputs — they are ordinary
chained UTXOs. They can in principle be delegated (and thus frozen via
the delegate lock), but a delegated/frozen foundry must be unfrozen
before it can be used for mint/burn; delegate state and the
foundry/policy pair never need to coexist at the same slot indices in a
single mint/burn transaction.


---

## Implementation plan

The closest existing pattern is **`redeemScript`** — the Go-implemented,
tx-level builtin at `ledger/local_script_builtins.go:19-65`, registered
in `def_upgrade0.go:49`. `token(...)` mirrors it almost line-for-line:
walk the inputs/outputs once, cache per-tag aggregates on the tx
context, fail on mismatch. The rest of the plan flows from that.

### Fixed decisions used below

- **Slot indices.** `foundry` is at tuple index **4** (first extras
  slot, immediately after `ConstraintIndexChain = 3` per
  `ledger/def_constants_path0.go:91-96`). `policyScript` is at tuple
  index **5**. Add named constants `ConstraintIndexFoundry = 4` and
  `ConstraintIndexFoundryPolicy = 5` next to `ConstraintIndexChain`.
- **Policy-script reading API.** No new EasyFL surface. The script
  reads its sibling `foundry(tag, supply)` constraint (and its
  counterpart on the consumed side) using the standard EasyFL
  bytecode-parsing functions already in the library — `parseBytecode`,
  `parseInlineData`, `parseInlineDataArgument`, etc. The policy script
  is a regular constraint that happens to be invoked on the foundry
  transit; it walks the input/output tuples it cares about by index.

### Phase A — EasyFL surface (parse-only)

New constraints, registered via `lib.mustRegisterConstraint(...)` in
`registerConstraints0` (`ledger/def_upgrade0.go:57-71`):

- `ledger/token_amount.go` — `TokenAmount` struct, `NewTokenAmount(tag,
  amount)`, `TokenAmountFromBytes(bytes)` deserialiser. 2-arg constraint,
  EasyFL source: assert `amount > 0`, expose `tag` and `amount` as
  inline literals; otherwise inert.
- `ledger/foundry.go` — `Foundry` struct, `NewFoundry(tag, supply)`,
  deserialiser. 2-arg constraint. No semantic check beyond format; the
  transit rules live in Phase C.
- `ledger/foundry_policy.go` — wraps an arbitrary EasyFL script. 1-arg
  constraint (the script bytecode), inert at parse time. The script is
  evaluated by Phase C, not by the constraint itself.

Output: parse-only constraints exist; transactions carrying them load
but their semantic rules don't yet fire.

### Phase B — `token(...)` Go builtin + per-tag tx-context cache

Mirror `redeemScript`:

- `ledger/token_builtin.go` (new): `evalToken(ctx)` Go function,
  signature `(tag, foundryProducedIndex) → ()`. Registered via
  `lib.mustRegisterBuiltinFunction("token", 2, evalToken)` next to
  `redeemScript` in `def_upgrade0.go`.
- Per-tx cache: extend `ledger/def_embed.go`'s `EvalContext` (where
  `Library.compiledScriptCache` already lives) with a
  `nativeTokenAggregator` map `tag → (consumedSum, producedSum,
  foundryConsumedIdx, foundryProducedIdx)`. First `token(...)` call in
  a tx populates the map by scanning all inputs and outputs once; later
  calls hit the cache.
- The builtin validates literal-shape of `tokenAmount` args during the
  scan (see §3 of the design); a non-literal `tokenAmount(...)` for the
  matched tag fails the tx.
- Mismatch error mirrors the PRXI message: `"native token amount
  mismatch for tag <hex>"`.

### Phase C — foundry transit semantics

Two distinct checks, triggered from `token(...)` when
`foundryProducedIndex` is non-sentinel:

1. **Chain match.** The consumed and produced foundry outputs share
   the same chain ID == tag. Verified by reading the chain constraint
   on both sides. Failure ⇒ `"foundry not transited for tag <hex>"`.
2. **Policy script.**
   - If the consumed foundry has no `policyScript`, the produced one
     must also have none.
   - If present, the produced bytecode must be byte-equal to the
     consumed (immutability).
   - Evaluate the script with the standard EvalContext; failure
     surfaces as the script's own error.

Both checks live in `ledger/token_builtin.go` (or a small
`ledger/foundry_transit.go` helper) — no new EasyFL surface.

### Phase D — `validateOutputs` hook + auditability

In `ledger/transaction/validate.go:165-210`:

- After the existing PRXI sum check, invoke the native-token pass:
  for each tx-level `token(tag, ...)` constraint, ensure the cached
  aggregator for that tag balances. Pure conservation if
  `foundryProducedIndex` is sentinel; with foundry delta otherwise.
- **Auditability:** scan inputs and outputs for any `tokenAmount(tag,
  ...)`; for every tag observed, require a matching tx-level
  `token(tag, ...)`. Missing declaration ⇒
  `"undeclared native token tag <hex>"`. Same indexability property as
  `redeemScript`'s commitment list.

### Phase E — TxBuilder helpers

In `ledger/txbuilder/`:

- `MakeFoundryOrigin(tag, initialSupply, policyScript)` — emits a new
  chain origin with the foundry constraint and (optionally) the policy
  script bytecode.
- `TransitFoundry(consumedFoundryIdx, newSupply)` — produces the
  transited foundry output and a paired tx-level `token(tag, idx)`.
- `AddTokenAmount(outputBuilder, tag, amount)` — puts a `tokenAmount`
  constraint into the next free slot of the given output.
- Convenience wrappers `Mint(tag, amount, recipient)` and `Burn(tag,
  amount)` composed from the above.

### Phase F — Indexer

In `ledger/multistate/mutate.go`, extend the index-value tuple at output
slot 1 to include the `tag` for any foundry output and for any output
carrying one or more `tokenAmount` constraints (deduplicated). Reuses
the existing `TriePartitionControllers` partition pattern so wallets
can look up by tag through whatever `get_outputs`-by-key endpoint ships
next.

### Phase G — CLI

In `proxi/node_cmd/`:

- `foundry.go` (new subcommand tree): `create`, `mint`, `burn`,
  `retire`.
- `send.go` — add `--tag <hex>` flag that emits a `tokenAmount`
  constraint on the produced output and a balancing tx-level
  `token(tag, sentinel)` constraint.

### Phase H — Tests (UTXODB)

In `ledger/tests/native_token_test.go`:

1. `tokenAmount` literal-arg enforcement (positive + negative).
2. Pure conservation: transfer tag T between two sigLocks, both sides
   balance.
3. Mint: foundry transit increases supply by Δ; outputs gain Δ; with
   and without `policyScript`.
4. Burn: symmetric.
5. Auditability: tx with a `tokenAmount` constraint but no matching
   `token(...)` is rejected.
6. Multi-tag tx: two `token(...)` constraints, two tags, both
   independently balance.
7. Policy script: `<= cap` reject when mint exceeds cap; accept at the
   cap.
8. Foundry retire: with empty script, allowed under controller
   signature; with `produced.supply == consumed.supply` script, the
   retire tx that drops supply to 0 is rejected.

---

## Related references

- `claude/utxo-indexing.md` — current UTXO tuple layout and the
  index-value slot 1 design.
- `feedback_utxo_vs_tx_bytes.md` (auto-memory) — UTXOs persist longer
  than the tx that creates them. Drives the "absent constraint = zero
  overhead" baseline; the storage-deposit minimum handles the
  multi-tag-bloat case organically, so no hard cap on `tokenAmount`
  count per UTXO is needed.
- `redeemScript(...)` at `ledger/local_script_builtins.go:19-65`
  (registered in `def_upgrade0.go:49`) — closest existing analogue for
  `token(...)`: tx-level builtin, Go implementation, per-tx cache,
  auditability via tx-constraint declaration. The implementation plan
  below mirrors its shape directly.
- `validateOutputs` in `ledger/transaction/` — single point currently
  enforcing PRXI conservation. Natural extension point for the per-tag
  reconciler.
