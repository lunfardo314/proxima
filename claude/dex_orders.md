# Buy / sell order locks for a DEX

## Goal

Add two lock-constraints, `sellOrder` and `buyOrder`, that enable trust-less
atomic swaps between a Proxima native token (tagged via a foundry chain ID)
and base tokens. Each order is a single UTXO; either it gets matched within a
timeout window, or the issuer reclaims it afterwards. The matching transaction
is the swap — there is no separate settlement step.

Each order also registers an index-values entry in the ledger state trie at
UTXO tuple position 1 (`ConstraintIndexIndexValues`), so the order book can
be enumerated directly from the trie by prefix-scanning that entry.

### Phased delivery

1. **PoC inside `examples/dex`** — implemented as a single EasyFL local script
   wired in via `redeemScript(<binary>)` on the consuming transaction and invoked
   from the order UTXO's lock element through `callRedeemer(<scriptHash>,
   <fnIdx>, args...)`. Zero ledger changes; per-consume cost is the script bytes
   carried by the consuming tx. This validates the design and exercises
   `redeemScript` / `callRedeemer` as a programmability vehicle.
2. **Graduate to base ledger** — once stable, move the two constraints into
   `ledger/def/lock_sell_order.easyfl` and `ledger/def/lock_buy_order.easyfl`,
   register them as standard 0-/2-/3-arg public locks. UTXO layout and index
   entries stay identical so previously-issued orders remain interpretable.

## Atomic-swap mechanics

A buyer who wants to lift a `sellOrder` UTXO `S` (carrying `X` native tokens of
tag `T` at `Y` base tokens per token, with `B` base tokens posted as the
order's own storage deposit) forms a transaction that:

- Consumes `S` plus enough buyer-owned base-token inputs.
- Produces a receipt output to the **seller** carrying `≥ B + X·Y` base tokens.
- Keeps the `X` native tokens in a buyer-owned output (or recycles them
  immediately). Token conservation at the tx level guarantees these tokens
  reappear somewhere; the order does not need to dictate where.

A seller who wants to lift a `buyOrder` UTXO `Q` (carrying `B` base tokens, no
native tokens, declaring an intent to buy `X` of tag `T` at `Y`) forms a
transaction that:

- Consumes `Q` plus enough seller-owned native-token inputs of tag `T`.
- Produces a receipt output to the **buyer** carrying `≥ B − X·Y` base tokens
  AND `tokenAmount(T, X)`.
- Keeps the `X·Y` base tokens in a seller-owned output.

In both directions the consuming tx is signed by the counterparty (single
signature; standard Proxima holder rules apply). The order locks do not
verify the counterparty's signature themselves — they only enforce the shape
and amounts of the **receipt output** flowing back to the order's issuer.
That receipt is what makes the swap atomic: either the counterparty pays the
issuer in full, or the consumed-output constraint rejects the transaction.

Orders are **all-or-nothing** in v1. Partial fills would require a recursive
"reduced order" receipt and are deferred.

## Index-values entry (UTXO tuple position 1)

Every order UTXO writes two entries into the index-values tuple at
`ConstraintIndexIndexValues` (UTXO tuple position 1). Each non-empty entry
becomes a row in the ledger state trie under `TriePartitionControllers`,
keyed by `<length byte> || <value> || <UTXO ID>`:

| Position | Bytes | Content |
|----------|-------|---------|
| 0        | 32    | issuer holder ID (seller for `sellOrder`, buyer for `buyOrder`) |
| 1        | 37    | `"ORDR"` (4 bytes ASCII, 0x4F524452) ‖ `<32-byte token tag>` ‖ `<side byte>` |

`<side byte>` = `0x00` for buy, `0x01` for sell.

The 4-byte ASCII prefix is for human-readability in `proxi db` dumps and
trie traces, and reserves a 37-length namespace for future order-book
variants. The trie's length-byte convention disambiguates length 37 from
existing 32-byte controller and 64-byte compound entries.

### Iteration

The trie orders entries lexicographically over `<length byte> || <value>`.
Prefix scans against the trie yield:

| Trie key prefix | Enumerates |
|-----------------|------------|
| `37 \|\| "ORDR"`                              | all orders, every tag, both sides |
| `37 \|\| "ORDR" \|\| <tag>`                   | full book for one token — bids (0x00) first, then asks (0x01) |
| `37 \|\| "ORDR" \|\| <tag> \|\| 0x00`         | bids for that token |
| `37 \|\| "ORDR" \|\| <tag> \|\| 0x01`         | asks for that token |

The issuer entry at index-values position 0 produces the usual 32-byte
controller-style trie row, so "all orders I issued" is a standard
controller prefix scan.

The order lock self-enforces this format at produce time, so an issuer
cannot register a sell order under a buy index key, under a tag different
from what the lock body operates on, or under any non-`"ORDR"` prefix.

## `sellOrder` lock

### Signature

```
sellOrder(price, timeoutSlots)
```

- `price` — uint64 base tokens per **one** native token, z-encoded (≤ 8 bytes).
- `timeoutSlots` — uint32, z-encoded (≤ 4 bytes), ≥ `constDexMinTimeoutSlots`
  (= 12, ≈ 2 minutes). No upper bound.

Token tag, issuer (seller) holder ID, and quantity for sale are NOT lock
arguments — they live in the index-values tuple at UTXO positions 1/0 (1
= compound index entry, 0 = issuer entry) and in the pinned `tokenAmount`
at UTXO position 3.

### Order UTXO shape

Fixed positions on the produced side:

| Position | Constraint |
|----------|------------|
| 0        | `amounts` — base tokens covering storage deposit; no inflation |
| 1        | `indexValues` — `(sellerHolderID, "ORDR" \|\| tag \|\| 0x01)` |
| 2        | `sellOrder(price, timeoutSlots)` |
| 3        | `tokenAmount(tag, X)` — the tokens for sale |
| 4 (opt)  | `randomizeConsumption(N)` — see below |

The lock enforces `selfNumConstraints ∈ {4, 5}` so no other constraints can
sneak in.

### Produce-side rules

- `selfBlockIndex == lockConstraintIndex` (position 2).
- `len(timeoutSlots-arg) ≤ 4` and `uint8Bytes(timeoutSlots) ≥ constDexMinTimeoutSlots`.
- `len(price-arg) ≤ 8` and `price > 0`.
- `len(selfIndexValue(0)) == 32` and `selfIndexValue(0) == txHolderID(txSignatureData)`
  (seller is the tx's signer; standard issuer-is-master pattern).
- `selfIndexValue(1) == concat(0x4F524452, tag, 0x01)` where `tag` is the
  argument of the pinned `tokenAmount` at position 3, parsed via
  `parseInlineDataArgument(selfSiblingConstraint(u64/3), 0, #tokenAmount)`.
- `selfNumConstraints` is 4 or 5; if 5, the constraint at position 4 must
  match the `randomizeConsumption` shape (see below).
- `selfEnforceZeroAmountsInNonChainedOutput`.

### Consume-side rules

`selfInputSlotPace = txSlot − slotOfInputByIndex(selfOutputIndex)`.

Two windows:

- **Δ < timeoutSlots — counterparty (buyer) path.** Unlock parameter of the
  lock = 1 byte `K`, the produced-output index where the seller's receipt
  lives. The lock requires the produced output at index `K` to have exactly
  these four constraints:
  - position 0: `amounts(receiptBase, 0, 0)` with
    `receiptBase ≥ originalBaseTokens(consumedOrder) + mul(X, price)`,
    where `X` is the quantity from the consumed order's `tokenAmount` at
    position 3.
  - position 1: `indexValues` whose position 0 equals `sellerHolderID` =
    `consumedSelfIndexValue(0)`.
  - position 2: `sigLock` (constant bytecode).
  - position 3: a 1-byte inline-data literal equal to `selfOutputIndex` of
    the consumed order (the order's index in the tx's input list). Built
    via `inlineData(byte(selfOutputIndex))`.
  - `selfNumConstraints == 4` on the receipt output.
- **Δ ≥ timeoutSlots — issuer reclaim.** Lock delegates to
  `_sigLock(selfIndexValue(0))`. The seller can spend the order's contents
  however they like, same pattern as `tagAlong` reclaim.

The 1-byte literal at position 3 of the receipt prevents fold attacks: if
the consuming tx attempts to consume two sell orders against one shared
receipt output, the two consumed orders would require literals equal to two
different input indices, which a single produced output cannot satisfy.

## `buyOrder` lock

### Signature

```
buyOrder(amount, price, timeoutSlots)
```

- `amount` — uint64 native tokens to buy, z-encoded (≤ 8 bytes).
- `price` — same encoding as for `sellOrder`.
- `timeoutSlots` — same.

Token tag and issuer (buyer) holder ID live in the index-values tuple
(UTXO position 1). The buyer is responsible for posting
`≥ amount · price + storageDepositOfBuyerReceipt`
worth of base tokens on the order UTXO; otherwise no seller can produce a
valid filling transaction.

### Order UTXO shape

| Position | Constraint |
|----------|------------|
| 0        | `amounts` — base tokens covering the order's deposit AND `amount·price` |
| 1        | `indexValues` — `(buyerHolderID, "ORDR" \|\| tag \|\| 0x00)` |
| 2        | `buyOrder(amount, price, timeoutSlots)` |
| 3 (opt)  | `randomizeConsumption(N)` |

`selfNumConstraints ∈ {3, 4}`. There is no `tokenAmount` on a buy order —
the tag is carried only in the index-values entry at position 1 and is
recovered by slicing the 4-byte prefix and 1-byte suffix off
`selfIndexValue(1)`.

### Produce-side rules

- `selfBlockIndex == lockConstraintIndex` (position 2).
- Argument-length and floor checks for `amount`, `price`, `timeoutSlots`
  matching `sellOrder`.
- `selfIndexValue(0) == txHolderID(txSignatureData)`.
- `selfIndexValue(1) == concat(0x4F524452, tag, 0x00)` where `tag` is
  recovered from `selfIndexValue(1)` itself (the lock proves the entry has
  the right shape by reconstructing it).
- `selfEnforceZeroAmountsInNonChainedOutput`.

### Consume-side rules

`selfInputSlotPace` as above.

- **Δ < timeoutSlots — counterparty (seller) path.** Unlock parameter = 1 byte
  `K`. The produced output at index `K` must have exactly five constraints:
  - position 0: `amounts(receiptBase, 0, 0)` with
    `receiptBase ≥ originalBaseTokens(consumedOrder) − mul(amount, price)`.
    (`originalBaseTokens − amount·price` must satisfy minimum storage
    deposit on its own; that's the buyer's responsibility at order-creation
    time.)
  - position 1: `indexValues` whose position 0 equals `buyerHolderID`.
  - position 2: `sigLock`.
  - position 3: 1-byte inline-data literal equal to `selfOutputIndex` of
    the consumed buy order.
  - position 4: `tokenAmount(tag, amount)` where `tag` is sliced from the
    consumed order's index-values entry at position 1, and `amount` is the
    lock argument.
  - `selfNumConstraints == 5` on the receipt output.
- **Δ ≥ timeoutSlots — issuer reclaim.** `_sigLock(selfIndexValue(0))`.

## `randomizeConsumption(N)` helper

Optional constraint that lowers contention for a hot order by gating
who-can-consume on a per-slot lottery.

```
// $0 — N (uint8 or uint16 z-encoded, 2 ≤ N ≤ 32)
func randomizeConsumption :
   or(
      selfIsProducedOutput,
      isZero(
         mod(
            slice(
               blake2b(concat(signaturePublicKey(txSignatureData), txSlot)),
               0, 7
            ),
            uint8Bytes($0)
         )
      )
   )
```

- The salt is `publicKey || txSlot`. Using `signaturePublicKey` directly
  (instead of `txHolderID`, which itself hashes the public key) drops a
  redundant inner blake2b while preserving the per-signer / per-slot
  property: a consumer cannot vary the tx essence within one slot to
  grind a passing hash.
- A would-be consumer can attempt the order only in slots where the
  hash-mod-N equals zero — probability ≈ 1/N per slot.
- The constraint is purely additive: it lives at output position 4
  (`sellOrder`) or position 3 (`buyOrder`) and the parent order lock allows
  one optional element after its own fixed positions.
- `N` floor 2, ceiling 32 — enforced on the produce side via
  `lessOrEqualThan(u64/2, uint8Bytes($0))` and
  `lessOrEqualThan(uint8Bytes($0), u64/32)`.

## Local-script PoC delivery

Phase-1 ships under `examples/dex/`:

- `examples/dex/dex.easyfl` — sources of `sellOrder`, `buyOrder`,
  `randomizeConsumption`, plus internal helpers.
- `examples/dex/compile.go` — compiles the EasyFL bundle once at start-up,
  exposes the resulting binary and its blake2b hash via package-level
  symbols.
- The order UTXO's lock element (position 2) is
  `callRedeemer(<hash literal>, <fnIdx>, <lock args...>)`.
- The consuming transaction attaches the same binary via `redeemScript(<bin>)`
  exactly once. All sellOrder/buyOrder consumes in that tx reuse the same
  attached script.

Caveats:
- `<hash literal>` and `<fnIdx>` must be inline-data literals per
  `callRedeemer` auditability rules. Lock args (`price`, `timeoutSlots`,
  optionally `amount`) are passed positionally.
- Per-consuming-tx bytes cost = size of the compiled dex script (rough
  estimate ≈ 400–700 bytes for both locks + helpers). Acceptable for a PoC;
  the graduation step eliminates this overhead.
- The script body is identical to what the graduated base-library locks
  would contain. Migrating in phase 2 is a wire change (the lock at
  position 2 becomes `sellOrder(...)` instead of `callRedeemer(...)`), not
  a logic change.

Test plan for the PoC:
- Happy-path sell-order match.
- Happy-path buy-order match.
- Reclaim after timeout (sell and buy).
- Fold-attack rejection (two orders, shared receipt).
- Wrong receipt-output count, wrong sigLock holder, wrong amount, missing
  1-byte literal, wrong literal value — each should fail validation with a
  distinct error message.
- `randomizeConsumption(N)` gating: assert pass/fail across a sweep of
  slots for a fixed holder.

## Followups (out of scope for v1)

- **Browse API.** `proxi dex book <tag>` and a corresponding RPC that
  performs the prefix scan and decodes each UTXO; ditto a "my orders"
  listing keyed off the issuer trie entry.
- **Partial fills.** Recursive receipt: consumer takes `x ≤ amount` and
  emits a smaller `sellOrder` / `buyOrder` UTXO back to the issuer with
  reduced quantity (or reduced deposit + payment) and the same
  price/timeout. Requires an additional produced-output shape rule and
  careful accounting of the storage deposit.
- **Price in the trie key.** Appending an 8-byte big-endian price would
  give a trie-sorted order book (bids descending, asks ascending) for
  free; cost is 8 bytes per entry and freezing the price encoding. Easy to
  add later by extending the entry length to 45.
- **Graduation to base ledger.** Move the two locks into `ledger/def/`,
  register them as standard public symbols, drop the `callRedeemer`
  wrapper.
