# Forced delegation of idle UTXOs

> **RESEARCH** — Forcing idle UTXOs into delegation. Draft only, no implementation; written to map the ledger invariants it would break.

## Status

**Draft / spec only.** No implementation. Goal of this document is to nail down the design surface area, the ledger invariants we'd have to break, and the cheapest way to bound the damage. Implementation, parameter tuning, and incentive calibration are out of scope until the design is accepted.

Related: [delegate_lock.md](archive/shipped/delegate_lock.md), [delegation_epoch_params.md](archive/shipped/delegation_epoch_params.md).

## Motivation

Coverage on the Proxima ledger is bounded above by `2 × totalSupply`. Reaching that bound requires every PROX to be **either** in a running sequencer chain **or** locked into a delegation that contributes to a sequencer's coverage. Tokens sitting idle in plain sigLock outputs do not contribute to coverage.

In steady state we expect a typical holder to keep ~100–1000 PROX in their sigLock account (for fees and operating needs) and freeze the rest in delegations. If the network behaves that way, coverage will sit a few percent under the theoretical maximum.

The failure mode this spec targets: holders forget to delegate, or have a technical obstacle to delegating (sequencer offline, wallet stale, custodial holder dormant). Their idle balance silently degrades total coverage and weakens cooperative consensus.

**Forced delegation** is a mechanism by which a sequencer can take an idle UTXO that meets clear criteria, wrap it into a delegation pointing at itself, and start earning inflation from it — while preserving the original owner's right to revoke. The original owner does not lose tokens; they only lose the option of leaving the tokens idle past the eligibility window.

## Goals

1. Recover idle-token coverage without owner cooperation, automatically, at sequencer initiative.
2. Preserve owner's right to recover at any time via the existing `askstop` / safe-revocation path.
3. Keep the protocol change **bounded in surface area**: one new typed lock, one Go-level exception in Stage 3 validation, no changes to existing locks or unrelated EasyFL constraints.
4. Be unambiguous about which ledger invariants are broken and where the break lives.

## Non-goals

- Multi-owner / m-of-n outputs. Same single-signature constraint as the rest of the protocol.
- Force-delegating chain outputs, foundries, existing delegations, or anything but plain sigLock UTXOs. (See _Eligibility_ below.)
- Compensating the original owner. Forced delegation earns the same inflation share the owner would have earned voluntarily; the sequencer's profit margin comes from the `inflationShare` parameter, not from owner penalty.
- Coercing owners to a particular sequencer. Any sequencer can force-delegate any eligible UTXO, first-come-first-served. The owner can `askstop` immediately and re-delegate to a different sequencer.

## The hard part: bypassing consumed-output lock validation

A normal delegation transaction:
- consumes a sigLock UTXO owned by Alice,
- produces a delegateLock UTXO with `master = Alice`, `target = sequencer`,
- is **signed by Alice** so the sigLock's `_sigLock` check passes on the consumed side.

For forced delegation the sequencer has no access to Alice's key. So Alice's sigLock cannot validate in the usual way. This is the core architectural intrusion: at least one consumed UTXO must be allowed to settle **without its own lock returning true**.

The bypass cannot live in the txbuilder — the ledger itself runs `_runOutputs(PathToConsumedOutputs, ...)` (`ledger/transaction/validate.go:184`) over every consumed output and evaluates its constraints. Skipping a consumed UTXO's lock is a property of how the ledger validates the transaction, not how the sender builds it. So we have to introduce an explicit, narrow exception in the Stage 3 path.

The least disruptive shape:

> A consumed UTXO's **lock element only** is skipped during Stage 3 iff a produced output in the same transaction is a `forceDelegateLock` whose body proves that this consumed UTXO is force-delegation-eligible and is being wrapped correctly. All other consumed-side constraints (amount vector, index-values tuple, any non-lock constraints) still run.

That is:

- We do **not** introduce a "transaction type" field.
- We do **not** modify `sigLock`, `chainLock`, or any existing lock.
- The exception is one branch in `_runOutputs` that, before running the consumed lock at `lockConstraintIndex`, checks whether the consumed input is referenced by exactly one `forceDelegateLock` produced output. If yes, the lock evaluation is **replaced** by evaluation of that produced wrap's body against the consumed input as additional argument. If the wrap validates, the consumed lock is considered satisfied.
- Everything else the wrap needs to enforce (eligibility, byte-equality of the wrapped bytes, master = original owner) lives in `forceDelegateLock`'s own EasyFL body — same way `delegateLock` carries its own policy.

This is the "ugly exception" — there is no way to make it pretty because the protocol genuinely needs a consumed UTXO to settle without its owner's consent. The mitigation is that the exception is **one place**, gated on a single recognisable lock kind, and the burden of "is this legitimate?" is pushed into EasyFL where the rest of the policy lives.

### Invariants we break, named explicitly

1. **"Every consumed UTXO's lock element returns true under the transaction's signature."** Broken for the force-delegated input. Replaced by "the lock element is satisfied by a matching `forceDelegateLock` wrap on the produced side." Bounded by the wrap's eligibility check.
2. **"The transaction signer is an authorised spender of every consumed UTXO."** Broken: the signer (the sequencer's controller, via the sequencer's own chain output spent in the same tx) is not the owner of the force-delegated input. Bounded by: the wrap encodes the original owner's HolderID as `master`, preserving Alice's revocation right.
3. **No new break in amount conservation.** The wrapped amount on the produced side equals the consumed amount byte-for-byte; ordinary `consumed + inflation = produced` still holds.

Nothing else breaks. No change to the single-signature model: the tx still carries exactly one signature, it just no longer authorises every input.

## Eligibility

A consumed UTXO is force-delegation-eligible iff **all** of the following hold. These are enforced inside the `forceDelegateLock`'s EasyFL body, evaluated against the consumed input as the bypass replacement:

1. **Lock kind is plain `sigLock`.** Force-delegating a chain output, foundry, delegation, or tag-along is forbidden. EasyFL: `equal(parseBytecode(consumedLockBytes, 0x), #sigLock)`.
2. **Idle ≥ `constForceDelegationIdleSlots` (default 8600 ≈ 1 day).** Computed as `txSlot - consumedOutputSlot ≥ constForceDelegationIdleSlots`. `consumedOutputSlot` is the slot component of the consumed input's OutputID, available through `inputIDByIndex`.
3. **Amount ≥ `constForceDelegationMinAmount` (default e.g. 1000 PROX).** Threshold tunable as a global EasyFL constant. Below this, the friction is not worth the coverage gain and the wrap would be griefable as spam against small UTXOs.
4. **Not already a delegation.** Implied by (1) but stated for clarity.
5. **Wrap amount equals consumed amount exactly.** The new force-delegation output's token-balance value must equal the consumed UTXO's token-balance value. Any inflation produced in the same tx must be attributed elsewhere (e.g. to the sequencer's own output) — the wrap is amount-preserving by rule.

(2) prevents griefing freshly-issued outputs. (3) prevents spamming the network with cheap force-delegation txs against dust UTXOs. Together they reduce the attack surface to "high-value, long-idle holdings," which is exactly the population the mechanism is designed to recover.

## The `forceDelegateLock` constraint

A new typed lock at `lockConstraintIndex`, with the same general shape as `delegateLock`. Index-value tuple stores `(originalOwnerHolderID, targetChainID)` (master-first, same convention as `delegateLock`). Lock bytecode args:

```
forceDelegateLock(maxFrozenEpochs, inflationShare, consumedInputIndex)
```

- `$0 maxFrozenEpochs` (z8) — same semantics as `delegateLock`. Default e.g. 10.
- `$1 inflationShare` (z64) — same semantics as `delegateLock`. Sequencer-chosen at wrap time, capped by a network-wide minimum the wrap enforces (so the owner cannot be force-delegated at 0%).
- `$2 consumedInputIndex` (z8) — index of the consumed input this wrap replaces.

Index-value tuple position 0 = `originalOwnerHolderID` (32 bytes), position 1 = `targetChainID` (32 bytes). The HolderID is **the raw 32-byte sigLock holder ID** of the original consumed sigLock — derived by parsing the consumed lock bytes. This is what makes the wrapped output indexable to the original owner's account (see _Indexing_).

The wrap UTXO has the same tuple shape as a regular delegation: amounts (0), index-values (1), lock (2), chain constraint (3) chaining the wrap into the target sequencer's chain — and `delegateLockState` (4) initialised to `frozen` with `_selfLastFrozenEpoch` set by the wrap-time rules of regular delegation. **The wrapped output is born already-frozen** so the sequencer can immediately count its amount in coverage.

The `forceDelegateLock`'s EasyFL body validates, on the **produced side**:
- All of the eligibility rules above against the consumed input at `$2`.
- That the consumed input's sigLock holder ID equals the wrap's `selfIndexValue(0)`.
- That the wrap's amount equals the consumed input's amount.
- That `selfNumConstraints == 5` and the structural shape matches a delegation (mirrors `_validStructureProduced`).
- That the chain constraint at index 3 is a chain origin (the wrap starts a fresh chain — same as `delegateLock` origin).

On the **consumed side**, the `forceDelegateLock` reuses the existing `delegateLock` logic for `_masterUnlockedConsumed` / `_targetUnlockedConsumed` — i.e. once wrapped, the output behaves exactly like a regular delegation. This is the cheapest way to inherit `askstop`, safe-revocation, frozen-epoch math, inflation transit, and target-immutability rules: don't fork the policy, share it.

In other words, `forceDelegateLock` differs from `delegateLock` **only at origin**. Post-origin, it's a delegation in every respect that matters.

(Realisation as we wrote this: the simpler alternative is to have **one** `delegateLock` that accepts a second origin path — "wrapped" — alongside the existing voluntary path. That would avoid a whole new lock kind. The tradeoff is loading the existing constraint with origin-mode-detection logic. Open question, see below.)

## Stage 3 bypass: where it lives

The Go exception is concentrated in one place: the per-output runner in `ledger/transaction/validate.go` (around `_runOutputs` / `runTuple`).

Pseudocode:

```go
// Before iterating constraints on a consumed output:
if wrapIdx, ok := tx.forceDelegationWrappedBy(consumedInputIndex); ok {
    // run all constraints on this consumed output EXCEPT the lock at lockConstraintIndex
    // the wrap at producedOutputs[wrapIdx] takes responsibility for lock-equivalent validation
    // when its own forceDelegateLock body runs on the produced side
    skipLockConstraint = true
}
```

`tx.forceDelegationWrappedBy(i)` is a one-time linear scan of produced outputs (cheap — typically ≤ 256) that returns the unique produced index whose lock is a `forceDelegateLock` with `$2 == i`. If zero matches → no bypass. If more than one match → tx is invalid (each wrapped input maps to at most one wrap).

The wrap's own EasyFL body is what enforces the eligibility / amount / holderID / shape checks. Go does **not** duplicate those — Go's only job is the dispatch ("skip the lock for the input that's wrapped, trust the wrap to validate").

This keeps the Go change small (single helper + one branch in `_runOutputs`) and keeps the policy in EasyFL where the rest of it lives.

## How the original owner sees and recovers their UTXO

### Indexing

The wrapped output's index-value tuple position 0 holds `originalOwnerHolderID` byte-equal to what the consumed sigLock held. Existing indexing on `TriePartitionControllers` will pick this up automatically and index the wrap under Alice's HolderID — same partition where her other sigLock UTXOs live. Her wallet (`proxi node balance`, `/api/account_outputs`) will list the wrap alongside her sigLock balances, distinguishable by lock kind in the rendering layer.

No new index needed. The existing `controllers / target / sender` semantics already cover this (master at position 0 → indexed as controller).

### Recovery via `askstop`

The wrap inherits `_masterUnlockedConsumed` from the delegation policy. Alice signs a tx that consumes the wrap with byte `0xff` in unlock-params[1], producing a fresh sigLock output paying back to herself. The frozen-state check `_consumedIsFrozenInTx` blocks her until the current frozen epoch closes, and the safe-revocation window applies as for any delegation.

**There is no separate "unwrap" operation.** The consumed UTXO is gone forever (its OutputID is consumed). What Alice recovers is a sigLock UTXO with the same amount, locked back to her HolderID. The "wrapping byte-for-byte" intuition from the brief turns out not to require literal byte preservation in the wrap payload — it is enough that the wrap reproduces (amount, holderID) on the produced side, because that's everything observable about a sigLock UTXO modulo OutputID, and OutputID is unrecoverable by definition.

(If we later want a richer "restore original" path — e.g. preserving the original output's full byte image for audit — we can add a slot-N inline-data position holding the consumed lock bytes. For now we don't, because nothing in the protocol consumes that information.)

### Sequencer revocation

The wrap is a delegation, so the sequencer can `askstop`-equivalent itself by producing a delegation marked `onHold` on the next chain transit. Same paths as regular delegations. No new code.

## Sequencer incentives

The sequencer earns `(1 - inflationShare) × inflation` over the wrap's frozen lifetime, identical to a voluntary delegation. The marginal cost is one extra inflated-storage UTXO and a small tx overhead. The reason a sequencer would bother is coverage: the wrap immediately counts in the sequencer's frozen-coverage vector, which is the input to the biggest-coverage rule.

This is the same incentive structure that already drives sequencers to accept voluntary delegations — we're just letting them claim coverage from idle holders unilaterally.

**Sequencer competition.** Multiple sequencers will scan for the same eligible UTXOs. The race is settled by the standard tangle: whichever wrap settles into the LRB first wins. Losers' wrap transactions are orphaned harmlessly. No new fairness mechanism needed.

## Anti-griefing analysis

| Vector | Mitigation |
|---|---|
| Sequencer wraps Alice's UTXO with `inflationShare = 0` so she gets nothing | Wrap enforces `$1 ≥ constForceDelegationMinInflationShare` (e.g. 500 promille = 50%). |
| Sequencer wraps Alice's UTXO with absurdly long `maxFrozenEpochs` to lock her up | Wrap enforces `$0 ≤ constForceDelegationMaxFrozenEpochs` (e.g. the same 10 used elsewhere). Standard safe-revocation window still applies after each frozen epoch. |
| Sequencer spams wraps against dust UTXOs to bloat state | Amount threshold (`constForceDelegationMinAmount`) excludes dust; tx-size cost is real, so wrapping tiny outputs is not profitable. |
| Sequencer wraps a freshly-issued UTXO that the holder was about to delegate | Idle threshold (`constForceDelegationIdleSlots ≈ 8600 ≈ 1 day`) gives the holder a clear window. |
| Many sequencers race for the same UTXO, wasting tx bandwidth | Acceptable cost; the racing is bounded by the population of eligible UTXOs, which is exactly the set we want recovered. |
| Owner is held captive in perpetually re-froze cycles | Owner can always `askstop` at the next safe-revocation window. Once on-hold, the wrap cannot be re-frozen by the sequencer — same as for voluntary delegations. |

A reasonable safety bound: even in the worst case, the owner's loss is the inflation they would have earned during one frozen epoch (≈ 2 h) minus the share the wrap pays them, plus the one-epoch revocation delay. That's roughly the same cost they'd pay for not delegating manually anyway.

## EasyFL changes

- New file `ledger/def/lock_force_delegate.easyfl` — defines `forceDelegateLock(maxFrozenEpochs, inflationShare, consumedInputIndex)`, the eligibility predicates, and the wrap-shape checks. Reuses (via import / shared private symbols) the post-origin delegation policy from `lock_delegate.easyfl`.
- New global constants in `ledger/def/lock_force_delegate.easyfl`:
  - `constForceDelegationIdleSlots` (default 8600)
  - `constForceDelegationMinAmount` (default e.g. 1000 PROX in raw units)
  - `constForceDelegationMinInflationShare` (default 500 promille)
- No change to `sigLock`, `chainLock`, `delegateLock`, `chain`, or any existing constraint.

## Go changes

- `ledger/transaction/validate.go` — extend `_runOutputs(PathToConsumedOutputs, ...)`: before running the lock at `lockConstraintIndex` on a consumed output, dispatch through a helper `tx.forceDelegationWrappedBy(consumedInputIndex)`. If a unique matching wrap exists, skip the consumed lock evaluation. If multiple match → reject. Other constraints on the consumed output still run.
- `ledger/transaction/transaction.go` — `forceDelegationWrappedBy` helper. Linear scan of produced outputs, returns wrap index or none. Result cached on the `*Transaction` for the duration of validation.
- `ledger/lock_force_delegate.go` — typed wrapper (parallels `ledger/lock_delegate.go`): `ForceDelegateLock{OriginalOwnerID, TargetChainID, MaxFrozenEpochs, RequiredInflationShare, ConsumedInputIndex}`, constructor, `FromBytes`, registration, inline test.
- `ledger/def_constants_path0.go` — no change (the new lock lives at the existing `lockConstraintIndex`, no new tuple position).
- `ledger/lock_force_delegate_util.go` — `MakeForceDelegateOutputParams`, `MakeForceDelegateOutput`. Parallels `lock_delegate_util.go`. Used by the sequencer's force-delegation builder.

## Sequencer changes

- `sequencer/txbuilder_seq/req_force_delegate.go` — new request type producing a tx that:
  - consumes the eligible idle sigLock UTXO,
  - consumes the sequencer's own chain output (for chain transit and as the signing input),
  - produces the wrap with chain origin chained into the sequencer chain,
  - produces the sequencer's continued chain output.
  - is signed by the sequencer controller (single signature, as always).
- A new scanning loop on the sequencer that periodically queries the indexer for sigLock UTXOs older than `constForceDelegationIdleSlots` and with amount ≥ `constForceDelegationMinAmount`, prioritises by amount, and submits wrap requests. This is similar to how the tag-along backlog is scanned today.
- A node-level opt-in flag (default off until the mechanism is well-tested): `sequencer.force_delegation.enabled` in the YAML config.

## CLI changes (proxi)

- `proxi node balance` and `proxi node account` — render force-delegated UTXOs distinctly ("frozen [forced by seq …]"). The lock kind is already discriminable.
- `proxi node delegate askstop` — works unchanged; the wrap is a delegation, the same path applies.
- `proxi util force_delegation eligible` (new, optional, low priority) — lists the user's UTXOs that are within or near the eligibility window, so they can pre-empt by delegating voluntarily. Pure UX, no protocol impact.

## Open questions

1. **One lock or two?** The spec proposes a separate `forceDelegateLock`. The cheaper alternative is one `delegateLock` with two origin paths (voluntary and wrapped), distinguished by a marker arg. Tradeoff: fewer kinds (good for indexing simplicity and serialiser surface) vs. heavier per-output validation logic for every voluntary delegation (every delegation pays the cost of distinguishing modes). Lean toward separate locks unless we hit a strong reason to merge.
2. **Should the wrap carry the consumed UTXO's full byte image at slot 4+?** Currently the spec keeps the wrap byte-minimal (master + target + amount is enough). Carrying the full original output bytes would enable richer post-hoc audit / "show me exactly what was wrapped" UX, at the cost of permanent per-UTXO bytes. [feedback_utxo_vs_tx_bytes.md](../../.claude/projects/-home-lunfardo-go-src-github-com-lunfardo314-proxima/memory/feedback_utxo_vs_tx_bytes.md) argues against; default = don't.
3. **Should chainLock-locked sigLock-like balances also be eligible?** ChainLock outputs can be effectively dormant. Tempted to say yes, but it adds an eligibility branch and a different ownership-recovery path. Defer to a phase 2.
4. **Eligibility timing window — fixed `constForceDelegationIdleSlots` or graduated?** A graduated curve (e.g. allowed after 1 day, mandatory by 7 days, sequencer profit margin rises with idle age) would reward holders who delegate before becoming eligible. Adds complexity; defer.
5. **Coordinated owner notification.** Out of protocol. Wallets / explorers can surface "your UTXO has been force-delegated by sequencer X" via the existing controller index. The protocol itself does not signal.
6. **Interaction with sequencer chain retirement.** If the wrapping sequencer retires while the wrap is mid-frozen, the wrap behaves like any delegation orphaned by its target — covered by the existing on-hold path. Verify in tests.

## Phasing

Roughly the minimum-viable slicing:

- **Phase A — EasyFL `forceDelegateLock` + Go typed wrapper.** No bypass yet; the constraint can be produced but the consumed lock can't be skipped. Useful as a self-contained unit landing.
- **Phase B — Stage 3 bypass.** `forceDelegationWrappedBy` helper + the one branch in `_runOutputs`. Tests: a wrap-only transaction settles when signed by a sequencer who is not the original owner; tampering with any eligibility check rejects the tx.
- **Phase C — Sequencer builder + opt-in flag.** `req_force_delegate.go`, scanning loop. Tests on the in-memory tangle: sequencer scans, picks up eligible UTXOs, wraps them, gets the coverage credit. Owner runs `askstop` and recovers.
- **Phase D — CLI rendering + eligibility surfacing.** UX-only.

Each phase ships as a reviewable unit. No phase requires a snapshot regen until B (because the validation rule changes).

## Backward compatibility

Per the `develop08` rule of thumb — this is a new ledger-level rule, ships in the same breaking-changes bucket as the rest. No migration shim; existing testnet state regenerates.
