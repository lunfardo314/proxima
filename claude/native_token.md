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
- No new tx-context accessors are needed. The script reads the consumed
  and produced `foundry(tag, supply)` constraints via the ordinary
  bytecode-parsing functions already available in EasyFL. That keeps the
  EasyFL surface unchanged — the policy script is just a regular
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
- `foundry(tag, supply)` — extended EasyFL constraint at **tuple index 3**
  on the foundry output (immediately after the chain constraint at
  index 2). Carries the current circulating supply.
- `policyScript` — **optional** EasyFL bytecode at **tuple index 4**,
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

## Suggested order of attack

1. Implement `tokenAmount(...)` and `foundry(...)` as extended EasyFL
   constraints at the agreed slots (`foundry` at index 3, `policyScript`
   at index 4); parse-only — semantic checks live in the Go reconciler.
2. Implement the Go-side `token(...)` builtin: per-tag aggregation,
   cache on tx context, mismatch errors mirroring `redeem(...)`.
3. Wire foundry transit checks: chain-input/output match, `policyScript`
   immutability, optional script evaluation.
4. Extend `validateOutputs` to invoke the per-tag pass and require
   tag-declaration auditability at the TxConstraints level (every tag
   appearing in any UTXO must have a matching `token(tag, ...)`).
5. TxBuilder helpers + indexer entry (so wallets can `get_outputs` by
   tag).
6. CLI: `proxi node mint`, `proxi node send` extended to carry a tag.
7. Tests, then end-to-end UTXODB flows (foundry create → mint → transfer
   → burn → retire), with and without a policy script.

---

## Related references

- `claude/utxo-indexing.md` — current UTXO tuple layout and the
  index-value slot 1 design.
- `feedback_utxo_vs_tx_bytes.md` (auto-memory) — UTXOs persist longer
  than the tx that creates them. Drives the "absent constraint = zero
  overhead" baseline; the storage-deposit minimum handles the
  multi-tag-bloat case organically, so no hard cap on `tokenAmount`
  count per UTXO is needed.
- `redeem(...)` in `ledger/` — closest existing analogue for
  `token(...)`: tx-level builtin, Go implementation, per-tx cache,
  auditability via tx-constraint declaration.
- `validateOutputs` in `ledger/transaction/` — single point currently
  enforcing PRXI conservation. Natural extension point for the per-tag
  reconciler.
