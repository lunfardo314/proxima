# Per-target-chain delegation epoch parameters

## Status

**Deferred.** Spec is captured for the record but implementation is on hold.

Current thinking: bumping the two global EasyFL constants may be enough for the foreseeable need. If we do go per-target later, the minimal version is to move only `maxFrozenEpochs` (the vector-size dimension) and leave `epochSlots` global — that halves the surface area and avoids touching epoch math. The full spec below assumes both move; trim as needed when revisited.

## Context: how delegation params work today

Three parameters drive delegation timing on this ledger:

| EasyFL constant                       | Go field                  | Value | Role |
|---------------------------------------|---------------------------|-------|------|
| `constDelegationEpochSlots`           | `DelegationEpochSlots`    | 700   | length of a delegation epoch in slots (≈ 2 h) |
| `constDelegationMaxFrozenEpochs`      | `MaxFrozenEpochs`         | 10    | max simultaneous frozen epochs (≈ 20 h horizon) |
| `constDelegationSafeRevocationSlots`  | `SafeRevocationSlots`     | 60    | slots after a frozen epoch ends during which the delegator can revoke without sequencer competition |

All three are global EasyFL constants in `ledger/def/lock_delegate.easyfl:11-13`, surfaced to Go via `Constants` (`ledger/constants.go:51-55`). They are baked at ledger initialisation and cannot vary per-chain.

`constDelegationSafeRevocationSlots` is a **network-wide UX guarantee** about how long the delegator gets to revoke after each frozen epoch — there is no reason for it to differ per chain. It stays a global constant.

`constDelegationEpochSlots` and `constDelegationMaxFrozenEpochs` are different: they govern the cadence and depth of a particular chain's freeze accounting. Different targets reasonably want different values.

## Goal

Move `constDelegationEpochSlots` and `constDelegationMaxFrozenEpochs` off the global library and onto the **target sequencer chain**. Each sequencer chain advertises its own `(epochSlots, maxFrozenEpochs)` pair as part of its chain output. Ledger-enforced lower/upper bounds prevent misconfiguration.

`constDelegationSafeRevocationSlots` stays a global constant.

## Invariant: target chain's params are immutable for the chain's lifetime

`epochSlots` and `maxFrozenEpochs` **must be fixed for the chain's lifetime**. Reason: the target chain carries a frozen-coverage vector whose size is `maxFrozenEpochs`, and *every* delegation contributing to that vector must use the same `epochSlots` math so the contributions land in the right cell. Changing either after the fact would corrupt the accounting on the target chain itself.

This is enforced by EasyFL: the new `delegationParams` constraint AND-s a self-immutability check across every chain transit, using the universal `selfImmutableOnSuccessorIndex(...)` helper already shipped in `chain.easyfl`. Mechanically the same as how `foundryNonDestructible` and `foundryMaxSupply` self-lock today.

## Placement: `delegationParams` sibling constraint at index 6

A new typed constraint at `ConstraintIndexDelegationParams = 6` (the next reserved tuple position after `foundryPolicy = 5`):

```
delegationParams(epochSlots, maxFrozenEpochs)
```

Lives on chain outputs that opt in to accepting delegations. Foundries, plain chains, and non-delegating sequencers don't carry it — no bytes overhead on unrelated outputs. `chain()`'s arity stays at 7.

A chain output WITHOUT `delegationParams` at index 6 simply cannot be a delegation target: any `delegateLock` referencing it via `Target` is rejected.

## Bounds

Two new EasyFL ledger constants define the *bounds* the target chain may choose within:

```
constDelegationEpochSlotsMin           // 500
constDelegationEpochSlotsMax           // 2000
constDelegationMaxFrozenEpochsMin      // 8
constDelegationMaxFrozenEpochsMax      // 32
```

The current values (700 epoch slots, 10 max frozen epochs) sit inside these bounds and become the **defaults** in the user-facing layer.

The `delegationParams(...)` constraint's EasyFL body enforces both args sit within these bounds. Anything outside gets rejected at output validation time.

The delegator's `delegateLock(maxFrozenEpochs, inflationShare)` arg (the *delegator's* chosen depth) is bounded above by the target's `delegationParams.maxFrozenEpochs` (not the global max), and below by 1 — bigger value = less frequent forced-withdrawal drops in total coverage.

## How does a delegation see the target's params

The target's `(epochSlots, maxFrozenEpochs)` are copied into the delegateLock at delegation origin. The delegateLock body grows from 2 args to 4:

```
delegateLock(maxFrozenEpochs, inflationShare, epochSlots, targetMaxFrozenEpochs)
```

- `$0 maxFrozenEpochs` — delegator's chosen depth (1 ≤ $0 ≤ $3)
- `$1 inflationShare` — required inflation share in promille (0..1000), unchanged
- `$2 epochSlots` — **copy of** target's `delegationParams.epochSlots`
- `$3 targetMaxFrozenEpochs` — **copy of** target's `delegationParams.maxFrozenEpochs`

Cross-check at delegation **origin** only: the origin tx must include the target chain output (as a consumed input or via endorsement). The delegateLock's `$2` and `$3` must equal the target's `delegationParams` values. EasyFL enforces this in the delegateLock's body when `_selfIsDelegationOrigin` is true.

After origin: the delegateLock's inline `$2` and `$3` are the authoritative source for all subsequent epoch math (`lastSlotInDelegationEpoch`, `_delegationEpochFromSlot`, safe-revocation window). No further lookups needed. Inflation transits, revocations, on-hold transitions — none require the target chain to be present in the tx.

Soundness: this copy can never go stale because the target's `delegationParams` is immutable by ledger rule (`selfImmutableOnSuccessorIndex` on the target's `delegationParams` constraint). The cost is 2 additional inline-data args per delegation UTXO — at most 4 bytes (z-encoded u32) + 1 byte (max frozen epochs ≤ 32) plus EasyFL data-prefix overhead, persistent for the delegation's lifetime.

The existing delegation logic stays the same shape — `_delegationEpochFromSlot`, `lastSlotInDelegationEpoch`, the safe-revocation window check, etc., just substitute the values' *source* from `constDelegationEpochSlots` / `constDelegationMaxFrozenEpochs` to the delegateLock's `$2` / `$3`.

## Target & lock immutability across transit (already enforced)

The delegateLock's target chain ID must be immutable across the delegation's lifetime — otherwise an attacker could transit the delegation to point at a different target whose `delegationParams` differ from the inline `$2`/`$3` copies, silently breaking the epoch math.

This is **already enforced** by the existing delegateLock body when the target spends the delegation for inflation (`_targetUnlockedConsumed`, `lock_delegate.easyfl:252-254`):

```easyfl
// delegation lock at the lock element must match exactly
require(equal(successorConstraint(lockConstraintIndex), selfSiblingConstraint(lockConstraintIndex)),
        !!!delegation_lock_on_successor_must_be_exactly_the_same),
// and the index-value tuple (master, target) must match exactly too
require(equal(successorConstraint(indexValuesConstraintIndex), selfSiblingConstraint(indexValuesConstraintIndex)),
        !!!delegation_index_values_on_successor_must_be_exactly_the_same)
```

- The index-values check pins **master** and **target** byte-equal on the successor.
- The lock-element check pins the **entire delegateLock bytecode** byte-equal on the successor — so when we extend the lock from 2 to 4 args, the two new args (`$2 epochSlots`, `$3 targetMaxFrozenEpochs`) inherit this immutability automatically. No new EasyFL rule needed.

On the **master-unlock** path (`_masterUnlockedConsumed`) — revocation — there is no successor delegation; master takes the recovered tokens back as a sigLock (or anywhere else). No transit, no immutability concern.

So: once a delegation is born with the four-arg lock and a target, neither the target nor the inline params can ever change. The origin cross-check is the only window in which they could be wrong; after that they're fixed forever.

## Chain creation flow — attaching `delegationParams` at origin

`delegationParams` can only be attached **at chain origin** (because immutability rejects any transit that changes index 6). To create a chain that can act as a delegation target:

- `proxi node mkchain <amount>` — extended with the three new flags:
  ```
  --delegation-epoch-slots N        (default 700, bounds 500..2000)
  --delegation-max-frozen-epochs N  (default 10, bounds 8..32)
  --no-delegations                  (omit delegationParams entirely)
  ```
  When the flags are present (or by default if we choose to make `delegationParams` default-on), the produced chain-origin output carries `delegationParams(epochSlots, maxFrozenEpochs)` at index 6. The `MakeChainOrigin` flow (`api/client/client.go` and `proxi/node_cmd/mkchain.go`) needs the analogous parameter pass-through.

- `proxi node seq setup` (the sequencer-bootstrap entry point in `proxi/node_cmd/setup_seq.go`) — needs the same flags. Sequencers will be the primary class of chain that accepts delegations, so this is the most-used path. The default is to attach `delegationParams` with the default values; pass `--no-delegations` to create a sequencer chain that opts out.

A chain origin without `delegationParams` cannot ever become a delegation target — there's no path to add the constraint later (`chain` transit can't introduce new constraints at a previously empty index without violating the chain successor invariants). The owner has to retire the chain and create a fresh one with `delegationParams` from day 1.

Foundries are chain outputs too, but they have their own per-position layout (`foundry` at 4, `foundryPolicy` at 5). Nothing prevents a foundry from also being a delegation target by carrying `delegationParams` at index 6 — uncommon but not blocked.

## Defaults — proxi user-facing side

`proxi node delegate amount` (existing):

| Flag                | Default behaviour |
|---------------------|-------------------|
| `--target / -q`     | required                                            |
| `--epochs / -e N`   | 0 → take target's `delegationParams.maxFrozenEpochs`; non-zero → cap at target's value |
| `--share`           | unchanged (current 900 default) |

Sequencer setup (`proxi node seq setup` / `make_chain` flow) — new flags:

```
--delegation-epoch-slots N           (default 700, bounds 500..2000)
--delegation-max-frozen-epochs N     (default 10, bounds 8..32)
--no-delegations                     (omit delegationParams; chain cannot be a delegation target)
```

Updating `delegationParams` on a live chain is **forbidden** — `selfImmutableOnSuccessorIndex` on the constraint rejects any transit that changes those bytes. To "change policy" the sequencer would have to retire and create a new chain.

## Files to change

EasyFL:
- `ledger/def/lock_delegate.easyfl` — drop the two `constDelegationXxx` constants moving to per-target; rewire `delegationEpochOffset`, `lastSlotInDelegationEpoch`, `_delegationEpochFromSlot`, `__consumedIsInTheSafeRevocationWindowTx` to read `$2` (epochSlots) and `$3` (targetMaxFrozenEpochs) from the delegateLock body instead of the global constants; keep `constDelegationSafeRevocationSlots` global. Extend `delegateLock` body to 4 args; AND in a cross-check that runs only at origin (`_selfIsDelegationOrigin == true`) verifying the inline `$2`/`$3` equal the target chain's `delegationParams` values fetched via `parseInlineDataArgument(...)` on the target output present in the tx.
- (new) `ledger/def/delegation_params.easyfl` — defines `delegationParams(epochSlots, maxFrozenEpochs)`. AND-s `selfImmutableOnSuccessorIndex(delegationParamsConstraintIndex)`. Enforces bounds `[DelegationEpochSlotsMin, DelegationEpochSlotsMax]` and `[DelegationMaxFrozenEpochsMin, DelegationMaxFrozenEpochsMax]` in its body.

Go:
- `ledger/constants.go` — drop `DelegationEpochSlots`, `MaxFrozenEpochs`; add `DelegationEpochSlotsMin/Max`, `DelegationMaxFrozenEpochsMin/Max`. Keep `SafeRevocationSlots`.
- `ledger/lock_delegate.go` — `DelegateLock` struct grows two fields (`EpochSlots uint32`, `TargetMaxFrozenEpochs byte`); `NewDelegateLock(...)` signature gains the two new params; serialiser/deserialiser updated for 4 args (z32 + z8 added).
- (new) `ledger/delegation_params.go` — typed wrapper for the new constraint (parallels `ledger/foundry.go`): `DelegationParams{EpochSlots uint32, MaxFrozenEpochs byte}`, `NewDelegationParams(...)`, `DelegationParamsFromBytes(...)`, `registerDelegationParams(lib)`, inline test.
- `ledger/def_constants_path0.go` — add `ConstraintIndexDelegationParams = 6`.
- `ledger/lock_delegate_util.go` — `MakeDelegateInitOutputParams` gains `EpochSlots` and `TargetMaxFrozenEpochs`; pipe through `MakeDelegationInitOutput`. `Amounts.FrozenCoverageVector(maxFrozenEpochs)` is already parametric — call sites switch from `lib.MaxFrozenEpochs` to the delegation's own `TargetMaxFrozenEpochs`. Same for `RequiredMinimumInflationAdvance` and all places that today read `lib.DelegationEpochSlots` / `lib.MaxFrozenEpochs`.
- `ledger/amounts.go` — no signature change.
- (test) `ledger/tests/claude_delegation_test.go` — adjust to new param flow; add tests exercising per-target params, bounds enforcement, the origin cross-check (success + failure cases), and immutability of `delegationParams` across chain transit.

Proxi:
- `proxi/node_cmd/delegate/amount.go` — fetch target's `delegationParams` via `GetChainOutput(targetSeqID)` (already done; just also read the constraint at index 6), inline into the produced delegateLock body, and add the target chain as an endorsement or input on the origin tx so the cross-check passes. Bound `--epochs / -e` by the target's `MaxFrozenEpochs` instead of `lib.MaxFrozenEpochs`.
- Sequencer chain origin flow (`proxi node make_chain` / `proxi node seq setup`) — new flags:
  - `--delegation-epoch-slots N` (default 700, bounds 500..2000)
  - `--delegation-max-frozen-epochs N` (default 10, bounds 8..32)
  - `--no-delegations` (omit the `delegationParams` constraint entirely → chain cannot be a delegation target)
- `proxi/node_cmd/balance.go`, `chain.go` — surface the target's `delegationParams(epochSlots, maxFrozenEpochs)` in inspection output (similar to foundry policy display); `proxi node chain <seqChainID>` shows the values when the constraint is attached.

## Backward compatibility

Per memory note, `develop08` has many breaking ledger changes already. This change goes in the same bucket — no migration shim. Existing testnet state has to be regenerated, same as for prior phases.
