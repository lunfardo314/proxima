# returnToSender constraint — spec

## 0. Purpose

`returnToSender(amount)` is an **additive constraint** (not a lock kind)
attached to a `sendWithDeadline` (SWD) output. It forces whoever accepts
the sent tokens to pay `amount` base tokens back to the **master**
(sender) in the same transaction. The master, when reclaiming, is
unaffected.

Two motivating use cases:

1. **Circumvent the storage deposit when sending a small net amount.**
   The master funds the SWD output with a large balance (enough to clear
   the receiver's storage deposit) but pins `returnToSender(large − small)`.
   The receiver can only consume the output by sending the bulk back, so
   the *net* transfer to the receiver is `small`. The receiver is forced
   to return the change or refuse to consume.

2. **Sell native tokens for a fixed price.** The SWD output carries the
   native tokens (`tokenAmount(tag, n)` in a further slot) plus
   `returnToSender(price)`. Any target that lifts the tokens must pay
   `price` base tokens to the master.

`returnToSender` is the deadlined-transfer analogue of the DEX
`buyOrder`/`sellOrder` receipt mechanism, and it **reuses the same
receipt helpers** (see §5).

## 1. Placement and shape

`returnToSender` sits at any free output position ≥ 3 on a UTXO whose
lock (position 2) is `sendWithDeadline`:

```
output[0]   amounts
output[1]   indexValues = [masterID, targetID]   (written by the SWD lock)
output[2]   sendWithDeadline(targetType, acceptanceSlots, cleanupSlots)
output[3]   returnToSender(amount)                <-- this constraint
output[4..] free (e.g. tokenAmount(tag, n) for the fixed-price use case)
```

The SWD lock already caps the output at `selfNumConstraints < 6` and
enforces zero inflation on non-chained outputs, so no extra shape rules
are needed here.

### 1.1 Lock bytecode (the constraint itself)

`returnToSender(amount)` — single arg:

- `$0` `amount` — z-encoded uint64 (≤ 8 bytes, > 0). Minimum base-token
  amount the receiver must return to the master.

## 2. The return receipt

When the target accepts (see §3), the transaction MUST also produce a
**receipt output** paying the master. The receipt has the standard
sigLock shape plus a 1-byte anti-fold literal at position 3:

```
receipt[0]  amounts        base balance ≥ amount
receipt[1]  indexValues    [masterID]              (sigLock holderID)
receipt[2]  sigLock
receipt[3]  inlineData(consumedInputIndex)         anti-fold literal
```

The `returnToSender` constraint reads the receipt's output index from its
own 1-byte unlock parameter (`byte(selfUnlockParameters, 0)`).

### 2.1 Anti-fold defence

Without a binding, one fat receipt could satisfy several consumed
`returnToSender` inputs at once ("folding"). The receipt's position-3
inline literal must equal **this** consumed input's index
(`selfOutputIndex`). A single receipt carries a single literal value, so
it can satisfy exactly one consumed input. Two SWD inputs pointing at the
same receipt fail because the literal cannot equal both input indices.
This mirrors the DEX `receiptLiteralMatchesInputIdx` check exactly.

## 3. Validation

### 3.1 Produced side

```
selfIsProducedOutput  ⇒
  amount > 0
  len(amount) ≤ 8
  the output's own lock at lockConstraintIndex is a sendWithDeadline call
      (selfHasLockType(#sendWithDeadline))
```

The third rule is the "fails in produced context if lock is different"
requirement: `returnToSender` is only valid alongside a `sendWithDeadline`
lock. `selfHasLockType(#sym)` is the existing general helper
`equal(parseBytecode(selfSiblingConstraint(lockConstraintIndex), 0x), $0)`
— the 2-arg `parseBytecode` returns the lock's call prefix, so the
comparison is a real boolean and the friendly error label fires on
mismatch (unlike the 3-arg `parseBytecode(x, 0x, #sym)` form the DEX locks
use, which raises `unexpected call prefix` first). The receipt's
sigLock check (§3.2) likewise uses the boolean form
`equal(parseBytecode(receiptSigLock(K), 0x), #sigLock)`.

### 3.2 Consumed side — signer-based discrimination

The constraint cannot observe which SWD unlock window fired, so it keys
off the transaction signer (the SWD lock independently enforces the
window rules; this constraint only adds the return obligation):

```
selfIsConsumedOutput  ⇒
  OR:
    (a) txHolderID(txSignatureData) == masterID   → noop (true)
    (b) otherwise require ALL of:
        - receipt lock at pos 2 is sigLock
        - receipt holderID (indexValues[0]) == masterID
        - receipt position-3 literal == this input's index   (anti-fold)
        - receipt base balance ≥ amount
```

- **(a) master reclaim / master self-spend.** When the master signs, it
  is reclaiming (or otherwise spending) its own funds — no return is owed.
  `masterID` is read from the consumed output's `selfIndexValue(0)` (the
  SWD lock writes the master there, master-first §4.1 convention).
- **(b) anyone else (target accept, or public cleanup by a third party).**
  Must produce the binding receipt to the master. This is intentionally
  slightly stricter than the SWD lock's public-cleanup window: even a
  cleanup spender returns the funds to the master. In the intended use
  cases `amount ≤ output balance`, so a valid receipt always exists; the
  cleanup window is also far in the future and the master will normally
  have reclaimed already.

## 4. EasyFL error labels

Produced:
- `returnToSender:_amount_must_be_positive`
- `returnToSender:_amount_must_be_at_most_8_bytes`
- `returnToSender:_lock_must_be_sendWithDeadline`

Consumed (branch (b)):
- `returnToSender:_receipt_lock_must_be_sigLock`
- `returnToSender:_receipt_must_pay_master`
- `returnToSender:_receipt_literal_must_equal_input_index`
- `returnToSender:_receipt_underpaid`

## 5. Reused EasyFL helpers

`returnToSender` reuses the receipt accessors already defined for the DEX
order locks in `def/lock_dex_orders.easyfl`. As part of this change those
helpers are **renamed public** (the leading `_` dropped) to mark them as a
shared, reusable receipt-validation API now consumed by three constraints
(`sellOrder`, `buyOrder`, `returnToSender`):

| public name (was `_…`)        | meaning                                            |
|-------------------------------|----------------------------------------------------|
| `receiptNumConstraints($0)`   | tuple length of produced output `$0`               |
| `receiptAmounts($0)`          | amounts constraint of produced output `$0`         |
| `receiptSigLock($0)`          | constraint at pos 2 of produced output `$0`        |
| `receiptLiteral($0)`          | inline data at pos 3 of produced output `$0`       |
| `receiptTokenAmount($0)`      | constraint at pos 4 of produced output `$0`        |
| `receiptHolderID($0)`         | indexValues[0] of produced output `$0`             |
| `receiptBase($0)`             | base-token balance of produced output `$0`         |
| `receiptLiteralMatchesInputIdx($0)` | `parseInlineData(receiptLiteral($0)) == selfOutputIndex` |

EasyFL has no library-private visibility — the underscore was convention
only. `returnToSenderSource` must be listed **after** `lockDexOrdersSource`
in `def_upgrade0.go`'s `IntroduceUpdateManyMulti` batch so these names
resolve. The standalone `examples/dex/dex.easyfl` prototype keeps its own
local `_receipt…` copies (separate callRedeemer scope, unaffected).

## 6. Go API

`returnToSender` is an additive constraint, so — like
`randomizeConsumption` — it needs **no serde registration**, just a
bytecode helper (`ledger/return_to_sender.go`):

```go
const ReturnToSenderName = "returnToSender"

// ReturnToSenderBytecode compiles returnToSender(amount).
func ReturnToSenderBytecode(amount uint64) ([]byte, error)
```

Wallet-side, in `ledger/txbuildercore/helpers_return_to_sender.go`:

```go
func (l *Library[any]) NewReturnToSenderBytecode(amount uint64) ([]byte, error)

// NewReturnReceiptOutput builds the consumer-side receipt output:
// sigLock(master) + inlineData(consumedInputIndex) at pos 3.
func (l *Library[any]) NewReturnReceiptOutput(base uint64, master base.HolderID, consumedInputIndex byte) (*Output, error)
```

## 7. proxi

`proxi node send --deadline --return <amount>` appends
`returnToSender(<amount>)` to the SWD output. Guard: refuse if `<amount>`
is below the minimum storage deposit of the receipt output the consumer
must build (`sigLock(master)` + 1-byte literal), computed wallet-side via
`storageDeposit(u64/<size>)` over `/eval` (mirrors foundry
`computeStorageDeposit`). Otherwise no consumer could ever satisfy the
constraint. `--return` requires `--deadline`.

## 8. Tests (`ledger/tests/return_to_sender_test.go`)

- **Produce happy**: returnToSender on an SWD output settles.
- **Produce reject**: returnToSender on a sigLock (non-SWD) output →
  `returnToSender:_lock_must_be_sendWithDeadline`; zero amount → rejected.
- **Consume — master reclaim (branch a)**: master signs, `Δ ≥
  acceptanceSlots`, no receipt → settles (noop).
- **Consume — target accept (branch b) happy**: target signs, `Δ <
  acceptanceSlots`, valid receipt to master ≥ amount with literal ==
  input idx → settles.
- **Consume — target accept rejections**:
  - no receipt / wrong unlock index → rejected;
  - receipt underpaid (base < amount) → `returnToSender:_receipt_underpaid`;
  - receipt pays wrong holder → `returnToSender:_receipt_must_pay_master`;
  - receipt lock not sigLock → `returnToSender:_receipt_lock_must_be_sigLock`;
  - **fold attack**: two SWD+returnToSender inputs share one receipt →
    `returnToSender:_receipt_literal_must_equal_input_index`.

## 9a. Spendable classification (`proxi node compact` + node filter)

A `returnToSender`-constrained SWD output is NOT claimable by the target
under a plain single-input signature unlock — claiming needs the return
receipt. So the node's `get_outputs?spendable=true` filter and
`proxi node compact` share one classifier:
`txbuildercore.ClassifySpendable(parser, utxoBytes, createSlot, accountHID,
targetSlot) → SpendClass` (singleton-free; `parser` is the minimal
`BytecodeParser` interface — both `*ledger.Library` and
`*txbuildercore.Library[T]` satisfy it). Classes:

- `SpendNotForAccount` — no claim at this slot.
- `SpendSimple` — claimable with a plain signature unlock, no extra output:
  3-element sigLock; SWD master-reclaim (returnToSender is a noop for the
  master); SWD sigLock-target accept with no extras.
- `SpendNeedsReturn` — SWD sigLock-target accept carrying returnToSender:
  claimable only by also producing the return receipt.
- `SpendUnknown` — account has a lock-level claim but the output carries
  unrecognized additional constraints.

Server `isSpendableForAccount` keeps an output iff class `!=
SpendNotForAccount` (so compact still sees the constrained ones). `compact`
consumes only `SpendSimple`; it warns-and-skips `SpendNeedsReturn` (they
become ordinary after the master reclaims — re-run then) and
refuses-and-skips `SpendUnknown` (lists them under `-v`). Tests:
`ledger/tests/spendable_classify_test.go`.

## 9. Migration / interop

Breaking ledger change (new constraint + DEX helper rename ⇒ new
`LibraryHash`). No backward compatibility with prior libraries; this is
intended as the last breaking change before testnet deployment.
