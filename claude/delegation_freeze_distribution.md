# Delegation freeze-epoch distribution — spec

Status: design (2026-06-14). Supersedes the buggy `optimalFreezeEpoch` /
`selectDelegationsToFreeze` logic in `sequencer/task/proposal.go`. Diagnosis of
the old bug: `claude` memory `project_delegation_freeze_distribution_bug.md`.

## 1. Where the epoch is chosen

The unfreeze epoch of a delegation is **not** chosen by the delegator. `proxi
node dlg --epochs` only sets the per-output cap `MaxFrozenEpochs` (default `0` →
parsed back as the target's max). The **target sequencer** picks the actual
freeze duration each time it freezes a delegation, in
`sequencer/task/proposal.go`:

- `selectDelegationsToFreeze()` builds the per-epoch load and assigns a
  `freezeUntilEpoch` to every freezable delegation output of this target.
- `optimalFreezeEpoch()` is the per-delegation selection.
- `txbuilder_seq.FreezeDelegation(o, freezeUntilEpoch)` writes the chosen epoch
  into the successor (`MakeDelegationFreezeOutput`), bounded to
  `[txEpoch, FreezeUntilMax]`.

## 2. Goal

Spread the **unfrozen amount** as evenly as possible across the reachable
epochs of a given sequencer, so that the coverage released per epoch (and the
matching drop of the sequencer's frozen-coverage) is smooth rather than
arriving in cliffs. "First ~K delegations occupy K distinct epochs before any
collision; thereafter ~1/K of the delegated amount unfreezes per epoch."

The old code balanced the **count** of delegations per epoch. Coverage
fluctuation is proportional to **amount**, not count, so the metric must change
to amount-weighting. This is the primary functional change.

## 3. Definitions

Let the target sequencer's inlined params (read via
`SeqTxBuilder.ChainDelegationParams()`) be:

- `S = chainEpochSlots`     (epoch length in slots, ∈ [500, 2000])
- `N = chainMaxFrozenEpochs` (∈ [8, 32]; default 20)

For the proposal at timestamp `ts`:

- `txEpoch = EpochFromSlotDirect(targetID, ts.Slot, S)` — current epoch.
- The **reachable window** is the absolute epochs `[txEpoch, txEpoch + N - 1]`,
  indexed by relative position `i ∈ [0, N-1]` (absolute epoch `txEpoch + i`).

Load vector **D** over the window:

```
D[i] = Σ TokenBalance(d)   over every delegation d of this target that is
       currently frozen with LastFrozenEpoch == txEpoch + i
```

"Currently frozen" = `IsInFrozenSlot(ts.Slot)` in the **effective state at the
proposal's tip** (see §5 — this is the crux of the fix). Candidates being
placed in this same proposal are *not* yet in `D`; they are added as they are
assigned (§4).

For a single delegation `d` being (re)frozen, its own reachable cap is

```
K(d) = FreezeUntilMax(ts) = txEpoch + d.MaxFrozenEpochs - 1   (absolute)
```

i.e. relative indices `[0, d.MaxFrozenEpochs - 1] ⊆ [0, N-1]`.

## 4. Selection rule

Two cases by the delegation's current `DelegateLockState.State`:

### 4a. First-time freeze — `State == DelegateLockStateUndef`

The delegation has never been frozen by this target; it has no established
phase. Choose the freeze duration to balance `D`:

```
choose i* = max { i ∈ [0, d.MaxFrozenEpochs - 1] : D[i] is minimal over that range }
freezeUntilEpoch(d) = txEpoch + i*
then D[i*] += TokenBalance(d)        // so later placements in this pass see it
```

This single rule subsumes the user's two-case formulation:

- If any reachable `D[i] == 0`, zero is the minimum, and `max`-index tie-break
  picks the **farthest empty epoch** — longest freeze, fills the window from the
  far end inward. Equivalent to "pick max-index zero".
- Once no reachable epoch is empty, the minimum is the least-loaded epoch and we
  pick the latest one tied for it — equivalent to "pick latest `argmin (D[k] +
  DN)`" (the `+DN` term is constant across candidates, so it does not change the
  argmin; it only describes the resulting peak height).

Selection must be restricted to `i ≤ d.MaxFrozenEpochs - 1` **before** taking
the argmin. Do not select globally and clamp afterwards — that is the secondary
latent bug (several small-cap delegations all clamp onto the same boundary epoch
while lower epochs sit empty).

### 4b. Continuation — `State == DelegateLockStateFrozen`, window elapsed

The delegation was frozen, its window has passed (`!IsInFrozenSlot(ts.Slot)`),
and it is being re-frozen. Do **not** rebalance — freeze for the full duration:

```
freezeUntilEpoch(d) = FreezeUntilMax(ts) = txEpoch + d.MaxFrozenEpochs - 1
```

Rationale: every delegation re-freezes with a constant period
`d.MaxFrozenEpochs`. With a constant period, the unfreeze epoch advances by
exactly one period each cycle, so `LastFrozenEpoch mod period` (its phase) is
preserved across cycles. The first-time placement (§4a) sets the phase once; the
safe-revocation window guarantees the target re-freezes promptly in the next
epoch, preserving it. Net effect on `D`: a continuation removes its old
contribution and re-adds it at the same epoch — zero perturbation to the
balance. So continuations must not run the balancer.

(`OnHold` outputs are revoked/master-controlled and are never freeze
candidates — `IsUnlockableByTargetForFreezing` already excludes them.)

## 5. `D` must be slot-accurate (the core fix)

The selection rule is correct only if `D` reflects **all freezes already
committed in the current slot's milestone chain**, not just the committed
baseline branch.

Old behavior: `selectDelegationsToFreeze` reads `p.StateReader()` =
`BaselineSugaredStateReader()` = the **previous slot's committed branch**.
Within one slot, milestone `M2` cannot see the freezes done by `M1`: those
outputs are not in the baseline yet, and `M1`'s just-frozen delegations are
dropped from `M2`'s candidate set by `IsConsumedInThePastPath`. So every
milestone rebuilds `D` from the same stale snapshot and `optimalFreezeEpoch`
returns the same top epoch each time. The spread advances ~one epoch per slot;
any burst of freezes within a slot collides. This is the dominant cause of the
observed collisions.

**Requirement:** `D` must reflect all freezes already done in the current slot,
not just the committed baseline.

### Chosen approach: a small write-through `D` cache on the Sequencer

Rebuilding `D` per proposal from a trie scan is both stale (baseline lags a
slot) and expensive (thousands of delegations per target × multiple proposers ×
proposals every ~12 ticks). Instead the Sequencer keeps `D` in memory and
maintains it from its own decisions.

Key property that makes this safe and accurate: **the target sequencer is the
single writer of freezes for its own target** — only the target can
freeze/revoke its delegations. So this is the sequencer caching *its own*
output, not a cache of externally-mutated state. And `D` is **optimization
only**: `FreezeDelegation` bounds the result to `[txEpoch, FreezeUntilMax]`, so a
stale `D` yields a valid, slightly-less-even freeze — never a validity/consensus
bug. This is the deliberate, narrow exception to the no-cache rule
(`feedback_cache_and_refcount.md`): that rule guards correctness-critical caches
of multi-writer external state; this is single-writer and consequence-free if
wrong.

Cache contents: just `D` as `map[uint32]uint64` (absolute epoch → frozen amount;
≤ a few live entries) plus `lastRebuiltEpoch`. No per-delegation map — the delta
is read straight from the accepted tx (see below). Tiny regardless of delegation
count. Guarded by its own brief `RWMutex` (never hold it across a trie scan — the
`ownMilestonesMutex` lock-convoy lesson).

Mutation events:

- **Own milestone accepted** (hook where `ownMilestones` is maintained — *not* at
  tentative proposal time, since only one of the parallel proposers' milestones
  wins; updating on a tentative assignment would pollute `D` with freezes that
  never happened). For each delegation transition in the accepted tx, derive the
  delta from the tx itself (it carries both the consumed/old and produced/new
  delegation output): `D[oldFrozenEpoch] -= amt` (if it was frozen),
  `D[newFrozenEpoch] += amt`; a revoke (→ `OnHold`) just subtracts.
- **Periodic full rebuild** (once per epoch, and on startup) from the LRB:
  `IterateDelegatedOutputs(target)`, sum `TokenBalance` per `LastFrozenEpoch` of
  the still-frozen ones. Corrects any drift from orphaned own-milestones. Drop
  entries with epoch `< txEpoch` lazily.

No cache action is needed when a *new* delegation appears (not in `D` until first
frozen) or when a freeze window merely *elapses* (the entry stays at its epoch
until the delegation is re-frozen or revoked).

`selectDelegationsToFreeze` then reads the window `[txEpoch, txEpoch+N-1]` from
the cached `D` and credits each assignment it makes within the pass (so multiple
first-time delegations in one proposal still spread), exactly as today — but
against an accurate, cheap `D`.

#### Fallback (if the cache is deferred)

Baseline-only + amount + clamp fix + in-pass accounting. Cheap to ship but does
**not** fix cross-milestone collisions in a slot (the dominant bug); acceptable
only when at most one delegation freezes per slot. Floor, not the final form.

## 6. Amount used

`D` and the per-delegation contribution use `Output.TokenBalance()` (the
coverage a delegation contributes when unfrozen, and the magnitude of the
release at its unfreeze epoch). This matches the existing candidate sort key.
Accrued inflation is ignored (second-order).

## 7. Assumptions and known approximations

- **Uniform caps.** The phase-preservation argument (§4b) is exact only when all
  delegations of a target share one period. Mixed `MaxFrozenEpochs` make periods
  differ; balancing is then best-effort and phases of short-cap delegations
  drift relative to the `N`-wide window. Acceptable — the amount-balancer still
  reduces cliffs; document, don't over-engineer.
- **Prompt re-freeze.** Phase preservation assumes the target re-freezes in the
  epoch immediately following the elapsed window (guaranteed by the safe-
  revocation window). A missed window lets the master revoke — out of scope.

## 8. Worked example (fresh sequencer, N=20, equal amounts)

`D` starts all-zero. First-time delegations arrive (whatever the milestone/slot
spread, as long as `D` is slot-accurate per §5):

| arrival | reachable min | i* (max-index zero) | freezeUntilEpoch |
|---------|---------------|---------------------|------------------|
| 1       | 0 everywhere  | 19                  | txEpoch+19       |
| 2       | 0 in [0..18]  | 18                  | txEpoch+18       |
| 3       | 0 in [0..17]  | 17                  | txEpoch+17       |
| …       | …             | …                   | …                |
| 20      | 0 only at 0   | 0                   | txEpoch+0        |
| 21      | all equal     | 19 (latest min)     | txEpoch+19       |

First 20 occupy 20 distinct epochs; #21 lands on the now-least-loaded (all
equal → latest) epoch. With unequal amounts, #21+ track `argmin D`.

## 9. Implementation touch points

- `sequencer/` (new, e.g. `delegation_load.go`) — the `D` cache on the Sequencer:
  `map[uint32]uint64` + `lastRebuiltEpoch` + `RWMutex`; `RebuildFromLRB(target)`
  (per-epoch / startup full scan via `IterateDelegatedOutputs`); `ApplyMilestone`
  (per accepted own milestone, derive freeze/revoke deltas from the tx);
  `Snapshot(txEpoch, N)` returning the reachable window for the proposer.
- `sequencer/own_milestones.go` / wherever own milestones are accepted — call
  `ApplyMilestone` at acceptance (not at tentative proposal).
- `sequencer/task/proposal.go`
  - `selectDelegationsToFreeze()` — read `D` from the cache snapshot; split
    candidates into first-time vs continuation (§4a/§4b); credit in-pass
    assignments locally.
  - `optimalFreezeEpoch()` — take amounts and the per-delegation reachable cap;
    restrict argmin to `[txEpoch, cap]` before selecting (drop the
    `min(epoch, maxPossible)` clamp).
- `sequencer/txbuilder_seq/txbuilder_seq.go` — `FreezeDelegation` unchanged; it
  already honors a passed `freezeUntilEpoch` in `[txEpoch, FreezeUntilMax]` and
  falls back to max otherwise (matches continuation default).

## 10. Open questions

- Rebuild cadence: once per epoch is the proposal. Confirm a convenient hook
  (slot-edge / branch acceptance) and whether startup needs an eager rebuild
  before the first freeze.
- Orphaned own-milestones between rebuilds drift `D` slightly. Tolerated
  (optimization-only); the per-epoch rebuild corrects it. Revisit only if drift
  proves visible.
- Hash-based alternative considered and rejected: assign phase =
  `hash(chainID) mod N`, re-anchored every freeze — fully stateless, no `D`
  needed. Rejected because it is **amount-blind**: a few large delegations
  hashing to the same epoch reproduce the coverage cliff the design is meant to
  remove.

## 11. Test plan

- Unit (ledger/sequencer task): feed a synthetic `D` and assert §4a picks
  max-index argmin within the cap; assert the clamp bug is gone (small-cap
  delegations do not pile on the boundary while lower epochs are empty).
- Continuation: a `Frozen`-state, window-elapsed delegation freezes to
  `FreezeUntilMax` and does not perturb `D`.
- Cache: `ApplyMilestone` moves a delegation's amount from old to new epoch and
  drops it on revoke; `RebuildFromLRB` reconstructs the same `D` from scratch.
- Slot-accuracy: two freezes in the same slot (two milestones) land on distinct
  epochs (regression for the stale-baseline bug — now covered by the write-through
  cache).
- Testnet: pull `delegateTarget` outputs for one sequencer, parse each
  `delegateLockState.LastFrozenEpoch`, confirm spread across epochs and amount
  balance.
```
