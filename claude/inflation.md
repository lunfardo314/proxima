# Proxima Inflation Model

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
The upper bound (`branchInflationBonusBase`) follows a schedule:

| Period | Bonus per slot | APR (approx) |
|--------|---------------|--------------|
| Bootstrap grace (slots 0-8503, ~1 day) | 5,000,000 | ~1.5% |
| Month 0 (after bootstrap) | 65,000,000 (= 5M * 13) | ~19% |
| Month 1 | 60,000,000 (= 5M * 12) | ~18% |
| ... | decreases by 5M/month | ... |
| Month 11 | 10,000,000 (= 5M * 2) | ~3% |
| Month 12+ (tail, permanent) | 5,000,000 | ~1.2% |

The monthly boundaries use `constSlotsPerMonth = 255,118` (not exactly 30 days of 8,437 slots).

### Combined Rate

| Period | Chain APR | Branch APR | Total APR |
|--------|-----------|------------|-----------|
| Year 0 (effective) | ~10% | ~19% → 1.2% | ~30% → 11% |
| Year 1 | ~9.2% | ~1.2% | ~10.4% |
| Year 2 | ~8.4% | ~1.1% | ~9.5% |
| Year 3+ | slowly declining | ~1% | slowly declining |

Actual year 0 total inflation: ~22.5% (weighted average of the declining branch bonus).
After year 1, total inflation settles around 10% and slowly declines.

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

When the branch bonus changes within a step (at bootstrap boundary slot 8504, or at monthly
boundaries every 255,118 slots), the step is split into sub-segments with constant bonus.
Each sub-segment is processed sequentially, carrying over the updated supply.

Verified: step=1 vs step=30 days over 60 days gives relative error of 0.00000006%
(640K tokens out of 10^15, pure integer rounding).

## EasyFL Definitions

All inflation formulas are defined in `ledger/def/inflation.easyfl`:

- `constSlotInflationBase`: 33,000,000 — maximum one-slot inflation of the total initial supply
- `minimumInflatableAmount0`: `InitialSupply / constSlotInflationBase` = 30,303,030
- `chainInflationOneSlot(amount, slot)`: `amount / (M0 + slot)`
- `chainInflationMultiStep(amount, slot, nSlots)`: `nSlots * chainInflationOneSlot(amount, slot)`
- `branchInflationBonusBase(slot)`: upper bound of branch bonus at given slot
- `constSlotsPerMonth`: 255,118
- `constBranchInflationBonusBaseTail`: 5,000,000
- `constBranchInflationBonusBaseGenesisSlopeMonths`: 12

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

Full inflation emulation with tabular output, per-year summary, and PNG chart generation.
Does not require a running node (uses default ledger parameters).

```bash
proxi util inflation_emulation [<n_slots>] [<step_days>]
```

Parameters:
- `n_slots`: number of slots to simulate (default: 10,000,000 = ~3.25 years)
- `step_days`: computation step in days (default: 10)

Flags:
- `--chart`: generate `inflation_rates.png` with three lines (Total, Chain, Branch APR over months). Off by default.

Output:
- Per-step table with Year, Month, Slot, BranchInflation, ChainInflation, TotalInflation, ProformaSupply, and annualized rates
- Summary: final supply, total inflated, percentage increase, elapsed time
- Per-year actual inflation rates

Examples:
```bash
# Default: 10M slots, 10-day step
proxi util inflation_emulation

# 5 years with 30-day step (fast, ~40 data points)
proxi util inflation_emulation 15000000 30

# First year only, 1-day step (detailed, ~365 data points)
proxi util inflation_emulation 3079505 1

# Generate PNG chart
proxi util inflation_emulation 10000000 10 --chart
```

The emulation assumes the entire supply is inflated each slot (upper bound projection).
Coverage bounds are ignored. This gives the maximum possible inflation rate.

## Overflow Analysis: uint64 Supply Limit

Token amounts are stored as `uint64`. The practical question is how long before the supply
approaches 2^63 (≈ 9.22 × 10^18), the signed interpretation boundary.

**Closed-form for supply at slot s** (upper bound: all supply inflated + max branch bonus every slot):

```
supply(s) = A0·(M0+s)/M0 + B·(M0+s)·ln((M0+s)/M0)
```

For large s >> M0 this simplifies to:

```
supply(s) ≈ s · (A0/M0 + B·ln(s/M0))
         = s · (3.3×10^7 + 5×10^6 · ln(s/3×10^7))
```

**Chain inflation alone** reaches 2^63 at s = M0 · (9223 - 1) ≈ 2.8 × 10^11 → **~91,000 years**.

**Chain + branch combined** (solving numerically):
- At s = 1.2 × 10^11 (~39,000 yr): supply ≈ 8.9 × 10^18 — just under 2^63
- At s = 1.25 × 10^11 (~40,500 yr): supply ≈ 9.3 × 10^18 — just over 2^63

**Result: ~40,000 years** to reach 2^63 under the worst-case upper bound.

**Practical slot limit**: slots are `uint32`, max ≈ 4.3 × 10^9 ≈ **1,394 years**.
At max uint32 slot the supply is:
- Chain part:  ~1.4 × 10^17
- Branch part: ~1.1 × 10^17
- Total:       ~2.5 × 10^17 = **2.7% of 2^63**

**Conclusion**: uint64 overflow is not a concern. The uint32 slot range (~1,394 years) is
exhausted long before supply approaches 2^63. At the slot limit the supply is under 3% of 2^63.

### Source Code

| File | Purpose |
|------|---------|
| `ledger/def/inflation.easyfl` | EasyFL inflation formulas (on-chain rules) |
| `ledger/inflation_fun.go` | Go wrappers for EasyFL inflation functions |
| `proxi/util_cmd/inflation.go` | `proxi util inflation` CLI command |
| `proxi/util_cmd/inflation_emulation.go` | `proxi util inflation_emulation` CLI command + chart |
