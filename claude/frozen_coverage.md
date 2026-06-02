# Stem `frozen_coverage`: correct it to a cumulative state total

**Status: IMPLEMENTING.** Decisions locked with the user:
- **Option A** — derive the delta, no new `sequencer()`/stem args. `succ`/`pred`
  `frozenCoverage[0]` are already EasyFL-enforced by the chain constraint; the
  attacher derives the per-slot delta from its mutation set.
- **Go-enforced** stem value (like `coverageDelta`): no new EasyFL stem recurrence.
  Only the EasyFL change is swapping the `frozen < coverageDelta` sanity bound for
  `frozen <= totalSupply` (§5).

Consensus-critical (changes the ledger library → hardfork on develop08).
Explorer work is parked until this lands.

## 1. The problem

The stem aggregate `frozen_coverage` (stemLock arg `$5`) is **not** the total
tokens frozen by delegations in the ledger state. It is the *frozen part of the
branch's `coverage_delta`*:

- Computed in Go as `Σ AdjustedFrozenCoverage` over the sequencer outputs
  **consumed in the past-cone delta** (`core/vertex/past_cone.go:1154`,
  `ledger.Coverage` at `ledger/lock_delegate_util.go:86`).
- Returned alongside `coverage_delta` by `attacher.CoverageDelta()` and
  cross-checked in `enforceStemValues` (`core/attacher/check.go:137`).
- The only EasyFL enforcement is a **sanity bound**, not a recurrence:
  `frozenCoverage < coverageDelta` (`ledger/def/lock_stem.easyfl:107`). The
  source even labels it `frozen part of $4 (trustless stats)` (`:59`).

Consequences:
- It is **view-dependent**: only sequencers whose milestones land in *this*
  branch's delta are counted. A sequencer idle in the slot is missed.
- It is **not accumulated** like supply (`totalSupply == prev + slotInflation`,
  enforced at `lock_stem.easyfl:92`). It is recomputed per branch from the delta.

It looks right on node0 only because there is a single sequencer consumed every
slot. (node0 LRB sample: `frozen_coverage = 9_003_345_364`.)

## 2. Ground truth (correct but costly)

Each **sequencer chain tip** already carries, on-chain and trustlessly, the
cumulative frozen tokens targeting it. The amounts vector
(`ledger/amounts.go`) is `[tokenBalance(0), inflation(1), frozenCoverage[0](2),
frozenCoverage[1](3), …]`; the frozen-coverage **vector** spans
`maxFrozenEpochs` entries from index 2. Entry `i` = tokens still frozen for
epoch-offset `i`. Entry **0** = total currently frozen.

This vector is enforced on every sequencer transition by the chain constraint
recurrence (`ledger/chain.go:294-299`):

```
pred_i (epoch-adjusted) + sum_i = 2 * succ_i
  => succ_i = pred_adjusted_i + Σ(frozen-coverage deltas of produced delegations)
```

A freeze adds `+balance` across `[0,frozenEpochs)` (`MakeFrozenCoverageAmounts`,
`lock_delegate_util.go:340`); a revoke subtracts the remainder
(`MakeFrozenCoverageAmountDeltasForRevoking:327`); epoch crossings shift the
vector so expiries naturally drop out of entry 0.

**Therefore total frozen at LRB = Σ over all sequencer chain tips of
`frozenCoverage[0]`.** Correct, but requires a full chain-tip scan of the state
on every branch — too costly for the hot path.

## 3. Target design (accumulate, like supply)

Keep the prior stem value and move by a per-slot delta:

```
frozen_coverage(branch) = frozen_coverage(prev_branch) + Σ(per-transition deltas in the past-cone delta)
```

where a sequencer transition's delta is

```
d = succ.frozenCoverage[0] - pred.frozenCoverage[0]   (signed; raw index-2 values)
```

### Implementation: piggyback on the Mutations iteration

Rather than a per-transition `succ-pred` (which needs a predecessor lookup and is
fragile for non-`Vertex` past-cone entries), compute the same telescoped sum from
the branch's mutation set, exactly like `PastCone.Mutations()` (`past_cone.go:696`)
which already reads every relevant output via the type-agnostic
`vid.MustOutputAt(idx)`:

```
delta = Σ(produced-unspent SEQUENCER outputs' frozenCoverage[0])   // ADD tips
      - Σ(deleted baseline SEQUENCER outputs' frozenCoverage[0])   // DEL baseline tips
```

- **ADD side**: not-in-state vid, `producedIndices` (produced & unspent) — the new
  chain tips. Add `frozenCoverage[0]` for sequencer outputs.
- **DEL side**: in-state vid, output consumed by a not-in-state consumer — the
  baseline tips being spent. Subtract `frozenCoverage[0]` for sequencer outputs.

**Filter to sequencer outputs only** (chain output with a `sequencer()` constraint
at `SequencerConstraintFixedIndex`). Delegation outputs *also* carry
`frozenCoverage[0]` (it is mirrored onto both the delegation and its target
sequencer by the chain recurrence), so counting both would double-count; the
sequencer is the aggregator, matching the §2 ground truth "Σ along all sequencers".
Regular chains and foundries carry an all-zero frozen vector (enforced), so they
contribute 0 regardless.

### Why this is exactly the ground truth (telescoping)

Per sequencer, summing `d = succ.frozen[0]-pred.frozen[0]` over all its transitions
from origin (`frozenCoverage[0] = 0`) telescopes to its current tip's
`frozenCoverage[0]`. The ADD/DEL form above is the same sum regrouped: for a chain
active in the delta it contributes `finalTip − baselineTip`; idle chains contribute
0 (neither added nor deleted); new chains contribute `+tip`; killed chains
`−baselineTip`. Accumulated from genesis (`frozen_coverage = 0`, already enforced at
`lock_stem.easyfl:44`), `prevFrozen + delta` **equals Σ sequencer-tip
`frozenCoverage[0]`** — the §2 ground truth — without the scan.

Staleness is identical to the scan: an idle sequencer's expiry is realised only
when it next transits, and the scan would read the same stale tip. So the
accumulator is never *worse* than the scan, and provably agrees with it.

### Producer vs verifier (the incremental-cone subtlety) — SOLVED

The verifier is the milestone attacher of the built branch: its cone is flattened
and **includes** the branch tx, so `SequencerFrozenCoverageDelta(nil)` over it gives
the correct full delta. `frozen = BaselineFrozenCoverage + delta`.

The producer is the sequencer's **incremental** attacher, whose cone **excludes**
the branch tx (not built yet) and marks the extend-target milestone as *virtually
consumed*. Consequently its cone delta is missing exactly one term: the branch's
own produced milestone tip (the ADD of `chainOut.frozenCoverage[0]`). It still
contains the DEL of the baseline own-seq tip and all OTHER sequencers' ADD/DEL
(those aren't anchored). So:

```
producerConeDelta = verifierDelta − chainOut.frozenCoverage[0]
```

The fix (no exclude-chain needed): `buildStemLock` emits
`frozen = BaselineFrozenCoverage + a.FrozenCoverageDelta + chainOut.frozenCoverage[0]`,
which equals the verifier's value. The auto-compute fallback (distribute / simple
tests, single-tx cone) uses `prevStem.FrozenCoverage + (chainOut − chainIn).frozen[0]`
since there it directly consumes the baseline own-seq tip.

**Live-verified** on a standalone node (single sequencer): three delegations frozen
at different slots accumulated to `frozen_coverage = 7_002_560_082` = exact Σ of the
frozen delegation balances, zero stem-value mismatches, LRB advancing. Multi-
sequencer (endorsed-merge) agreement holds by the derivation above (OTHER sequencers'
deltas are identical in both cones) but is only provable on a multi-node testnet.

### Master-revoke is not a special case

The master can revoke only inside the safe-revocation window, which opens
*after* the frozen epoch ends — by then the freeze has already shifted out of
the sequencer's entry 0 (captured as a decrease at the crossing transition). So
a master-revoke tx (which does not transit the sequencer) does not need to, and
must not, move the frozen total. No double counting.

### Sign handling without signed encoding

Represent each transition's delta as two unsigned numbers, **at least one zero**:
`frozenIncrease`, `frozenDecrease`. The defining relation (pure uint64 add +
equality, no subtraction/underflow):

```
succ.frozenCoverage[0] + frozenDecrease == pred.frozenCoverage[0] + frozenIncrease
AND (frozenIncrease == 0 OR frozenDecrease == 0)
```

Past-cone aggregation mirrors `SlotInflation()` (`past_cone.go:1125`): iterate
new (not-in-state) vertices, and for each sequencer transition add its
`frozenIncrease`/`frozenDecrease` into running `incTotal`/`decTotal`. Then:

```
delta          = int64(incTotal) - int64(decTotal)
frozen_coverage = prev_frozen_coverage + delta
```

### Arithmetic-safety asserts (Go)

```
abs(delta) <= totalSupply
0 <= frozen_coverage <= totalSupply
```

(`frozen_coverage ⊆ supply` always holds since it is Σ frozen balances.)

## 4. The immutability wrinkle (decision needed)

Your spec stores the per-transition delta "as args of the `sequencer()`
constraint". But `sequencer()` is currently enforced **whole-constraint
immutable** across transit — `selfImmutableOnSuccessorIndex(selfBlockIndex)`
(`ledger/def/sequencer.easyfl:128`) requires byte-identical bytecode on the
successor. Its two args (`epochSlots`, `maxFrozenEpochs`) are deliberately
immutable. Per-transition `frozenIncrease`/`frozenDecrease` change every slot, so
they cannot live inside an immutable constraint unchanged. Three ways out:

- **Option A — no new args; attacher derives the delta.**
  `succ.frozenCoverage[0]` and `pred.frozenCoverage[0]` are *already*
  EasyFL-enforced (chain constraint §2). The attacher reads produced-succ and
  consumed-pred frozen[0] for each sequencer transition in the delta and sums.
  EasyFL change = only the stem bound (§5). Minimal; leverages existing
  enforcement; costs one predecessor lookup per sequencer tx in the delta.
  *Closest to "keep it minimal"; does not store the delta explicitly.*

- **Option B — new tiny mutable constraint slot.** A `seqFrozenDelta(inc,dec)`
  constraint (its own index) self-checks
  `succ.frozen[0]+dec == pred.frozen[0]+inc` and `inc==0 ∨ dec==0`. `sequencer()`
  stays immutable. Attacher reads the args directly (no predecessor lookup).
  Adds one constraint kind + an output index.

- **Option C — split `sequencer()` immutability.** Keep `epochSlots`/
  `maxFrozenEpochs` immutable via a *partial* check (compare only those args
  pred↔succ) and add mutable `inc`/`dec` args. Most invasive to `sequencer()`.

**Recommendation: Option A.** The values are already enforced and derivable;
storing them again (B/C) is redundant state and new machinery for a hot-path
micro-optimisation. If profiling later shows the predecessor lookup matters,
promote to B.

## 5. EasyFL change required either way

The existing sanity bound `frozenCoverage < coverageDelta`
(`lock_stem.easyfl:107-110`) becomes wrong: a cumulative frozen total can exceed
a single slot's coverage delta. Replace with a state-total sanity bound:

```
require(lessOrEqualThan(uint8Bytes(frozenCoverage), uint8Bytes(totalSupply)),
        !!!frozen_coverage_must_not_exceed_total_supply)
```

No new stem recurrence is strictly required (frozen_coverage stays Go-enforced
on the stem via `enforceStemValues`, exactly as `coverageDelta` is today). A full
EasyFL stem recurrence would need the signed slot delta on the stem (e.g. two
extra stem args) — deferred unless we want frozen trustlessly verifiable from the
stem chain alone, parallel to supply. Flag for discussion.

## 6. Touchpoints (Option A)

Producer / consumer / display all read the *prev branch's* frozen_coverage and
add the slot delta:

- `core/vertex/past_cone.go` — new `FrozenCoverageDelta()` (signed, or inc/dec
  pair) mirroring `SlotInflation()`; reads succ/pred `frozenCoverage[0]` for each
  sequencer transition in the delta.
- `core/attacher/attacher.go` — expose it (parallel to `SlotInflation()`),
  fold prev-branch frozen + delta in `wrapup.go`.
- `core/attacher/check.go` `enforceStemValues` — compute
  `prevFrozen + delta` instead of taking the past-cone `frozen` from
  `CoverageDelta()`; add the abs/≤supply asserts.
- `core/attacher/attacher.go` `CoverageDelta()` / `past_cone.go CoverageDeltaRaw`
  — the `frozen` return value is now unused for the stem; decide whether to drop
  it or keep for diagnostics. (`coverage_delta` itself is unchanged.)
- `sequencer/task/proposer.go:25,57-58` and
  `sequencer/txbuilder_seq/txbuilder_seq.go` (`StemAggregates.FrozenCoverage`,
  `buildStemLock`) — producer must emit the accumulated value so the produced
  stem matches `enforceStemValues`.
- `ledger/def/lock_stem.easyfl` — swap the bound (§5).
- `ledger/multistate/roots.go:603`, `json.go:71` — the human summary currently
  prints `frozen` as `% of coverageDelta`; switch to `% of supply` (now
  meaningful).
- Genesis: `frozen_coverage = 0` already enforced; no change.

## 7. Hardfork / migration

Changing `lock_stem.easyfl` (and, for B/C, `sequencer.easyfl`) changes the
library hash → new ledger upgrade. develop08 is already a breaking line
(`project_v080_breaking`); confirm we just break rather than carry a shim.
Existing branches' `frozen_coverage` values were computed under the old
semantics; after cutover the accumulator re-derives from the new baseline (or we
seed `prev_frozen_coverage` from a one-time Σ sequencer-tip scan at the upgrade
boundary). Decide seeding vs. clean restart.

## 8. Open decisions for the user

1. **Option A vs B vs C** for where/whether to store the per-transition delta
   (recommend A).
2. **Stem recurrence**: Go-enforced only (like coverageDelta, minimal) vs. full
   EasyFL recurrence with extra stem args (max trustlessness, parallel to
   supply).
3. **Migration seeding**: one-time Σ sequencer-tip scan to seed the accumulator
   at the upgrade slot, vs. clean restart from a fresh genesis/snapshot.
