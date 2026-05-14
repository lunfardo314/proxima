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

`tokenAmount(...)` is an ordinary extended-EasyFL constraint — read by
the Go reconciler for aggregation; its body only enforces local format
invariants (32-byte tag, non-zero amount).

`foundry(tag, supply)` is an extended-EasyFL constraint with real
semantic load: its body enforces the **tag-equals-chain-ID** invariant
across transit (skipped at origin where the chain ID is still
NilChainID). The Go reconciler additionally reads it to extract the
supply for the balance equation.

`foundry()` does **not** enforce immutability of anything past index 4.
Whatever lives at index 5+ — a policy script, additional data, or
nothing — is up to the foundry owner. If a policy script wants to lock
itself across every chain transit, it composes the universal helper
`selfImmutableOnSuccessorIndex($0)` (in `chain.easyfl`) with its own
body. The two predefined policy scripts shipped today
(`foundryNonDestructible`, `foundryMaxSupply`) do exactly that.

---

## Issuance policy — optional bytecode at index 5

The simplest mechanism is the most general one: any extra constraint(s)
on the foundry output **past index 4**. By convention the canonical
position for a single policy script is `ConstraintIndexFoundryPolicy =
5`, but additional indices may hold whatever further data or scripts
the owner wants. Concretely:

- The position holds **raw EasyFL bytecode** — there is no wrapper
  constraint. (Earlier drafts proposed a `foundryPolicy(script)`
  wrapper; it was dropped because the position itself identifies the
  policy and the wrapper added nothing.)
- If position 5 is **absent**, the foundry's controller has full
  discretion over mint/burn (subject only to the chain-controller's
  lock).
- If position 5 is **present**:
  - The script is **evaluated automatically** by the standard
    output-tuple validation pass — every non-amounts/non-index-values
    position of every produced output is compiled and evaluated;
    index 5 is no exception. The script's `selfXxx` accessors resolve
    to the producedFoundry output, and on the consumed side they
    resolve to the consumed predecessor.
  - `foundry()` does **not** enforce immutability of these bytes. If the
    script wants to lock itself across transit (the typical case), it
    composes the universal helper
    `selfImmutableOnSuccessorIndex(foundryPolicyConstraintIndex)` with
    its own body. The two predefined policies shipped today
    (`foundryNonDestructible`, `foundryMaxSupply`) do this.
  - The script may express any rule. Examples:
    - `<circulating supply> <= S` — shipped as `foundryMaxSupply($0)`.
    - "Retire only when supply is zero" — shipped as
      `foundryNonDestructible`.
    - Rate-limited inflation, burn-only, mint-only, etc. — write as
      EasyFL.

A foundry with no script is the minimum-viable launch: the
controller's signature is the only gate. The two predefined policies
plus the `selfImmutableOnSuccessorIndex` building block cover the
common cases; richer rules are just EasyFL.

How the policy script reads foundry state:
- No new tx-context accessors are needed. The script reads its sibling
  `foundry(tag, supply)` constraint (and the consumed-side counterpart)
  using standard EasyFL bytecode-parsing functions already in the
  library — `parseBytecode`, `parseInlineData`,
  `parseInlineDataArgument`, etc. The policy script is just a regular
  constraint that happens to live at position 5 of a foundry output; it
  walks the input/output tuples it cares about by index.

Foundry deletion (no produced foundry for a consumed one):
- Whatever the policy script encodes wins. `foundryNonDestructible`
  requires the consumed foundry's supply to be 0 at chain discontinue.
  Absent script ⇒ deletion is governed only by the chain-controller's
  signature. No hard-coded default anywhere.

---

## Wire layout

Two new typed constraints + one raw-bytecode slot:

- `tokenAmount(tag, amount)` — extended EasyFL constraint, inline-literal
  args. Carried by any non-foundry UTXO that holds native tokens. Lives
  at **any non-reserved position** in the UTXO tuple; multiple
  occurrences allowed. ~41 bytes per instance (32 tag + 8 amount +
  overhead). The body enforces local format only (32-byte tag, non-zero
  amount); the literal-arg invariant is checked centrally by the
  `token(...)` builtin during its scan.
- `foundry(tag, supply)` — extended EasyFL constraint at **tuple index 4**
  on the foundry output (the first extras position after the chain
  constraint at index 3). Carries the current circulating supply. The
  body enforces format **and** the tag-equals-chain-ID invariant at
  every transit (skipped at origin).
- **Policy script** — **optional**, raw EasyFL bytecode at **tuple
  index 5** (`ConstraintIndexFoundryPolicy`). No wrapper constraint.
  Auto-evaluated as part of the standard output-tuple validation pass.
  `foundry()` does NOT enforce immutability of this position — it is
  up to the script to self-lock via
  `selfImmutableOnSuccessorIndex(foundryPolicyConstraintIndex)` (the
  predefined policies do this).

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
  deserialiser. 2-arg constraint. Body enforces format only at this
  phase; the foundry-transit invariants (tag-equals-chain-ID, policy-
  slot immutability) are added in Phase C.

No wrapper constraint is registered for the policy slot — slot 5 is
raw EasyFL bytecode, auto-evaluated by the standard validation pass.

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

### Phase C — foundry transit semantics (in EasyFL)

Following the general rule "**enforce in EasyFL when possible; reach
for Go only when the rule cannot be expressed there**" (see CLAUDE.md),
the foundry's only transit invariant lives in `foundry()`'s EasyFL body
rather than in `evalToken`:

1. **Chain match.** Produced `foundry.tag` must equal `chain.ChainID` at
   the same output. At origin (`chain.ChainID == NilChainID`) the check
   is skipped — at first transit the chain ID becomes real and is
   enforced from then on. Chain's own validation already propagates
   `ChainID` across transits, so checking the produced side is enough.
2. **Policy-script evaluation.** Automatic — `runTuple` evaluates every
   position of every produced output as ordinary bytecode, so the
   policy at index 5 (if any) fires naturally during the standard
   validation pass. The script's `selfXxx` accessors resolve to the
   producedFoundry on the produced side and to the consumed
   predecessor on the consumed side.
3. **No foundry-enforced immutability past index 4.** Whether the
   policy script (or any further data at index 6+) survives across a
   transit is purely the policy's own concern. The universal helper
   `selfImmutableOnSuccessorIndex($0)` (in `chain.easyfl`) provides
   self-lock semantics: a constraint at position N that AND-s
   `selfImmutableOnSuccessorIndex(u64/N)` cannot be replaced or
   removed by any subsequent transit while it remains in the chain.
   Used by `foundryNonDestructible` and `foundryMaxSupply`.

`evalToken` Go side only retains the defensive `pf.Tag ==
requestedTag` / `cf.Tag == requestedTag` checks (so a `token()` call
referring to the wrong produced index fails locally) and the balance
equation itself.

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

In `proxi/node_cmd/foundry/` (subpackage, mirrors `delegate/`):

- `create.go` — emit a foundry chain origin with `foundry(NilChainID,
  0)`. Initial supply at origin is always 0; the real chain ID is not
  known until the tx is finalised, so no tokenAmount outputs can be
  tagged in the same tx. Minting happens at a later transit via
  `mint`. Flags:
  - `-t / --target <lock>` — foundry chain controller. Defaults to
    the wallet account.
  - `--non-destructible` — attach the `foundryNonDestructible`
    predefined policy at index 5.
  - `--max-supply N` — attach `foundryMaxSupply(N)` at index 5.
  - The two policy flags are mutually exclusive; only one predefined
    policy script can be attached. Arbitrary user-supplied bytecode is
    not accepted in v1.
- `mint.go` (TODO) — first or subsequent foundry transit that
  increases supply and produces `tokenAmount(realTag, N)` outputs to a
  target lock. Builds the paired tx-level `token(realTag,
  producedFoundryIdx)`.
- `burn.go` (TODO) — symmetric.
- `retire.go` (TODO) — discontinue the foundry chain.
- `send.go` — add `--tag <hex>` flag that emits a `tokenAmount`
  constraint on the produced output and a balancing tx-level
  `token(tag, sentinel)` constraint.

### Phase H — Tests (UTXODB)

In `ledger/tests/native_token_test.go`:

1. `tokenAmount` literal-arg enforcement (positive + negative).
2. Pure conservation: transfer tag T between two sigLocks, both sides
   balance.
3. Mint: foundry transit increases supply by Δ; outputs gain Δ; with
   and without a policy script at index 5.
4. Burn: symmetric.
5. Auditability: tx with a `tokenAmount` constraint but no matching
   `token(...)` is rejected.
6. Multi-tag tx: two `token(...)` constraints, two tags, both
   independently balance.
7. `foundryMaxSupply(N)`: reject when produced supply > N; accept at
   the cap.
8. `selfImmutableOnSuccessorIndex` (via either predefined policy):
   a transit that replaces or removes the policy bytecode at index 5
   is rejected from the consumed side; a transit that leaves it
   byte-equal passes.
9. `foundryNonDestructible`: chain discontinue while consumed supply
   is non-zero is rejected; chain discontinue with consumed supply = 0
   is allowed.
10. Foundry retire (no policy script): allowed under controller
    signature.

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
