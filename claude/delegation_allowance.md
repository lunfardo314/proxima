# Delegation allowance — spec

Status: **implemented.** Constraints in `ledger/def/{ensure,lock_delegate}.easyfl`,
sequencer side in `sequencer/txbuilder_seq/req_askstop.go`, wallet side in
`proxi/node_cmd/delegate/askstop.go`. Tests in
`ledger/tests/delegate_test.go` (section 4) and the end-to-end path in
`tests/txbuilder_seq_test.go`.

## Problem

`delegateLock` enforces strict non-decrease of the delegated balance
(`lock_delegate.easyfl:323`, `!!!delegated_amount_should_not_decrease`).
Chosen for simplicity and auditability. The consequence is that the `askstop`
compensation must be paid from *outside* the delegation: it is taken from the
tag-along output's own balance (`req_askstop.go:95-102`), so a delegator has
to keep liquid tokens in a sigLock account sized to the projected inflation of
their frozen delegation. For a large delegation that is a significant idle
balance — and it is the only remaining reason to hold idle tokens (tag-along
fees themselves are small).

## Design

Add an **allowance** argument to the `ensureStopDelegation` constraint: a
delegator-signed authorisation, riding on the tag-along command output, that
lets the sequencer take up to that amount out of the delegation balance as
askstop compensation.

```
ensureStopDelegation(delegationID, allowance)
```

`allowance == 0` reproduces today's behaviour bit-for-bit. Everything else
about askstop is unchanged — the delegation still ends in `on hold`.

There is deliberately **no withdraw command**. To take funds out, the
delegator stops the delegation and then does as they like with the on-hold
output (today: `killchain`, then re-delegate a smaller amount). More proxi
commands for chained-account manipulation may follow for UX, but are not part
of this.

Scope: outside the frozen period the master already spends the delegation
output directly (`_masterUnlockedConsumed`, `lock_delegate.easyfl:279-284`,
which requires `not(_consumedIsFrozenInTx)`). The allowance only matters
mid-freeze.

## What already exists (facts)

- Tag-along command output, max 5 elements (`lock_tag_along.easyfl:25`,
  `parse.go:46`): `[0]` amounts, `[1]` index-values, `[2]` tagAlong lock,
  `[3]` request data (inline `smallkv`), `[4]` optional constraint.
  **Index 4 is the only free slot** — the allowance must extend the
  constraint already living there, not add an element.
- **Sender authorisation is already unforgeable.** `tagAlong` enforces on the
  produced side `equal($1, txHolderID(txSignatureData))`
  (`lock_tag_along.easyfl:26-29`), so the senderID in the index-value tuple is
  bound to the actual signer of the creating transaction. Comparing it to the
  delegation's masterID (`selfIndexValue(0)`) is a complete authorisation
  check. **No new credential machinery is needed.**
- `ensureStopDelegation(delegationID)` (`ensure.easyfl`) sits at index 4 and,
  on consumption, requires the produced output named by
  `selfUnlockParameters` to be a delegation with that chain ID in state
  `on hold`. It is the delegator's guarantee that the sequencer really did
  what was asked. It is currently optional (`req_askstop.go:104`).
- Delegate lock unlock params are exactly 2 bytes
  (`lock_delegate.easyfl:334-337`); byte 1 == `0xff` means master-unlock,
  otherwise target-unlock.
- Today's askstop sits exactly on the non-decrease boundary:
  `MakeDelegationRevokeOutput` produces
  `balance + inflation - harvestInflation` and `req_askstop.go:131` passes
  `HarvestInflation = inflation`, so the successor balance equals the
  predecessor balance. The gate at `:323` is precisely the thing to relax and
  nothing else moves.
- Frozen-coverage accounting needs **no change**. The sequencer chain enforces
  `pred_i + sum_i = 2*succ_i` (`chain.go:295-303`), and for an `on hold`
  successor the expected vector is
  `dOutPred.MakeFrozenCoverageAmountDeltasForRevoking(txTs)`
  (`lock_delegate.go:362`) — computed from the *predecessor's* vector, so it
  is independent of the successor's balance.

## Ledger changes

### 1. `ensureStopDelegation` gains an argument and a ceiling

`ledger/ensure.go` (`EnsureStopDelegation` type) and
`ledger/def/ensure.easyfl`. The existing produced-side check is unchanged —
delegation chain ID correct, state `on hold`. Two additions:

- the allowance argument, which the delegate lock reads (below);
- a ceiling: **`allowance <= projected inflation advance`**, so the delegator
  is protected from an oversized allowance regardless of what their wallet
  computed.

The ceiling is skipped entirely when `allowance == 0`, so the legacy path
pays nothing for it.

The ceiling needs the *consumed* delegation, reachable from the produced one:

1. produced delegation output ← `selfUnlockParameters`;
2. its chain constraint arg 1 → predecessor input index;
3. consumed output at that index → token balance, `delegateLockState`
   (`lastFrozenEpoch`), and the lock's `epochSlots` / target chain ID;
4. `lastSlotInDelegationEpoch(target, lastFrozenEpoch, epochSlots)` −
   `slotOfInputByIndex` → remaining frozen slots;
5. `chainInflationMultiStep(balance, inputSlot, remaining)` → the ceiling.

**Anchor it to the delegation output's own slot, not `txSlot`.** The
sequencer's compensation is the same quantity measured from `txSlot`, and it
shrinks as the frozen span runs out. Anchoring the ceiling there would make
it drift between the moment the delegator signs the request and the moment
the sequencer picks it up out of the tag-along window, so an honest request
sized at signing time would be rejected at execution time. Measured from the
input's slot it is a fixed property of an immutable output — wallet and
constraint compute the identical number — and it still bounds the true
compensation from above at any later slot, which is all a ceiling needs.
`DelegationOutput.AllowanceCeiling()` is the Go mirror, used by both the
sequencer's pre-check and the wallet.

**Use the uncut `chainInflationMultiStep`, not `requiredInflationAdvance`.**
The two differ: `requiredInflationAdvance` (`lock_delegate.easyfl:157`)
applies the promille cut and is what the sequencer prepays when *freezing*;
askstop's compensation (`req_askstop.go:95`) is the sequencer's full loss and
carries no cut. Capping at the cut version would put the ceiling far below
the real compensation and break askstop outright.

Two arithmetic details that only surface on implementation: the remaining
span is guarded by a comparison, because an unfrozen delegation carries
`lastFrozenEpoch` 0 and would otherwise underflow the subtraction; and that
comparison needs `uint8Bytes` on the slot, since `lessThan` requires operands
of equal width and epoch arithmetic yields 8 bytes against a 4-byte slot.

The sequencer keeps the tag-along fee on top of whatever it takes under the
allowance, so the delegator pays fee + compensation. That is correct — the
fee is owed for processing the command.

### 2. Delegate lock reads it

Unlock params become 2 **or** 3 bytes (relax
`lock_delegate.easyfl:334-337`). Third byte = index of the consumed
allowance-bearing output; absent = current behaviour. The third byte must be
rejected on the master path (byte 1 == `0xff`) — the allowance is
target-only.

The non-decrease gate at `lock_delegate.easyfl:323` becomes:

```
if( lessOrEqualThan(selfTokenBalanceValue, _amountOnSuccessor),
    true,                              // no decrease — unchanged path
    _decreaseWithinAllowance )         // new
```

The `if` form matters: `sub(selfTokenBalanceValue, _amountOnSuccessor)` must
only be evaluated on the branch where it cannot underflow.

`_decreaseWithinAllowance`, given consumed index `k` from unlock byte 2:

1. `parseBytecode(consumedConstraintByIndex(k, 4), 0x, #ensureStopDelegation)`
   — a panic here is the reject path (same idiom as `_validStructureProduced`,
   `lock_delegate.easyfl:248`).
2. arg 0 == this delegation's chain ID
   (`parseInlineDataArgument(selfSiblingConstraint(chainConstraintIndex), 0, #chain)`).
3. output `k`'s lock parses as `#tagAlong` and its index-value position 0
   (senderID) equals `selfIndexValue(0)` (masterID). This is the
   authorisation.
4. `sub(selfTokenBalanceValue, _amountOnSuccessor) <= ` arg 1.

Steps 1–3 make the allowance unforgeable; step 4 is the relaxation.

## Sequencer side

`req_askstop.go` only. No new command code, no `parse.go` entry.

- `parseAskStopDelegationOutput`: the structural check at `:104-113` already
  requires the constraint at index 4 with a matching chain ID — extend it to
  read the allowance. The compensation test at `:96` becomes

  ```
  neededCompensation <= tagAlongOutput.TokenBalance() + allowance
  ```

  Sender/master authorisation (`:71`), target authorisation (`:65`), frozen
  check (`:58`) and patience margin (`:88`) are untouched.
- `Apply`: `MakeDelegationRevokeOutput` gains a `TakeFromBalance` parameter
  subtracted from the produced on-hold balance. `AttachmentCostDelta` is
  unchanged (still 3) — no extra output.
- `TakeFromBalance` is the **whole allowance**, not the shortfall — the
  sequencer is greedy, exactly as it is with an oversized tag-along fee. The
  ceiling in the constraint is what bounds the delegator's exposure.

## Who computes what

**The allowance is a price the delegator offers, and the sequencer takes it
all.** The lock only enforces `decrease <= allowance`; nothing obliges the
sequencer to take less. That is the same contract as the tag-along fee today
— an oversized fee is simply kept — and it stays the wallet's job to compute
the minimal viable cost.

The wallet splits the cost in two:

- **tag-along output balance** — the ordinary fee for processing the command
  (sequencer minimum + config margin), exactly as for any other command;
- **allowance** — the askstop compensation, i.e. the sequencer's forgone
  projected inflation.

The user must be shown enough to check this (delegation balance, unfreeze
slot, projected compensation), but the computation belongs in the wallet.
The wallet is not trusted for the upper bound — `ensureStopDelegation`
enforces the ceiling itself (above).

Storage deposit is not a concern: the allowance is capped at the projected
inflation advance, which is far below the delegation balance, so the on-hold
output cannot be pushed under the minimum.

## Wallet / CLI

No flag. `proxi node delegate askstop` derives the split itself: the request
output carries the ordinary `glb.GetTagAlongFee()`, and whatever compensation
that does not cover becomes the allowance. The user is shown the delegation
balance, the estimated compensation, and the two components, then prompted
before authorising anything to be taken out of the delegation.

- `ledger/txbuildercore/helpers_seq.go`: `NewEnsureStopDelegationConstraint`
  gains the argument.
- `proxi/glb/display_chains.go`: showing the allowance on delegation rows is
  still to do.

## Security checklist

- Allowance is authorised only by `tagAlong` senderID == delegation masterID,
  signature-bound at creation (`lock_tag_along.easyfl:26-29`).
- The constraint must be pinned at index 4 and must name the delegation chain
  ID, so an allowance for delegation X cannot be replayed against delegation Y
  in the same transaction.
- One tag-along output authorises one transaction — it is consumed.
- Third unlock byte rejected on the master path.
- Absent the third unlock byte, behaviour is bit-identical to today.

## Fixed separately: the subtraction in `_validInflationAdvanceProduced`

The function itself is essential — it is the prepayment rule, evaluated on
**every freeze** via the frozen branch of `_validLimitsProduced`. Only the
askstop path skips it, because that path produces an `on hold` output and
takes the other branch.

The hazard was arithmetic: `lessOrEqualThan(_requiredInflationAdvance(...),
sub(selfTokenBalanceValue, predecessorTokenBalance))` subtracts on uint64, so
a frozen successor holding less than its predecessor would underflow to a
huge value and pass the check vacuously.

It could not happen, but only by an indirect argument:

- on the **target** path, the non-decrease gate (`:323`) guarantees
  `successor >= predecessor`;
- on the **master** path there is no such gate, but a frozen successor is
  impossible anyway: `evalEnforceFrozenCoverageOnDelegateOutput`
  (`lock_delegate.go:347-351`) forces all-zero frozen coverage when the
  predecessor was master-unlocked, while a frozen successor needs at least
  one non-zero epoch cell.

Since this change relaxes the very gate the first argument rests on, the
subtraction is now guarded explicitly by `lessOrEqualThan($1,
selfTokenBalanceValue)` — a decreased balance cannot have prepaid the
advance, so rejecting it is also the correct answer. The `and` is safe
regardless of evaluation order: an underflowed difference cannot rescue a
false first conjunct.

## Breaking

Any change to a constraint body changes the library hash — this is a
hardfork. Bundle with the fairlaunch break if timing allows.

## Resolved during implementation

**The accessors were all available in consumed-output form.** The ceiling
reads the consumed delegation's `delegateLockState` at its last tuple
position plus its lock arguments; the `tupleLenAtPath` pattern used by
`_successorLastConstraintIndex` generalises to an arbitrary consumed index,
and index values come out of `atTuple8(atPath(...))`. No embedded Go function
was needed — the whole ceiling is EasyFL (`_projectedCompensation` in
`ensure.easyfl`).

Neither UTXO size nor evaluation cost is an issue: the lock element carries
only the call, function bodies live in the shared library, and the delegate
lock already evaluates `chainInflationMultiStep` on every freeze — a far more
frequent path than askstop.

## Tests

`ledger/tests/delegate_test.go` builds the request output in its
own master-signed transaction, which is what makes the sender binding real
rather than assumed. Covered: the compensation actually leaving the
delegation balance, a partial take, and seven rejections — take above the
allowance, allowance above the ceiling, forged sender, allowance naming a
different delegation, missing third unlock byte, third byte on the master
path, and zero allowance still forbidding any decrease. Each fails on its own
distinct constraint error rather than incidentally.

`tests/txbuilder_seq_test.go` grants an allowance to half the scheduled
askstop requests, driving the real sequencer build path
(`AddTagAlongInput` → `parseAskStopDelegationOutput` → `Apply`) and asserting
the balance actually dropped by the granted amount. That assertion matters:
the test's revoked-count checks are commented out, so silently rejected
requests would otherwise go unnoticed.
