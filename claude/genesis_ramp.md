# 5 % genesis, and two emission schedules to go with it

Status: **exploration, nothing implemented, nothing chosen.** Written 2026-08-15 for the fair
launch plan (`.internal/launch_v2.md`). Supersedes the scrapped genesis-lock idea — §7.

Halve the genesis share from 10 % to **5 %** of the target supply. That alone would put the
5/12 crossing at 17 days, far too soon for a participating community to exist, so the amount
minted per transit stops being constant and grows with the slot. Two shapes are worked out
below; **B is the better one**, and the difference is why. B has one free dial, `L`, and both
values worth considering are carried through.

Both keep `I` = 50 M, `R` = 950 M, the 1 B ceiling and the 4-slot pace.

---

## 1. The two candidates

**A — ramp from zero.** `A(slot) = min(k × slot, A_max)`, `k` = 1115 motes/slot,
`A_max` = 1250 PROX, flattening on day 133.

**B — flat, then ramp.** `A(slot) = A1` up to slot `L`, then `A1 + (slot − L) × k2`. No cap;
it grows until `R` runs out. `A1` is set so the flat phase alone reaches `5I/7` exactly at `L`,
which makes `L` the 5/12 crossing itself. Two values of `L` are worth having on the table.

| | flat A=1000, I=100 M | A: ramp from 0 | **B, L=45 d** | **B, L=60 d** |
|---|---|---|---|---|
| genesis share | 10 % | 5 % | **5 %** | **5 %** |
| 5/12 — founder can no longer stop the network | 33.9 d | 60.0 d | **45.1 d** | **60.5 d** |
| 50 % — mined overtakes genesis | 47.4 d | 71.0 d | **62.4 d** | **82.4 d** |
| 7/12 — ledger runs without the genesis capital | 66.4 d | 84.0 d | **84.5 d** | **108.2 d** |
| emission complete | 426.7 d | 426.7 d | **547.5 d** | **547.7 d** |
| reward, day 1 | 1000 | **9.4** | **375** | **280** |
| reward at the end | 1000 | 1250 | 1350 | 1498 |
| acceleration over the run | 1× | 133× | **3.6×** | 5.4× |
| month-1 emission | 63 M | 4 M | 23.7 M | 17.7 M |
| transits over the run | 900 000 | 900 135 | 1 154 958 | 1 155 301 |
| end-state mined share | 90 % | 95 % | **95 %** | **95 %** |

Constants: `L=45 d` → `A1` = 375 PROX, `k2` = 230 motes/slot, ramp starts slot 379 688.
`L=60 d` → `A1` = 280 PROX, `k2` = 296 motes/slot, ramp starts slot 506 250.

## 2. Why B is better

**In B the flat phase and the bootstrap phase are the same interval.** `A1` is chosen so that
the flat emission alone reaches `5I/7` exactly at `L`, which makes the schedule
self-describing: *the reward is flat while the founder still controls the network, and starts
growing the moment it does not.*

That alignment is not decoration. It removes the one real hazard in A.

**A's schedule assumes miners it may not attract.** Every date in either column is emission at
`A(slot)/4`, which holds only while somebody actually mines every ~4 slots. A pays 9.4 PROX on
day one. If that attracts nobody, transits stop, emission stalls, and the schedule slides
*right* — lengthening the centralised period, the opposite of the intent, and self-reinforcing
while it lasts. B pays a flat 280–375 PROX through exactly the interval where that failure
would hurt most, and asks nothing of a reward curve.

**A's early-miner penalty is 133×**, smallest precisely when the network is least proven. B's
is 3.6–5.4×, and its opening reward is 30–40× A's.

**A rising reward also sustains the tail.** As difficulty climbs with hashrate, a growing `A`
gives miners a reason to keep going rather than stop, which is where a 1.5-year emission needs
the help.

### Choosing `L`

`L` is a single dial and it trades one thing against everything else.

**`L = 60` maximises the runway.** Two months before the founder can no longer hold the
network is the most time a community has to form, and forming one is the entire reason the
genesis share can be halved at all.

**`L = 45` is better on every other axis.** Full decentralisation arrives at 84.5 days rather
than 108 — near candidate A's 84.0, and much nearer today's 66.4 — so the genesis capital stops
being load-bearing for liveness six weeks sooner. The opening reward is higher (375 against
280), the acceleration is milder (3.6× against 5.4×), and month-one emission is larger, which
is a real signal to early miners that the chain is worth mining.

The question is only whether six weeks is enough for delegated capital to accumulate. Nothing
measures that yet, which is what makes it a testnet question rather than an argument.

## 3. Why the dates land where they do

**A couples its three early milestones rigidly.** With `A` linear from zero the cumulative is
quadratic, so a milestone at mined `M` arrives at `t ∝ √M` and the three sit in fixed ratio
`0.845 : 1 : 1.183`. Pinning 5/12 at 60 days *forces* 50 % at 71 and 7/12 at 84; `A_max` moves
only the tail. There is nothing to tune.

**B separates them**, because its flat phase and ramp phase have different shapes. `A1` sets
the first milestone, `k2` sets the tail, and `L` decides where the schedule changes character —
which is why moving `L` from 60 to 45 days pulls 7/12 in by 24 days while costing only 15 on
the first milestone.

**All of them compress the handover relative to today.** The span from 5/12 to 7/12 is 1.96×
under the current flat schedule, 1.40× under A, 1.87× at `L=45` and 1.79× at `L=60` — so the
launch stays centralised longer but passes through the transition more quickly once it starts.

## 4. What the 5 % genesis buys

**Half the founder's share**, and 95 % of supply mined instead of 90 %. That is the point of
the exercise.

**A real runway, without the share.** `claude/delegation_scalability.md` establishes that more
than 7/12 of supply must be actively participating or branches stop being produced, and the
genesis capital is what covers that single-handedly until enough delegated mined capital
exists. Halving the genesis share halves that runway — 5/12 would arrive in 17 days on a flat
schedule, which is not long enough for a community to form from nothing. The ramp buys it back
to 45–60 days *while still halving the share*. That is the trade the whole design makes.

**A smaller early land-grab.** `fairlaunch-research.md` §9 expects the top actor to take
30–50 % of month-one emission. B emits 18–24 M PROX in month one against ~63 M on the current
schedule, so whatever concentration occurs applies to a much smaller slice.

## 5. What B costs

**7/12 lands late, and how late is what `L` decides.** 108 days at `L=60`, 84.5 at `L=45`,
against 66 today. The genesis capital stays load-bearing for liveness that whole time, and it
is the real price of opening flat.

**Emission accelerates rather than decays** — 0.59 M PROX/day rising to 3.18 at `L=60`, or 0.79
to 2.85 at `L=45`, then a hard stop at exhaustion. This is the inverse of every schedule a reader
knows, and someone will ask why late miners are paid more. It is defensible — emission
accelerates as the network decentralises — but it belongs in the launch document rather than
being discovered.

**28 % more transits**, 1.15 M against 900 000 at either `L`, so 28 % more mine transactions
and mine-chain transitions over a longer run. Marginal, and the price of the longer tail at a
lower average `A` (823 against 1055).

## 6. Mechanics

Either way `constMineAmount` stops being a constant and becomes a function of the transaction
slot. For B:

    _mineAmount = if txSlot <= constMineRampStartSlot
                     constMineAmountBase
                  else
                     constMineAmountBase + (txSlot - constMineRampStartSlot) * constMineAmountPerSlot

Three constants replace one — for `L=60`, `constMineAmountBase` = 280 000 000 motes,
`constMineRampStartSlot` = 506 250, `constMineAmountPerSlot` = 296 motes; for `L=45`,
375 000 000 / 379 688 / 230 — and every current
reference to `constMineAmount` in `lock_mine.easyfl` becomes a call: the inflation equality,
the payout cap and the 1 % fee cap. `R`'s decrement follows the same value, and the terminal
condition `R_pred < A` still works with `A` varying. `constMineRemainingInit` goes to 950 M
and `constInitialSupply` to `constTargetBaseSupply/20`.

Integer arithmetic throughout; `k2` is a whole number of motes per slot and the ramp start is a
slot number, so nothing rounds.

A is one constant cheaper (no base, no ramp start) but that is not a reason to prefer it.

## 7. Why the genesis lock was dropped

The earlier idea was to bind the genesis capital to its chain with a balance floor. It failed
on two counts:

- **fault tolerance.** The genesis capital has to be split across ~5 sequencers on separate
  machines immediately, or the launch has a single point of failure. A rule pinned to one chain
  ID does not generalise to five without becoming a whitelist of chains, and its whole appeal
  was being two lines in the chain constraint;
- **it did not decentralise anything.** One entity holding 10 % under a covenant is still one
  entity holding 10 %. The lock changed what the founder could *do* with the stake, not the
  fact of it.

Halving the stake addresses the second directly, which is why it is the better lever.

## 8. Open

- **How long a runway does the community actually need?** Everything else follows from it —
  `L` is that number, and `A1` follows from `L`. Nothing measures it yet; the pre-launch testnet
  should show how fast delegated capital accumulates.
- **Should B's ramp cap?** As written it grows until `R` is exhausted, ending at 1350–1500
  PROX. A cap would bound the acceleration at the cost of a fourth constant and a later finish.
- **`L` at the 5/12 crossing is a choice, not a constraint.** It makes the schedule
  self-describing, but `L` could sit earlier or later if there is a reason.
- **45 or 60 days** is the live decision, and it reduces to whether six weeks is enough for
  delegated capital to accumulate. Everything else favours 45.
- **Nothing here is measured.** These are projections at the target pace, for a network that
  has never run a launch.
