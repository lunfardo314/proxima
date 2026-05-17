# Per-target-chain delegation epoch parameters

## Status

**Active.** Refactor revived 2026-05-17 after the experience of bumping the
globals (cfa509f4: `epochSlots 700→600`, `maxFrozenEpochs 10→20`) made it
obvious that we don't know the right values yet and will likely need to
move them again. Globals work fine until the day they're imprinted into
the structure of every live delegation target — past that point a global
change becomes a network-wide ledger reset.

The change makes the two parameters a property of *the target chain
output itself*, snapshotted immutably at chain origin. Globals survive
only as proxi-side defaults for new chains; they have zero influence on
chains that already exist.

## Context: how delegation params work today

Three parameters drive delegation timing on this ledger:

| EasyFL constant                       | Go field                  | Current value | Role |
|---------------------------------------|---------------------------|---------------|------|
| `constDelegationEpochSlots`           | `DelegationEpochSlots`    | 600           | length of a delegation epoch in slots (~ 100 min) |
| `constDelegationMaxFrozenEpochs`      | `MaxFrozenEpochs`         | 20            | max simultaneous frozen epochs (~ 33 h horizon) |
| `constDelegationSafeRevocationSlots`  | `SafeRevocationSlots`     | 60            | slots after a frozen epoch ends during which the delegator can revoke without sequencer competition |

All three are global EasyFL constants in `ledger/def/lock_delegate.easyfl:11-13`,
surfaced to Go via `Constants` (`ledger/constants.go:51-55`). They are
baked at ledger initialisation and cannot vary per-chain.

`constDelegationSafeRevocationSlots` is a **network-wide UX guarantee**
about how long the delegator gets to revoke after each frozen epoch —
there is no reason for it to differ per chain. It stays a global
constant.

`constDelegationEpochSlots` and `constDelegationMaxFrozenEpochs` are
different: they govern the cadence and depth of a particular chain's
freeze accounting. Different targets reasonably want different values,
and — more importantly — the network needs to be able to evolve the
defaults without rewriting the rules for chains that already opted in
under the old defaults.

## Goal

Move `constDelegationEpochSlots` and `constDelegationMaxFrozenEpochs`
off the global library and onto **the target chain output**. Each chain
that opts in to accepting delegations advertises its own
`(epochSlots, maxFrozenEpochs)` pair, snapshotted at chain origin and
fixed for the chain's lifetime. Bounds (min/max for each) stay global
so the network can reject obviously misconfigured chains; the *defaults*
inside those bounds are a proxi-side concern only.

`constDelegationSafeRevocationSlots` stays a global constant.

## Invariant: target chain's params are immutable for the chain's lifetime

`epochSlots` and `maxFrozenEpochs` **must be fixed for the chain's
lifetime**. Reason: the chain carries a frozen-coverage vector whose
size is `maxFrozenEpochs`, and *every* delegation contributing to that
vector must use the same `epochSlots` math so the contributions land in
the right cell. Changing either after the fact would corrupt the
accounting on the target chain itself, and on the delegations targeting
it.

This is enforced by EasyFL: the `delegationParams` constraint
AND-s a self-immutability check across every chain transit, using the
universal `selfImmutableOnSuccessorIndex(...)` helper already in
`chain.easyfl:82` and used today by `foundryNonDestructible` and
`foundryMaxSupply` (`native_token.easyfl:75,92`).

## Placement: `delegationParams` sibling constraint at index 6

A new typed constraint at `ConstraintIndexDelegationParams = 6` (the
next reserved tuple position after `foundryPolicy = 5`):

```
delegationParams(epochSlots, maxFrozenEpochs)
```

Lives on chain outputs that opt in to accepting delegations. Foundries,
plain chains, and non-delegating sequencers don't carry it — no bytes
overhead on unrelated outputs. `chain()`'s arity stays at 7.

A chain output **without** `delegationParams` at index 6 simply cannot
be a delegation target: any `delegateLock` referencing it via `Target`
is rejected at delegation origin (the cross-check below fails because
there is nothing to cross-check against).

`delegationParams` can be attached only at **chain origin**. Once the
chain transits, `selfImmutableOnSuccessorIndex(6)` would reject any
transit that introduces a different (or absent) constraint at index 6.
A chain that didn't opt in at origin can never opt in later; the owner
must retire the chain and create a fresh one. This is the spec choice
(confirmed 2026-05-17).

### Foundries: never delegation targets

A **foundry is never a delegation target.** Only sequencer chains
accept delegations (the freeze/unfreeze flow is driven by sequencer
milestones — foundries don't produce milestones). Foundry chain
origins therefore never carry `delegationParams`; `proxi node foundry
create` exposes no flags for it.

What a foundry's controller *can* do is delegate the foundry chain
itself to a sequencer — equivalent to running `proxi node delegate
chain` on a regular chain holding. The intent: the foundry's
controller wants to earn delegation inflation on the chain's residual
token balance without giving up the foundry-ness (mint/burn rights and,
crucially, any `foundryNonDestructible` / `foundryMaxSupply` policy
the foundry was created with).

### The index-4 conflict (open design choice)

Delegation outputs today have a fixed shape:

```
0: amounts
1: index-values (master, target)
2: delegateLock
3: chain
4: delegateLockState
```

`_validStructureProduced` in `lock_delegate.easyfl` pins
`selfNumConstraints == 5` "to prevent injection attacks" and the
delegateLock body reads its state via `selfSiblingConstraint(4)` —
**index 4 is hard-wired to `delegateLockState`**.

A foundry chain output, however, occupies index 4 with the `foundry`
constraint (and index 5 with the optional foundry policy). Both
positions must persist across every transit when a policy is attached
— `foundryNonDestructible` and `foundryMaxSupply` self-lock via
`selfImmutableOnSuccessorIndex(foundryPolicyConstraintIndex = 5)`,
which fails the moment a transit changes or drops index 5.

So "delegate this foundry" (lock-only change, foundry constraint
preserved) **cannot fit** into the current 5-element delegation
layout. Two paths forward, pick one:

**Option A — Foundries cannot be delegated.** Accept the limitation.
The user delegates *non-foundry* token holdings to a sequencer; the
foundry chain stays a foundry. Simplest; no layout changes; no
`_validStructureProduced` relaxation. Phase 6 adds a negative test
that `proxi node delegate chain` on a foundry chain is rejected (the
produced delegation output would either drop the foundry constraint
or fail `_validStructureProduced`).

**Option B — Move `delegateLockState` to a free position and relax
the 5-element check.** Concretely:

- Reserve `ConstraintIndexDelegateLockState = 7` (after
  `ConstraintIndexDelegationParams = 6`). On every delegation,
  `delegateLockState` lives at index 7.
- The delegateLock body changes every `selfSiblingConstraint(4)` /
  `successorConstraint(4)` reference to point at index 7
  (`delegateLockStateConstraintIndex` EasyFL symbol).
- `_validStructureProduced` no longer pins `selfNumConstraints`;
  instead it checks the specific positions it requires:
  delegateLock at 2, chain at 3, delegateLockState at 7. Positions
  4 / 5 / 6 are optional and only meaningful when they carry their
  designated constraints (`foundry`, `foundryPolicy`,
  `delegationParams` — but a delegation is not a target, so 6 is
  always empty). The empty-bytecode-at-extras rule from Phase 3
  validation makes this safe.
- Foundry-delegations are then a single transit that swaps the lock
  at index 2 from `sigLock` (or `chainLock`) to `delegateLock`,
  preserves `foundry` at 4 and `foundryPolicy` at 5, attaches
  `delegateLockState` at 7. The foundry's self-immutability check on
  index 5 still passes (bytes unchanged).

Option B costs: one new constraint index, a small EasyFL rewrite of
the delegateLock body's siblings, and a relaxation of the
"exactly-5" injection-prevention check (replaced by per-index
positive assertions, which is arguably cleaner anyway). It removes
the limitation for the canonical use case the user called out:
*"issue full supply of native tokens then delegate the foundry,
otherwise non-destructible"*.

**Recommendation: Option B** if the foundry-delegation use case is a
first-class feature (the user's phrasing suggests it is). Option A
only if foundry-delegation is genuinely out of scope.

Until this choice is recorded, this spec leaves Phase 5 / 6 with
delegateLockState at index 4 (today's behaviour, Option A semantics).
A subsequent phase will execute Option B if chosen.

## Bounds

Four new EasyFL ledger constants define the *bounds* the target chain
may choose within:

```
constDelegationEpochSlotsMin           // 500
constDelegationEpochSlotsMax           // 2000
constDelegationMaxFrozenEpochsMin      // 8
constDelegationMaxFrozenEpochsMax      // 32
```

The current values (600 epoch slots, 20 max frozen epochs) sit inside
these bounds and become the **proxi-side defaults**.

The `delegationParams(...)` constraint's EasyFL body enforces both args
sit within these bounds. Anything outside gets rejected at output
validation time.

The delegator's `delegateLock(maxFrozenEpochs, inflationShare, ...)`
arg (the *delegator's* chosen depth) is bounded above by the target's
`delegationParams.maxFrozenEpochs` (not the global max), and below by 1
— bigger value = less frequent forced-withdrawal drops in total
coverage.

## How does a delegation see the target's params

The target's `(epochSlots, maxFrozenEpochs)` are copied into the
`delegateLock` at delegation origin. The lock body grows from 2 args
to 4:

```
delegateLock(maxFrozenEpochs, inflationShare, epochSlots, targetMaxFrozenEpochs)
```

- `$0 maxFrozenEpochs` — delegator's chosen depth (`1 ≤ $0 ≤ $3`)
- `$1 inflationShare` — required inflation share in promille
  (0..1000), unchanged
- `$2 epochSlots` — **copy of** target's
  `delegationParams.epochSlots`
- `$3 targetMaxFrozenEpochs` — **copy of** target's
  `delegationParams.maxFrozenEpochs`

Cross-check at **delegation origin only**: when the origin tx happens
to include the target chain output among consumed inputs, the
delegateLock's `$2` and `$3` must equal the target's
`delegationParams` values. Enforced by the embedded function
`evalDelegationOriginCrossCheck` called from the delegateLock body when
`_selfIsDelegationOrigin` is true. The check is **best-effort**: if the
target chain output is not among consumed inputs, the check permits
without verifying. Rationale: the typical delegator-initiates flow
cannot consume the target chain output (master doesn't control the
target), and wrong inline values only break the delegation for the
delegator — master-revoke still works and there is no protocol-level
harm. proxi-side tooling fetches values from the target and inlines
them correctly; the on-chain check is the safety net for the
coordinated-tx case (target sequencer transits its chain output in the
same tx as the delegation origin).

After origin, the delegateLock's inline `$2` / `$3` are the
authoritative source for all subsequent epoch math
(`lastSlotInDelegationEpoch`, `_delegationEpochFromSlot`, safe-revocation
window, `_validLimitsProducedFrozen`). No further lookups needed.
Inflation transits, revocations, on-hold transitions — none require the
target chain output to be present in the tx.

**Why inline (not look up each tx).** The master-revoke flow runs
*without* the target chain as input — adding it would require the
target's cooperation, which defeats the whole point of revocation.
Master-revoke checks `_consumedIsFrozenInTx` (needs `epochSlots` to
compute `lastSlotInEpoch`) and the safe-revocation window (needs
`epochSlots` too). So `epochSlots` must be inline. Once we're already
inlining one, inlining `targetMaxFrozenEpochs` too is essentially free.
Cost is ~3–5 bytes per delegation UTXO, persistent — see *Storage
deposit* below.

**Why the inline copy can't go stale.** The delegateLock body and the
delegation's index-value tuple (which holds the target ChainID) are
both byte-equal across every chain transit, by the existing
`_targetUnlockedConsumed` rules at `lock_delegate.easyfl:252-254`. So
once the origin cross-check fixes the inline pair to the target's
params, neither the inline copy nor the target pointer can ever change.
The target's `delegationParams` is in turn pinned by
`selfImmutableOnSuccessorIndex(6)`. End-to-end immutability.

## Target & lock immutability across transit (already enforced)

The delegateLock's target chain ID must be immutable across the
delegation's lifetime — otherwise an attacker could transit the
delegation to point at a different target whose `delegationParams`
differ from the inline `$2`/`$3` copies, silently breaking the epoch
math.

This is **already enforced** by the existing delegateLock body when
the target spends the delegation for inflation
(`_targetUnlockedConsumed`, `lock_delegate.easyfl:252-254`):

```easyfl
// delegation lock at the lock element must match exactly
require(equal(successorConstraint(lockConstraintIndex), selfSiblingConstraint(lockConstraintIndex)),
        !!!delegation_lock_on_successor_must_be_exactly_the_same),
// and the index-value tuple (master, target) must match exactly too
require(equal(successorConstraint(indexValuesConstraintIndex), selfSiblingConstraint(indexValuesConstraintIndex)),
        !!!delegation_index_values_on_successor_must_be_exactly_the_same)
```

- The index-values check pins **master** and **target** byte-equal on
  the successor.
- The lock-element check pins the **entire delegateLock bytecode**
  byte-equal on the successor — so when we extend the lock from 2 to
  4 args, the two new args (`$2 epochSlots`,
  `$3 targetMaxFrozenEpochs`) inherit this immutability automatically.
  No new EasyFL rule needed.

On the **master-unlock** path (`_masterUnlockedConsumed`) — revocation
— there is no successor delegation; master takes the recovered tokens
back as a sigLock (or anywhere else). No transit, no immutability
concern.

So: once a delegation is born with the four-arg lock and a target,
neither the target nor the inline params can ever change. The origin
cross-check is the only window in which they could be wrong; after that
they're fixed forever.

## Storage deposit — UX & tokenomics calibration

Storage-deposit math itself (`ledger/sdeposit.go:78`) is purely
byte-count-based via `effectiveStorageSize(o)`, so the function is
transparent to this refactor. The *amount* of deposit needed does
shift, in two directions, and both need careful calibration before we
ship.

**1. Per-delegation UTXO** — `delegateLock` body grows from 2 to 4
args. Two new z-encoded inline args plus 2 element-length bytes adds
~3–5 bytes to the lock bytecode at output index 2, persistent for the
delegation's lifetime. At today's `storageDeposit(size)` schedule this
is fractions of a token per delegation — irrelevant to the minimum-
deposit floor.

The frozen-coverage vector on a delegation output is sized by the
delegator's chosen `maxFrozenEpochs` (`≤ target.maxFrozenEpochs`).
`NewAmounts` (`ledger/amounts.go:31-41`) trims trailing zeros, so the
actual serialized size grows with active freeze *depth*, not with the
cap. A delegator who chooses depth 1 pays exactly the same deposit
whether the target's cap is 8 or 32.

**2. Per-target chain output** — two new costs:

  - The `delegationParams` constraint at index 6 adds the constraint's
    serialized bytecode + element-length byte to the chain output's
    own size — ~6–10 bytes one-time, paid by the chain owner.
  - The chain's frozen-coverage vector is sized by *its own*
    `maxFrozenEpochs` (because that's the cap on contributions it can
    receive). Today every chain pays for `lib.MaxFrozenEpochs = 20`
    cells regardless of whether it accepts delegations. After this
    refactor, a chain that doesn't opt in pays for **zero** frozen-
    coverage cells (vector is empty → trimmed). A chain that opts in
    with `maxFrozenEpochs = 32` pays for up to 32 cells, but only the
    actively-non-zero ones contribute to size (trailing zeros
    trimmed).

  Net effect at typical configurations: **chains that don't accept
  delegations get cheaper**; chains that do accept them pay roughly
  what they pay today, plus the constant `delegationParams` bytes.

**Calibration questions to resolve before shipping**:

- Are `[500..2000]` slots and `[8..32]` epochs the right bounds?
  500 slots is ~83 min, 2000 slots is ~5.5 h. 8 epochs at 500 slots
  is ~11 h horizon; 32 epochs at 2000 is ~7.4 days. That's a wide
  range; verify the upper end is operationally meaningful and the
  lower end doesn't break sequencer ergonomics.
- Do we want a *default proxi behaviour* of opt-in or opt-out for
  sequencer setup? Memory note says spec choice was "default-on";
  this stays open until phase 5.
- Does the storage-deposit schedule need a tweak so that the new
  `delegationParams` element doesn't get rounded into an extra deposit
  tier? `proxi db txstore` round-trip on a representative chain output
  before/after will answer this.

These calibrations don't affect correctness — they affect operator
incentives. Adjust the proxi defaults and the EasyFL bounds before any
mainnet-style ledger generation, not after.

## Chain creation flow — attaching `delegationParams` at origin

`delegationParams` can only be attached **at chain origin** (because
immutability rejects any transit that changes index 6). To create a
chain that can act as a delegation target:

- `proxi node mkchain <amount>` — extended with three new flags:
  ```
  --delegation-epoch-slots N        (default 600, bounds 500..2000)
  --delegation-max-frozen-epochs N  (default 20, bounds 8..32)
  --no-delegations                  (omit delegationParams entirely)
  ```
  When the flags are present (or by default if we choose default-on),
  the produced chain-origin output carries
  `delegationParams(epochSlots, maxFrozenEpochs)` at index 6. The
  `MakeChainOrigin` flow (`api/client/client.go` and
  `proxi/node_cmd/mkchain.go`) needs the analogous parameter
  pass-through.

- `proxi node seq setup` (the sequencer-bootstrap entry point in
  `proxi/node_cmd/setup_seq.go`) — needs the same flags. Sequencers
  will be the primary class of chain that accepts delegations, so
  this is the most-used path.

- Foundry creation (`proxi node foundry create`) — **no
  `delegationParams` flags**. Foundries are never delegation targets
  (see *Foundries: never delegation targets* above).

A chain origin without `delegationParams` cannot ever become a
delegation target — there's no path to add the constraint later
(`chain` transit can't introduce new constraints at a previously empty
index without violating the chain successor invariants, and even if it
could, `selfImmutableOnSuccessorIndex(6)` would refuse). The owner has
to retire the chain and create a fresh one with `delegationParams` from
day 1.

## Defaults — proxi user-facing side

`proxi node delegate amount` (existing):

| Flag                | Default behaviour |
|---------------------|-------------------|
| `--target / -q`     | required                                            |
| `--epochs / -e N`   | 0 → take target's `delegationParams.maxFrozenEpochs`; non-zero → cap at target's value |
| `--share`           | unchanged (current 900 default) |

Sequencer setup (`proxi node seq setup` / `make_chain` flow) — new
flags:

```
--delegation-epoch-slots N           (default 600, bounds 500..2000)
--delegation-max-frozen-epochs N     (default 20, bounds 8..32)
--no-delegations                     (omit delegationParams; chain cannot be a delegation target)
```

Updating `delegationParams` on a live chain is **forbidden** —
`selfImmutableOnSuccessorIndex(6)` rejects any transit that changes
those bytes. To "change policy" the sequencer would have to retire and
create a new chain.

## Implementation phases

Concrete sequencing. Each phase ends green on `go test ./ledger/...`
before the next starts (per `feedback_test_scope.md`).

**Phase 1 — `delegationParams` constraint.** New EasyFL
`ledger/def/delegation_params.easyfl` defining
`delegationParams(epochSlots, maxFrozenEpochs)` with bounds checks +
`selfImmutableOnSuccessorIndex(delegationParamsConstraintIndex)`. New
`ledger/delegation_params.go` typed wrapper (parallels
`ledger/foundry.go`) with inline round-trip test. Add the four bounds
constants. Add `ConstraintIndexDelegationParams = 6` in
`def_constants_path0.go` and the matching `delegationParamsConstraintIndex`
EasyFL symbol in `pathConstantsUpgrade0`. Register in lib. **No effect
on delegateLock yet** — this phase just lands the new constraint type.

**Phase 2 — delegateLock 2→4 args + origin cross-check.** In
`ledger/def/lock_delegate.easyfl`: drop
`constDelegationEpochSlots` and `constDelegationMaxFrozenEpochs`;
keep `constDelegationSafeRevocationSlots`. Rewire
`delegationEpochOffset`, `lastSlotInDelegationEpoch`,
`_delegationEpochFromSlot`,
`__consumedIsInTheSafeRevocationWindowTx`, and
`_validStructureProduced` to read `$2` (epochSlots) and `$3`
(targetMaxFrozenEpochs) from the delegateLock body. Extend
`delegateLock`'s public arity from 2 to 4 args. Add an origin
cross-check that runs only when `_selfIsDelegationOrigin == true`,
verifying inline `$2`/`$3` equal the target chain's
`delegationParams` (fetched via `parseInlineDataArgument` on the target
chain output located in the tx). Update `ledger/lock_delegate.go`:
`DelegateLock` struct gains `EpochSlots uint32` and
`TargetMaxFrozenEpochs byte`, `NewDelegateLock` signature, serde.

**Phase 3 — rewire Go helpers off `Constants`.** All the
`Constants.EpochOffsetSlotsDirect / CoveredSlotsInCurrentEpoch /
FrozenSlotsFromFrozenEpochs / EpochFromSlotDirect / EpochLimits /
LastSlotInEpochDirect / DiffEpochs / AdjustFrozenCoverageVector`
helpers move off `Constants` (no per-chain knowledge there anymore)
and become methods that take the relevant `(epochSlots,
maxFrozenEpochs)` pair explicitly, sourced from the
`DelegationOutput` (or its `DelegateLock`). Update
`evalEnforceFrozenCoverageOnDelegateOutput`
(`ledger/lock_delegate.go:262`) and
`evalEnforceFrozenCoverageOnNonDelegationChain`
(`ledger/chain.go:212`) to read `maxFrozenEpochs` from the chain
output's `delegationParams` (or the delegateLock's `$3`) instead of
`lib.MaxFrozenEpochs`. Drop `DelegationEpochSlots` and `MaxFrozenEpochs`
fields from `Constants`; replace with the four `...Min`/`...Max` bound
fields. Adjust `Lines()` accordingly.

**Phase 4 — sequencer + memdag plumbing.** In
`sequencer/txbuilder_seq/txbuilder_seq.go`: `chainOutAmounts` buffer
sizing reads from the sequencer's own `delegationParams` (not
`Library.MaxFrozenEpochs`). The freeze/inflation routines that iterate
over target frozen epochs read the cap from the sequencer's
`delegationParams`. `sequencer/task/proposal.go:282` and
`sequencer/txbuilder_seq/req_askstop.go:163` get rewired the same way.

**Phase 5 — proxi flags + inspection.** New flags on
`proxi node mkchain` / `proxi node seq setup` / `proxi node foundry
create`. `proxi node delegate amount` fetches target's
`delegationParams` on top of the chain output it already loads,
inlines `(epochSlots, maxFrozenEpochs)` into the produced delegateLock,
and includes the target chain as endorsement or input so the origin
cross-check passes. `proxi node chain` / `balance` / `utxo` surface
`delegationParams` when attached.

**Phase 6 — tests.** `ledger/tests/delegation_test.go` extended to
cover per-target params, bounds enforcement at output validation, origin
cross-check (success + failure cases: missing target in tx, wrong inline
`$2`/`$3`, out-of-bounds params), immutability of `delegationParams`
across chain transit (any transit that changes index 6 must fail).

**Foundry interaction tests** depend on the Option A / Option B
decision recorded in *The index-4 conflict* above:

- **Option A (foundry-not-delegatable, current default)** —
  `ledger/tests/foundry_delegation_test.go` covers a single
  negative scenario: `proxi node delegate chain` on a foundry chain
  must reject (either because the produced delegation can't preserve
  the foundry constraint at index 4 or because
  `_validStructureProduced` rejects a 6+ element output). Also
  confirm a foundry origin built via `MakeFoundryOriginOutput` carries
  no `delegationParams` and is rejected as a delegation target.
- **Option B (foundry-delegatable via delegateLockState at 7)** —
  full end-to-end: create foundry with `foundryNonDestructible`,
  mint full supply, then transit the foundry chain to a delegation
  pointing at a sequencer target (lock at 2 changes; foundry at 4
  and foundryPolicy at 5 preserved byte-equal; delegateLockState
  attached at 7). Exercise master-revoke (must produce a successor
  that *re-acquires* the original lock or breaks the foundryPolicy
  immutability — depending on how revocation is defined for this
  case). Exercise target freeze/unfreeze/harvest with the foundry
  constraint still present.

Storage-deposit smoke test: compare `MinimumStorageDeposit` for chain
outputs with/without `delegationParams`, and for delegations with
2-arg vs 4-arg locks; lock the numbers into a regression test so
schedule tweaks are caught.

## Files to change (summary)

EasyFL:
- `ledger/def/lock_delegate.easyfl` — drop the two
  `constDelegationXxx` constants moving to per-target; rewire epoch
  math to use `$2`/`$3`; keep `constDelegationSafeRevocationSlots`
  global; extend `delegateLock` body to 4 args; AND in the origin
  cross-check.
- (new) `ledger/def/delegation_params.easyfl` — defines
  `delegationParams(epochSlots, maxFrozenEpochs)` with
  `selfImmutableOnSuccessorIndex(delegationParamsConstraintIndex)` +
  bounds checks.

Go:
- `ledger/constants.go` — drop `DelegationEpochSlots`,
  `MaxFrozenEpochs`; add the four bounds fields. Keep
  `SafeRevocationSlots`.
- `ledger/lock_delegate.go` — `DelegateLock` grows two fields,
  signature/serde updated.
- (new) `ledger/delegation_params.go` — typed wrapper.
- `ledger/def_constants_path0.go` — add
  `ConstraintIndexDelegationParams = 6`.
- `ledger/lock_delegate_util.go` —
  `MakeDelegateInitOutputParams` gains `EpochSlots` and
  `TargetMaxFrozenEpochs`; pipe through `MakeDelegationInitOutput`.
  Helpers move off `Constants` and onto the delegation/output type.
- `ledger/chain.go` —
  `evalEnforceFrozenCoverageOnNonDelegationChain` reads chain's own
  `delegationParams.maxFrozenEpochs` (zero/empty if not attached).
- `ledger/amounts.go` — no signature change.

Sequencer:
- `sequencer/txbuilder_seq/txbuilder_seq.go`,
  `sequencer/task/proposal.go`,
  `sequencer/txbuilder_seq/req_askstop.go` — all reads of
  `lib.MaxFrozenEpochs` switch to the chain output's `delegationParams`.

Tests:
- `ledger/tests/delegation_test.go` — adjust + extend.
- (new) `ledger/tests/foundry_delegation_test.go` — foundry-delegation
  scenarios per the Option A / Option B decision (see Phase 6).

Proxi:
- `proxi/node_cmd/delegate/amount.go` — load target's
  `delegationParams`, inline into produced delegateLock, include
  target in origin tx; cap `--epochs / -e` by target's value.
- `proxi/node_cmd/setup_seq.go`, `proxi/node_cmd/mkchain.go` — new
  flags. (`proxi/node_cmd/foundry/create.go` gets no flags — see
  *Foundries: never delegation targets*.)
- `proxi/node_cmd/balance.go`, `chain.go`,
  `proxi/node_cmd/utxo.go` — surface `delegationParams` in inspection.

## Backward compatibility

`develop08` has many breaking ledger changes already
(`project_v080_breaking.md`). This change goes in the same bucket — no
migration shim. Existing testnet state has to be regenerated, same as
for prior phases.
