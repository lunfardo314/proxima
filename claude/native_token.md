# Native tokens — design and reference

Status: **SHIPPED on develop08**. Started 2026-05-12; original
implementation (Phase D Go-level audit pass) shipped 2026-05-14;
**refactored to the "every constraint accounts for itself" model on
2026-05-18** — the audit pass was deleted in favour of in-constraint
enforcement; `foundry` reduced to 1-arg `foundry(supply)`; `tokenAmount`
became a Go builtin.

Goal: add native (tagged) tokens to the Proxima transaction model. A
native token is identified by a **tag equal to a chain ID**. The chain
whose ID is the tag is called the **foundry** of that token. The
foundry's controller is the issuance authority: only a transaction
that transits the foundry can mint or burn its tag. Outside of
mint/burn, per-tag amounts are conserved across every transaction.

Base-token preservation lives in `validateOutputs` ("mismatch between token
amounts"); native tokens generalise it to per-tag preservation, with the
foundry as the single point that can introduce a non-zero supply delta.

---

## Design principle: constraint-local enforcement

A native token's invariants are split into **three local responsibilities
plus one closing balance check**. No constraint is responsible for the
whole-tx state; each one validates only what it can see, and the cache
they share is just a running tally — there is no "audit pass" that
walks the tx after the fact.

| Responsibility | Lives in | Enforces |
|---|---|---|
| Declare a tag and its foundry-supply delta | `token(tag, foundryIdx)` (tx-level) | Tag well-formed; foundry-transit form: produced foundry's chain ID == tag, reach consumed predecessor, compute `Δ = producedSupply − consumedSupply` |
| Account each native-token instance onto the cache | `tokenAmount(tag, amount)` (UTXO-level) | Both args inline literal; `amount > 0`; tag must already be declared; pre-check overflow then add `amount` to the per-tag consumed-or-produced sum |
| Closing balance equation | `agg.CheckBalances()` at tail of `validateOutputs` | For each declared tag: `consumedSum + Δ == producedSum` (mint) or `consumedSum == producedSum + Δ` (burn) |
| Gate the supply on origin and on every transit | `foundry(supply)`, produced side | Supply is 0 at chain origin; on a transit it may differ from the chain predecessor's only under a `token(...)` declaration for this foundry's own tag pointing at this output, named by index in `TxConstraints` in the predecessor's foundry unlock params |

The **only sequencing assumption** is that `token()` runs before
`tokenAmount()` — which is structurally guaranteed because tx-level
constraints fire before per-output constraints (see
`validateTxLevelConstraints` and `_runOutputs` in
`ledger/transaction/validate.go`).

---

## Shape of the design

### 1. Tag identity — the chain ID is the tag

A native token's tag **is** a chain ID. There is no separate policy
object, no script hash, no registry. The chain whose ID equals the tag
is the foundry for that tag.

Consequences:
- Tag uniqueness is inherited from chain-ID uniqueness — no new
  collision surface.
- The foundry chain's existing lock (sigLock / EasyFL covenants) already
  gates who can authorise mint/burn — no new authority model.
- Burning the foundry chain freezes total supply forever (or, depending
  on the policy script, is forbidden while supply > 0). Either rule
  gives NFT-like and fixed-supply behaviour with no extra machinery.

### 2. Foundry constraint — the per-tag header

A foundry is a chained UTXO whose chain ID **is** the tag. It carries
its supply in a 1-arg `foundry(supply)` constraint at
`ConstraintIndexFoundry = 4` (first extras slot after `chain` at
index 3):

```
foundry(supply)        supply is z64-encoded uint64
```

The tag is **not** stored on the foundry — the sibling chain
constraint's `ChainID` IS the tag. `chain()` enforces ChainID
preservation across every transit, so the tag is invariant by chain's
own rule. (Earlier 2-arg `foundry(tag, supply)` carried a redundant
tag arg; refactored 2026-05-18.)

The optional policy script at `ConstraintIndexFoundryPolicy = 5` is
raw EasyFL bytecode auto-evaluated as part of the standard output-tuple
validation pass. `foundry()` does NOT enforce immutability of that
position — policy scripts self-lock via
`selfImmutableOnSuccessorIndex(foundryPolicyConstraintIndex)`. See
"Issuance policy" below.

#### Supply: 0 at origin, moves only under a declaration

The supply delta is computed and balanced only for tags a tx-level
`token(...)` declares. A transit that omits the declaration is therefore
outside the balance equation, so `foundry()` itself has to constrain the
supply. All of it is checked on the **produced** side, where the output's
own chain constraint is at hand:

- **at chain origin**: supply must be 0. Nothing of the tag can circulate
  yet, and there is nothing to declare against — the chain ID is still
  the origin ID, so no `token(tag, ...)` could name it.
- **on a transit, supply equal to the chain predecessor's**: nothing to
  declare, no further check. This is the path of a re-lock, a move, and
  of a sequencer transiting a delegated foundry chain.
- **on a transit, supply different**: the chain predecessor's unlock
  params at `foundryConstraintIndex` must be 1 byte — the index in
  `TxConstraints` of a `token(tag, foundryProducedIdx)` whose `tag`
  equals this output's own ChainID and whose `foundryProducedIdx` equals
  this output's index.

The tag is compared against the foundry's **own** chain ID, not the
predecessor's or a successor's: `chain()` already keeps the ChainID
invariant across transits, so one comparison at the produced output is
the whole check.

Reading the predecessor's supply through `parseInlineDataArgument(...,
#foundry)` has a second effect: the predecessor of a foundry output must
itself be a foundry, so a plain chain cannot grow a foundry mid-life.
Every foundry starts at its own chain origin, at supply 0.

`token()` then derives the mint/burn amount from exactly this transit and
`CheckBalances` ties it to the `tokenAmount` sums, which is what makes
the stored supply the true circulating amount.

The consumed side keeps one check: with a continuing chain, the
successor's slot at `foundryConstraintIndex` must be a foundry call
(symbol only). That is position immutability — a produced output that
simply dropped the foundry has no constraint left to run, so it can only
be caught from the predecessor.

### 3. UTXO-side: `tokenAmount(tag, amount)`

A non-foundry UTXO that holds native tokens carries one or more
`tokenAmount` constraints at any non-reserved positions in its tuple:

```
tokenAmount(<tag>, <amount>)        amount > 0
```

`tag` is a 32-byte chain ID; `amount` is uint64. **Both must be inline
literals** — not the result of an EasyFL expression — so the per-tx
cache can read them directly without sub-evaluation.

Placement is free: multi-tag UTXOs are allowed; the dominant case is
zero `tokenAmount` instances (no overhead). The storage-deposit minimum
disincentivises gratuitous bloat.

`tokenAmount` is a Go builtin (`evalTokenAmount`). At every invocation
it enforces locally:

1. arg 0 (tag) is inline-data literal, 32 bytes.
2. arg 1 (amount) is inline-data literal, decodes to uint64 > 0.
3. The tag must already be in the per-tx cache. If not, the constraint
   fails — this is how "undeclared native token" is rejected, at the
   constraint itself, no global audit needed.

Then, as a side effect, it adds `amount` to the per-tag
`ConsumedSum` or `ProducedSum` (side derived from the eval path).
The addition is pre-checked against `MaxUint64` at the call site;
overflow fails the constraint.

### 4. Tx-side: `token(tag, foundryProducedIdx)`

```
token(<tag>, <foundryProducedIdx>)
```

A **fixed-arity-2** tx-level constraint (Go builtin `evalToken`). Both
args inline literals:

- `tag`: 32-byte chain ID.
- `foundryProducedIdx`: single byte. `0xFF` (`FoundryIdxNone`) is the
  reserved sentinel meaning "no foundry transit; pure conservation".
  Any other byte names a produced foundry output.

What `token()` does — **only**:

- Sentinel form: declare `(tag, deltaMag = 0, isBurn = false)`.
- Foundry-transit form: locate the produced foundry; verify
  `producedOut.ChainConstraint().ChainID == tag`; reach the consumed
  predecessor via `pcc.PredecessorInputIndex`; read both supplies;
  compute `deltaMag, isBurn` using only unsigned subtraction-of-smaller-
  from-larger; declare `(tag, deltaMag, isBurn)`.

Duplicate declarations for the same tag in one tx are rejected (a
builder bug). `token()` does NOT enforce the balance equation — that
falls to `tokenAmount` accounting + the closing check.

Practical consequence of the `0xFF` sentinel: a foundry produced at
output index 255 cannot be a transit target. With max-outputs = 256
this caps foundry-transit txs to ≤ 255 outputs, which is fine.

### 5. Closing balance check

At the tail of `validateOutputs`, after all per-output constraints have
fired and the per-tx cache is fully populated,
`tx.nativeTokenAggregator.CheckBalances()` runs:

- **Mint** (or sentinel): `producedSum == consumedSum + deltaMag`
- **Burn**: `consumedSum == producedSum + deltaMag`

Each addition is pre-checked against `MaxUint64`. The call is gated on
the aggregator being non-nil so txs that never touched native tokens
pay zero cost. This is the only tx-wide step in the native-token
pipeline; everything else is local-constraint.

### 6. Implementation — Go builtins, foundry stays EasyFL

Both `token` and `tokenAmount` are Go builtins:
- iteration over UTXO slot positions is more natural in Go;
- the per-tx cache is a Go `map` on the transaction object;
- arithmetic with explicit overflow guards is awkward in EasyFL.

`foundry(supply)` is the only native-token constraint with a real
EasyFL body: its position self-lock at slot 4, `len(supply) <= 8` plus
the origin / transit supply rule above on the produced side, and the
successor-is-a-foundry check on the consumed side. The serde wrapper
(`Foundry` struct) lives in `ledger/foundry.go`.

---

## Issuance policy — optional bytecode at index 5

`ConstraintIndexFoundryPolicy = 5` holds **raw EasyFL bytecode** (no
wrapper constraint). The standard output-tuple validation pass
evaluates it like any other constraint position; `selfXxx` accessors
resolve to the producedFoundry (produced side) or consumed predecessor
(consumed side).

`foundry()` does **not** lock this position across transits. Policies
self-lock via `selfImmutableOnSuccessorIndex(foundryPolicyConstraintIndex)`
in their AND-composed body. Two predefined policies ship today:

- **`foundryNonDestructible`**: discontinue only when consumed supply is
  zero — all minted tokens must be burned back before retiring the
  foundry chain.
- **`foundryMaxSupply(N)`**: produced supply must be ≤ N on every
  transit.

Absent script ⇒ controller's lock is the only gate on mint/burn/retire.
Foundry retire (no successor output) is governed entirely by whatever
the policy says (or the chain controller's signature if there is no
policy).

Foundries are ordinary chained UTXOs — not sequencer outputs. They can
be delegated through the standard chainLock-master path (the foundry's
own holdings sit in a separate delegation UTXO), but the foundry chain
output itself never carries `delegationParams`.

---

## Wire layout

| Position | What | Notes |
|---|---|---|
| `tokenAmount(tag, amount)` | UTXO-level Go builtin | Any non-reserved tuple position. Multiple per UTXO allowed. ~41 bytes per instance. |
| `foundry(supply)` (at `ConstraintIndexFoundry = 4`) | UTXO-level EasyFL constraint | Lives on chained UTXOs only. Tag = sibling chain's ChainID; not stored. Supply 0 at origin. Unlock params on the consumed side: empty, or 1 byte naming the `token(...)` declaration when the successor's supply differs. |
| **Raw policy bytecode** (at `ConstraintIndexFoundryPolicy = 5`) | UTXO-level, optional | No wrapper. Self-locking is the policy's own job. |
| `token(tag, foundryProducedIdx)` | Tx-level Go builtin | Lives in TxConstraints alongside `redeem`. `foundryProducedIdx == 0xFF` ⇒ pure conservation. |

---

## File / symbol map

- `ledger/native_token.go` — all native-token Go code in one file:
  `NativeTokenAggregator`, `NativeTokenEntry`, `evalToken`,
  `evalTokenAmount`, plus the `TokenAmount` serde wrapper and
  `OutputBuilder.WithTokenAmount`.
- `ledger/foundry.go` — `Foundry` struct and 1-arg `foundry(supply)`
  serde wrapper.
- `ledger/foundry_policies.go` — `FoundryNonDestructibleBytecode`,
  `FoundryMaxSupplyBytecode(N)`.
- `ledger/def/native_token.easyfl` — `foundry`, `foundryNonDestructible`,
  `foundryMaxSupply` EasyFL bodies (tokenAmount is no longer here —
  it's an embedded Go builtin).
- `ledger/def/def_embed0.json` — registrations for `token` and
  `tokenAmount` as embedded functions (`embeddedAs evalToken` /
  `evalTokenAmount`).
- `ledger/transaction/native_tokens.go` — `Transaction.NativeTokenAggregator()`
  lazy-allocation helper. (The old `validateNativeTokenAuditability`
  pass was deleted on 2026-05-18; its job is now done by
  `CheckBalances` invoked from the tail of `validateOutputs`.)
- `examples/exhelp/builder.go` — `MakeFoundryOriginOutput`,
  `TransitFoundry` (pushes the `token(...)` declaration and names it in
  the consumed foundry's unlock params), `DeclareTokenConservation`.
- `proxi/node_cmd/foundry/` — CLI subpackage (`create`, `mint`, `burn`,
  `retire`).
- `proxi/node_cmd/send.go` — `--tag <hex>` flag (pure-conservation
  `token(tag, 0xFF)` + tokenAmount-bearing recipient output).

---

## Tests

`ledger/tests/native_token_test.go` covers:

- Foundry origin (no policy / `foundryNonDestructible` / `foundryMaxSupply`).
- First mint (chain ID becomes real, supply grows, tokenAmount UTXO produced).
- Mint to another address.
- Multi-mint accumulation.
- Pure-conservation transfer with remainder + multiple inputs.
- Auditability: undeclared tag rejected by `tokenAmount` itself.
- `foundryMaxSupply`: accept at cap, reject over cap, burn still allowed.
- `foundryNonDestructible`: reject retire with supply > 0; accept with
  supply == 0.
- Foundry retire (no policy) succeeds under controller signature.
- Policy-script self-immutability: a transit that drops the policy at
  slot 5 is rejected.
- **Exploit probes** added 2026-05-18:
  - `TestExploitProbeMintWithoutDeclaration` — fabricate
    `tokenAmount(fakeTag, N)` with no `token()` declaration → rejected
    at the `tokenAmount` constraint ("not declared at tx level").
  - `TestExploitProbeOrdinaryTxWithFakeTokenAmount` — same, in an
    ordinary "send to self" tx shape.
  - `TestExploitProbeMintWithSentinelDeclaration` — declare
    `token(fakeTag, 0xFF)` and produce 1000 fakeTag tokens; rejected by
    the closing balance check (consumed=0, produced=1000, Δ=0).

`ledger/tests/foundry_test.go` holds the foundry-constraint tests in
three groups: position immutability, the supply rule, and the inline
sigLock-controller guard that bans delegation. The supply group walks the
rule end to end: origin must be at 0 and a plain chain cannot grow a foundry;
an undeclared transit may keep the supply but not inflate it, not zero it
(which would let the amount be minted twice), and not zero it to slip
past `foundryNonDestructible`; a declared mint and a declared no-op
transit both validate; and every wrong wiring of a supply change is
rejected — declaration present but not pointed at, sentinel declaration,
index pointing nowhere, a constraint that is not a `token(...)` call, and
a second foundry in the same tx riding on the first one's declaration.

---

## Related references

- `claude/utxo-indexing.md` — UTXO tuple layout and the slot-1
  index-value design used by `WithTokenAmount` to add compound
  `controller || tag` entries.
- `feedback_utxo_vs_tx_bytes.md` (auto-memory) — UTXOs persist longer
  than the tx that creates them; drives the "absent constraint = zero
  overhead" baseline.
- `redeemScript(...)` at `ledger/local_script_builtins.go` — the
  original tx-level Go builtin that `token` follows in shape.
- `validateOutputs` in `ledger/transaction/validate.go` — the only
  tx-wide validation point; native-token closing balance check is its
  last step.
