# delegationParams cross-chain transition bug — investigation handoff

## Verified facts (not speculation)

### What the server actually reports

From `proxi node dlg chain 842099a962e2...` failure:

```
constraint 'delegationParams' failed with error
'panic: evalAtPath: path=[0 8 0 6] -> Tuple.At(6): index is out of range. Num elements: 5'.
Path: @.consumed.[0].out[0].constraint[6]
```

`Path: @.consumed.[0].out[0].constraint[6]` is the **constraint that is
running** — the consumed chain-origin output's `delegationParams` at
slot 6. Its body trips on `evalAtPath([0 8 0 6])` — i.e., **the produced
successor's** TransactionTuple(0) → TxOutputs(8) → output[0] →
constraint[6]. The produced output has 5 elements (0..4), so slot 6 is
out of range.

### The consumed UTXO's actual layout (from server-side display)

```
   0: amounts = (6_000_000_000)
   1: index values: [<wallet holderID>]
   2: sigLock
   3: chain(ORIGIN, ...)
   4: bytecode= (unexpected EOF)
   5: bytecode= (unexpected EOF)
   6: delegationParams(epochSlots=600, maxFrozenEpochs=20)
```

This chain has `delegationParams` at slot 6. It is NOT a sequencer
chain (slots 4 and 5 are empty placeholders, not `sequencer` /
`seqMilestoneData`).

### The produced delegation output's actual layout

From wallet-side display:

```
[0] amounts = (6_000_000_196, inflation: 197)
[1] index values: [<master>, <target>]
[2] delegateLock(0x,0x0384,0x0258,20)
[3] chain(<chainID>, 0, 165, 197, 0x, 1, 0x)
[4] delegateLockState(0x,0)
```

5 constraints; `delegateLockState` at the last position (index 4).

### What the code enforces

- **`delegateLockState_must_occupy_the_last_tuple_position`** —
  `ledger/def/lock_delegate.easyfl:107`:
  ```
  require(equal(selfBlockIndex, _selfLastConstraintIndex),
          !!!delegateLockState_must_occupy_the_last_tuple_position)
  ```
  So on a delegation output, `delegateLockState` MUST be last.
  → The produced output cannot place anything after `delegateLockState`.

- **`delegationParams` is pinned to slot 6 and immutable across transits** —
  `ledger/def/delegation_params.easyfl:28-52`:
  ```
  func delegationParams :
  and(
     selfImmutableOnSuccessorIndex(delegationParamsConstraintIndex),
     ...
  )
  ```
  `selfImmutableOnSuccessorIndex` walks into the chain successor's
  constraint at the same index. With `delegationParamsConstraintIndex =
  6` and the produced output having only 5 elements → "Tuple.At(6):
  index is out of range".

- **`mkchain` defaults `--accept-delegations` to `false`** —
  `proxi/node_cmd/mkchain.go:32`:
  ```
  addDelegationParamsFlags(makeChainCmd, false /* default: opt-out for regular chains */)
  ```
  → A bare `proxi node mkchain` does NOT attach delegationParams. The
  failing chain must have been created with `--accept-delegations` set
  explicitly (or `proxi node setup_seq` was used, which defaults the
  flag to `true`).

### So what is actually broken

The chain `842099a962e2...` is a NON-sequencer chain that was tagged
with `delegationParams` at origin (advertising "I can be a delegation
target"). The user is now trying to delegate THIS chain to a
sequencer — i.e., reuse it as a delegation SOURCE.

The produced delegation output (delegateLock + chain + delegateLockState)
cannot carry `delegationParams` forward, because:
- delegateLockState must be last, and
- there's no semantic place for delegationParams on a delegation
  output anyway — the chain is no longer a delegation target.

But the consumed chain's `delegationParams` constraint enforces
*immutability across transit* via `selfImmutableOnSuccessorIndex`. The
constraint cannot distinguish "transit to another regular chain step"
from "transit to a delegation-source step". Result: any attempt to
delegate a chain that carries `delegationParams` panics with the
above error.

## Two interlocking design issues

**(a) `delegationParams` makes no semantic sense on a non-sequencer
chain.** It declares the chain as a delegation TARGET, but only
sequencer chains can actually be targets (a non-sequencer chain has no
inflation to share). Today nothing prevents attaching delegationParams
to a non-sequencer chain (mkchain --accept-delegations on a regular
chain is silently accepted).

**(b) `delegationParams` self-immutability doesn't allow for a
delegation-source transit.** Even if delegationParams were correctly
attached only to sequencer chains, you couldn't ever convert such a
chain back to a non-target form — selfImmutableOnSuccessorIndex would
require carrying delegationParams across every transit. For the
specific case of a CHAIN that DELEGATES to ANOTHER chain, that's a
distinct role from "I accept delegations"; the constraint shape needs
to either allow the source-role transit or refuse to be attached to
chains that might one day become sources.

## User's design direction (from this session)

> "delegateLockState [is] expected last in the utxo, on delegated utxo"

Confirmed at `lock_delegate.easyfl:107`.

> "I suspect delegationParams appears on the delegated output that was
> never meant to be a delegation target."

Confirmed: the consumed chain has delegationParams but is not a
sequencer chain.

> "Probably default when creating chain, delegation params absent."

Confirmed: `mkchain.go:32` defaults to `false`.

> "Also, consider constraining delegationParams to sequencer chains
> only. To enforce that, we may require index of the 'sequencer'
> constraint as one of args of delegationParams. In general,
> delegationParams should enforce valid environment by itself."

This is the proposed architectural fix.

## Plan for the next session

1. **Reproduce in a ledger-level test.**
   - Build a chain origin with `delegationParams` attached (the way
     `mkchain --accept-delegations` would build it) but without a
     `sequencer` constraint.
   - Build a delegation tx that consumes that chain and produces a
     delegation output with `delegateLockState` at the last position
     and no constraint at slot 6.
   - Submit through `ValidateFullContext` and confirm the same
     `Tuple.At(6): index is out of range. Num elements: 5` panic.

2. **Propose the fix** (architectural, per user direction):
   - Add a `sequencer` constraint index arg to `delegationParams`:
     `delegationParams(epochSlots, maxFrozenEpochs, seqConstraintIdx)`.
   - In the constraint body, parse the sibling at `seqConstraintIdx`
     and require it to be the `sequencer` constraint (via
     `parseBytecode(..., #sequencer)` or equivalent). Reject if not.
   - With this enforced, `mkchain --accept-delegations` on a regular
     chain becomes structurally impossible: the chain origin would
     fail validation at construction time because no sequencer
     constraint is present.

3. **Decide on the immutability semantics** for the legitimate
   sequencer-chain case. Options:
   - Keep `selfImmutableOnSuccessorIndex` but also enforce sequencer
     presence on every transit — then any tx that drops the sequencer
     constraint must also drop delegationParams (consistent state).
   - Move to a softer "immutable on TRANSITION between sequencer-chain
     steps; absent on non-sequencer transits" rule. Likely more
     complex.

4. **Wallet-side mitigation in the meantime.**
   - At `mkchain`, refuse `--accept-delegations` when not also setting
     up a sequencer (or warn loudly).
   - At `proxi node dlg chain`, pre-check the source chain for
     delegationParams presence and reject with a clear error before
     submitting the doomed tx.

## Useful pointers for the next session

- The constraint code:
  - `ledger/def/delegation_params.easyfl` — the constraint to extend.
  - `ledger/def/lock_delegate.easyfl:53` — `_selfLastConstraintIndex`
    helper (already does `selfNumConstraints - 1`); the same pattern
    can be used in `delegationParams` to find the sequencer
    constraint instead of taking the index as an arg.
  - `ledger/def/sequencer.easyfl` — definition of `sequencer`
    constraint and `SeqMilestoneDataFixedIndex` for the milestone-data
    sibling.

- Wallet compose:
  - `proxi/node_cmd/mkchain.go:32,52-58` — default flag, --accept-delegations wiring.
  - `proxi/node_cmd/setup_seq.go` — sequencer-setup path that defaults
    delegationParams to `true`.
  - `proxi/node_cmd/delegate/chain.go` — the delegate-chain submit path
    where the failure originates.

- Display path is now wallet-side and singleton-free (commit
  `03d73bdb`); failures will render full consumed-UTXO context for the
  failing tx, making reproduction easy.

## Don't do in the next session

- The wider proxi singleton sweep — already complete in `03d73bdb`.
- The mineable-branch-inflation work — reverted on develop08 (see
  the `project_branch_inflation_mining.md` memory).
