# delegationParams design flaw + setup_seq default-attach bug — investigation handoff

## What actually happened (verified)

### The proxi setup_seq default

`proxi node setup_seq <name> <amount>` calls
`addDelegationParamsFlags(seqSendCmd, true)` at
`proxi/node_cmd/setup_seq.go:34` — **default is `--accept-delegations =
true`** for setup_seq (unlike `mkchain` which defaults to false at
`mkchain.go:32`). Both share the same package-level flag variable
`flagAcceptDelegations`.

When the user runs `proxi node setup_seq <name> <amount>`, `MakeChain`
is called at `setup_seq.go:61`. Inside `MakeChain`,
`resolveChainOriginDelegationParams()` reads `flagAcceptDelegations`
(= `true` from the default) and produces a chain-origin output that
**carries delegationParams from the very first commit** — without the
user explicitly setting any flag.

### Server-side display of the consumed UTXO (failing tx)

```
0: amounts = (6_000_000_000)
1: index values: [<wallet holderID>]
2: sigLock
3: chain(ORIGIN, predInputIdx=empty, originSlot=165, ...)
4: bytecode= (unexpected EOF)
5: bytecode= (unexpected EOF)
6: delegationParams(epochSlots=600, maxFrozenEpochs=20)
```

Note: slots 4 and 5 are EMPTY. Sequencer-milestone outputs put
`sequencer` at slot 4 and milestone data at slot 5. **This chain
origin has delegationParams attached but NO sequencer constraint — an
inconsistent state under the user's design intent** (see next
section).

### The failure mechanism

The chain origin's `delegationParams` at slot 6 enforces
`selfImmutableOnSuccessorIndex(delegationParamsConstraintIndex = 6)`
(`ledger/def/delegation_params.easyfl:28-52`). When the user
`proxi node dlg chain ...` produces a delegation output with
`delegateLock` + `chain` + `delegateLockState` (last), the produced
output has 5 elements total, no constraint at slot 6, and the
predecessor's immutability check trips:

```
panic: evalAtPath: path=[0 8 0 6] -> Tuple.At(6): index is out of range. Num elements: 5
```

`delegateLockState` is required to be last (`lock_delegate.easyfl:107`
`!!!delegateLockState_must_occupy_the_last_tuple_position`), so the
produced delegation output cannot legally carry forward
delegationParams at slot 6.

## User's design intent (verified during this session)

> "we either have sequencer chain and it always accepts delegation
> with immutable parameters, or it is not a sequencer chain and will
> never be. Conversion between the two types makes no sense and
> should not be possible. chain can only be destroyed."
>
> "This leads to the idea that natural place for delegation parameters
> is 'sequencer' constraint, so we do not need separate function for
> delegationParams."

Concretely:

- A chain is **typed at origin** as either:
  - **Sequencer chain** — always accepts delegations, with immutable
    `(epochSlots, maxFrozenEpochs)` baked into the `sequencer`
    constraint itself.
  - **Regular chain** — never accepts delegations, never carries any
    delegation parameters.
- **No type conversion is allowed.** A regular chain cannot become a
  sequencer chain later; a sequencer chain cannot lose its sequencer
  constraint. The only terminal action is chain destruction.
- The separate `delegationParams` constraint is obsolete: its two args
  fold into the `sequencer` constraint as additional args.

## Plan for the next session

### Step 1 — Reproduce in a ledger-level test

Build a self-contained test in `ledger/tests/` that:

1. Creates a chain-origin output with `delegationParams` at slot 6
   AND no sequencer constraint at slots 4/5 (the layout produced by
   `setup_seq` before any sequencer milestone runs).
2. Builds a delegation tx that consumes this output and produces a
   delegation output with `delegateLockState` at the last position.
3. Runs `tx.ValidateFullContext` and asserts the panic message
   `Tuple.At(6): index is out of range. Num elements: 5` (or the
   equivalent post-fix expected error).

This pins down the failure and gives a green-on-fix signal.

### Step 2 — Architectural fix (per user direction)

**Fold delegation parameters into the `sequencer` constraint.**

- Extend the `sequencer` constraint with two args:
  `sequencer(epochSlots, maxFrozenEpochs)` — both immutable, both
  ranged with the existing `constDelegationEpochSlots*` /
  `constDelegationMaxFrozenEpochs*` bounds.
- Delete the standalone `delegationParams` EasyFL constraint,
  `ledger/delegation_params.go`, the
  `ConstraintIndexDelegationParams` slot constant, and the wallet-side
  `NewDelegationParams` / `ParseDelegationParams` helpers (or — minimum
  scope — leave the helpers as deprecated wrappers; cleaner is delete).
- Update sequencer-side compose so the sequencer constraint emitted
  from genesis and from `setup_seq` carries the two values.
- Update consumers that read `delegationParams` (e.g. `lock_delegate`
  for target-chain epoch math) to read from the sequencer constraint
  instead.

### Step 3 — Lock the "no conversion" invariant

Once the sequencer constraint is present on a chain output, it must
be **immutable across every subsequent transit** —
`selfImmutableOnSuccessorIndex(sequencerConstraintIndex)` on the
constraint body. This already follows from the existing chain-output
rules but should be made explicit on the sequencer constraint itself
once the delegation params live on it.

**Open design question — when does the sequencer constraint first
appear on the chain?** Two viable answers, with trade-offs:

- **(A) On the first sequencer milestone (current convention,
  unchanged).** Chain origin is a plain chain (`sigLock` + `chain`,
  nothing else). The first sequencer-milestone tx adds the sequencer
  constraint, at which point its args (including the new delegation
  params) are locked in for the chain's lifetime. Before that
  milestone runs, the chain is just a regular chain and can never
  accept delegations. The "no conversion" rule applies *from the
  point the sequencer constraint is first attached* — never
  removable, args never editable.
  - Pro: no protocol change; matches every existing flow.
  - Pro: makes the failing-case state (`setup_seq` ran, sequencer
    never started) just a regular chain — no `delegationParams` to
    fail against, no inconsistency.
  - Con: still admits a one-shot "promotion" event (origin → first
    milestone) which is a kind of conversion. Whether the user's
    "no conversion" intent includes this is a design choice to
    confirm.

- **(B) At chain origin.** The sequencer constraint, with its
  delegation params, is present from byte zero on the chain origin
  output; promotion at first milestone goes away. Pure "type fixed
  at origin" semantics.
  - Pro: matches the user's "no conversion" intent literally.
  - Con: significant protocol change. Every existing flow that
    creates a sequencer chain — genesis, distribute, `setup_seq` —
    has to emit the sequencer constraint at origin. The first
    sequencer milestone's logic changes (no longer "the milestone
    that promotes"; just a normal transit).
  - Con: changes interpretation of the existing `chain origin`
    output shape on the wire.

**This decision should be made explicitly before code lands.** A
narrow first-pass implementation that picks (A) ships fast and fixes
the user-observed bug. (B) is the cleaner long-term shape but is a
protocol-level decision worth its own discussion.

### Step 4 — proxi mitigations

Under either design (A) or (B):

- `setup_seq`: drop `addDelegationParamsFlags` — the new sequencer
  constraint carries the params. The two numeric flags
  (`--delegation-epoch-slots`, `--delegation-max-frozen-epochs`)
  either feed into the sequencer constraint args (under A: emitted on
  the first sequencer milestone; under B: emitted at origin) or move
  to per-chain config.
- `mkchain`: drop `addDelegationParamsFlags` entirely. A regular chain
  has no delegation parameters and no sequencer constraint, period.
- `dlg chain`: pre-check that the source chain has a sequencer
  constraint and refuse if not — verify with the user before locking
  this in; the bug report concerned the reverse direction (delegating
  a non-sequencer chain that happened to carry stale
  delegationParams).

### Step 5 — Migration / breaking change posture

Per `develop08` convention, no backwards compatibility is preserved
across this refactor. Any on-chain state with the old
`delegationParams` constraint at slot 6 will be unreadable post-fix.
Acceptable on develop08; if a different branch needs migration, fork
the change.

## Verified code citations for the next session

- `proxi/node_cmd/setup_seq.go:34` — `addDelegationParamsFlags(seqSendCmd, true)` (the proximate bug source).
- `proxi/node_cmd/mkchain.go:32` — `addDelegationParamsFlags(makeChainCmd, false)` (correctly opt-out for regular chains).
- `proxi/node_cmd/mkchain.go:74-88` — `resolveChainOriginDelegationParams` reads the shared `flagAcceptDelegations`.
- `proxi/node_cmd/mkchain.go:144,236` — `dp := resolveChainOriginDelegationParams()` + emission via `lib.NewDelegationParams`.
- `proxi/node_cmd/setup_seq.go:61` — `MakeChain(amount)` from setup_seq (same shared flag).
- `ledger/def/delegation_params.easyfl:28-52` — constraint to delete.
- `ledger/def/lock_delegate.easyfl:107` — `delegateLockState` last-position rule (verified, unchanged by the fix).
- `ledger/def/sequencer.easyfl` — the sequencer constraint to extend.
- `ledger/delegation_params.go` — Go-side type to delete.
- `ledger/txbuildercore/helpers_delegate.go:84-` — `NewDelegationParams` to delete.

## Useful context

- Display path is wallet-side and singleton-free as of `03d73bdb`.
  The wallet-side `txDisplay` in `proxi/glb/wallet_submit.go` now
  prints full consumed-UTXO context on submit failure, making the
  reproduction trivial to visualise.
- The bootstrap sequencer chain is created at genesis
  (`ledger/genesis.go:36`) with `NewDelegationParams(...)`. The fix
  needs to update genesis to emit the new sequencer-constraint shape.

## Don't do in the next session

- Mineable-branch-inflation work — reverted (see
  `project_branch_inflation_mining.md` memory).
- The wider proxi singleton sweep — complete in `03d73bdb`.
