# Proxima Inflation Model

> **QUEUED → `overview/incentives.md`** — The two components of inflation: chain and branch.
> Rewritten there, then archived. See `claude/kb_reorg.md`.

## Two Components of Inflation

Proxima inflation consists of two independent components:

### Chain Inflation
Rewards for holders of chained accounts. Deterministic, proportional to the token balance.

Formula per slot: `chainInflationOneSlot(A, s) = A / (M0 + s)`

where `A` is the token amount and `M0 = minimumInflatableAmount0 = InitialSupply / constSlotInflationBase`.

With default parameters: `M0 = 1,000,000,000,000,000 / 33,000,000 = 30,303,030`.

Chain inflation rate starts at ~10.16% APR and slowly decreases over time as the supply grows
relative to M0. The decline is gradual: ~9.2% at year 1, ~8.5% at year 2, ~7.7% at year 3.

### Branch Inflation Bonus
VRF-based pseudo-random bonus awarded to sequencers that produce branch transactions.
The upper bound (`branchInflationBonusBase`) is a **flat constant from genesis**:
`constBranchInflationBonusBaseTail = 5,000,000` per slot. There is no bootstrap period.
Its APR starts at ~1.5% and declines purely as the supply grows (~0.3% after 30 years).

The earlier declining schedule — 65,000,000 (5M×13) at genesis, decreasing 5M/month down to
the 5M tail over the first year — was removed (breaking ledger change, 2026-06-14). The
helper constants `constSlotsPerMonth` and `constBranchInflationBonusBaseGenesisSlopeMonths`
and the function `branchInflationBonusBaseSchedule` were deleted with it.

### Combined Rate

Flat branch base → no genesis spike; total inflation starts ~11.9% and declines
monotonically as supply grows (upper-bound projection):

| Year | Chain APR | Branch APR | Total APR |
|------|-----------|------------|-----------|
| 0  | ~10.4% | ~1.6% | ~11.9% |
| 5  | ~6.9%  | ~1.0% | ~7.9%  |
| 10 | ~5.1%  | ~0.7% | ~5.8%  |
| 20 | ~3.4%  | ~0.4% | ~3.9%  |
| 29 | ~2.6%  | ~0.3% | ~2.9%  |

## Key Mathematical Property

The chain inflation formula has an exact multi-step property:

```
chainInflationMultiStep(A, s, N) = N * A / (M0 + s)
```

Each slot produces exactly `A / (M0 + s)` chain inflation regardless of intermediate compounding.
This is because after k slots the amount is `A * (M0+s+k) / (M0+s)`, and
`A * (M0+s+k) / ((M0+s) * (M0+s+k)) = A / (M0+s)` — the per-slot inflation is constant.

Key identity:
```
A + N*A/(M0+s) == (A+I) + (N-1)*(A+I)/(M0+s+1)
```
where `I = A/(M0+s)`. The final inflated amount is independent of step decomposition.

This allows computing inflation over arbitrarily large periods (months, years) in O(1)
without iterating slot by slot.

## Branch Inflation Closed-Form

When branch bonus B is constant over a segment, the slot-by-slot recurrence
`supply_{k+1} = supply_k * (M0+s+k+1)/(M0+s+k) + B` has the exact solution:

```
supply_S = A * (M0+s+S)/(M0+s) + B * (M0+s+S) * ln((M0+s+S)/(M0+s))
```

The total inflation for the segment is:
- Chain inflation: `N * A / (M0 + s)` (exact, via EasyFL)
- Branch inflation: `B * (M0+s+S) * ln((M0+s+S)/(M0+s))` (closed-form, accounts for chain compounding of branch bonuses)

The branch bonus base is now constant for all slots, so a step never straddles a bonus
change; `computeStepInfl` still splits at bonus-change boundaries generically, but
`findNextBonusChangeSlot` returns the step end immediately (single segment per step).

Verified: step=1 vs step=30 days over 60 days gives relative error of 0.00000006%
(640K tokens out of 10^15, pure integer rounding).

## EasyFL Definitions

All inflation formulas are defined in `ledger/def/inflation.easyfl`:

- `constSlotInflationBase`: 33,000,000 — maximum one-slot inflation of the total initial supply
- `minimumInflatableAmount0`: `InitialSupply / constSlotInflationBase` = 30,303,030
- `chainInflationOneSlot(amount, slot)`: `amount / (M0 + slot)`
- `chainInflationMultiStep(amount, slot, nSlots)`: `nSlots * chainInflationOneSlot(amount, slot)`
- `branchInflationBonusBase(slot)`: upper bound of branch bonus — flat `constBranchInflationBonusBaseTail` for all slots (`evalArg1($0, ...)` ignores the slot)
- `constBranchInflationBonusBaseTail`: 5,000,000

Go wrappers in `ledger/inflation_fun.go`:
- `lib.ChainInflationMultiStep(amount, inSlot, forSlots)` — evaluates EasyFL, returns uint64
- `lib.ChainInflationOneSlot(amount, inSlot)` — calls ChainInflationMultiStep with forSlots=1
- `lib.BranchInflationBonusBase(slot)` — returns max bonus for the slot

## CLI Tools

### `proxi util inflation`

Point calculator for chain inflation on a specific amount.

```bash
proxi util inflation <amount> [<n_slots>] [<start_slot>]
```

Requires a running node (uses `InitLedgerFromNode`).

Example: inflation on 1 billion tokens for 1000 slots starting at slot 0:
```bash
proxi util inflation 1000000000 1000 0
```

### `proxi util inflation_emulation`

Slot-by-slot emulation of supply growth, with a per-year table and two PNG charts.
Does not require a running node (uses default ledger parameters).

```bash
proxi util inflation_emulation [<years, default 10>]
```

Flags:
- `--dir`: directory the charts are written to (default `.internal`, which is gitignored)
- `--pace`: mean mining pace in slots per transit (default 4.7, the observed testnet figure)
- `--seed`: seed of the random draws, so a run reproduces (default 1)
- `--no-charts`: print the summary without writing charts

Supply is tracked as three pools, each carrying the chain inflation its own balance
earned, so they add up to the supply exactly:

| Pool | Contents |
|------|----------|
| bootstrap capital | the genesis supply and the inflation on it |
| branch bonus | the per-slot bonuses and the inflation on them |
| mined | the fair-launch emission and the inflation on it |

Charts:
- `supply.png` — the three pools stacked in PROX, months on the x axis
- `supply_shares.png` — the same pools stacked to 100% of supply, which is where the
  dilution of the bootstrap capital is visible
- `inflation_rate.png` — realized year-over-year supply growth, mining included, on a log
  y axis (the first year runs two orders of magnitude above the steady state)

Assumptions, printed in the run header:
- chain inflation on the whole supply every slot — an upper bound, since only chained
  outputs earn it; coverage bounds are ignored
- the branch bonus is drawn uniformly in [1, base] each slot, as the VRF does. This is
  **half** the base the earlier step-wise emulation assumed, which took the base itself
- mining pace is a shifted exponential with the given mean and the ledger's minimum pace
  as its floor, and stops when the mintable budget R_init is exhausted

The per-slot chain inflation is computed in Go rather than through the EasyFL evaluator,
which at tens of millions of slots would take hours; `checkChainInflationFormula` pins the
inlined formula to `ChainInflationOneSlot` at startup.

## Overflow Analysis: uint64 Supply Limit

Token amounts are stored as `uint64`. The practical question is how long before the supply
approaches 2^63 (≈ 9.22 × 10^18), the signed interpretation boundary.

**Closed form.** With a constant inflow `r` per slot on top of chain inflation, supply obeys
`A' = A/(M0+s) + r`, whose solution over a segment is

```
A(s2) = (M0+s2) · [ A(s1)/(M0+s1) + r·ln((M0+s2)/(M0+s1)) ]
```

Taking `r = B` (branch bonus) after emission ends, and `r = B + R_init/S_mine` during it.
This reproduces the per-slot emulation to within 0.03% at years 10 and 30.

Two conventions for `B` give two answers, and both are quoted here because the difference
is a factor of two in the dominant long-run term:

| `B` per slot | Meaning | 2^63 reached at |
|--------------|---------|-----------------|
| 5 × 10^6 (the base) | upper bound: the largest bonus the VRF can draw, every slot | **~40,000 years** |
| 2.5 × 10^6 (the mean) | what the VRF actually pays, uniform in [1, base] | **~57,000 years** |

Mined emission barely moves either figure: `R_init` = 9 × 10^14 motes is 0.5% of the supply
at the slot limit, though it dominates the first year.

**The binding limit is the slot counter, not the amount.** Slots are `uint32` and every
value is valid, so the last representable slot is 4,294,967,295 ≈ **1,395 years**. At that
slot the supply is ≈ 1.88 × 10^17 motes (188 billion PROX) — **2% of 2^63**, leaving 49×
headroom.

**Conclusion**: uint64 overflow is not a concern, and not by a small margin — the ledger's
own time representation is exhausted roughly 40× sooner than the amount range. Reaching
2^63 would require both a wider slot and tens of thousands of years.

### Source Code

| File | Purpose |
|------|---------|
| `ledger/def/inflation.easyfl` | EasyFL inflation formulas (on-chain rules) |
| `ledger/inflation_fun.go` | Go wrappers for EasyFL inflation functions |
| `proxi/util_cmd/inflation.go` | `proxi util inflation` CLI command |
| `proxi/util_cmd/inflation_emulation.go` | `proxi util inflation_emulation` CLI command + chart |
