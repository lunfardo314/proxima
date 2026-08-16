# 5 % genesis with a ramped mine amount

Status: **exploration, nothing implemented.** Written 2026-08-15 for the fair launch plan
(`.internal/launch_v2.md`). Supersedes the scrapped idea of locking the genesis output — see
§6 for why that died.

Shrink the genesis share from 10 % to **5 %** of the target supply, and let the amount minted
per transit **grow linearly with the slot** until it flattens, so that losing 5/12 still takes
about two months rather than five weeks.

---

## 1. The numbers

    A(slot) = min(k × slot, A_max)      k = 1115 motes/slot,  A_max = 1250 PROX

Genesis `I` = 50 M, mintable `R` = 950 M, target ceiling unchanged at 1 B. Pace stays 4 slots,
so emission is `A(slot)/4` per slot. The ramp reaches `A_max` at **slot 1 121 076, day 133**.

| Milestone | mined | flat A=1000, I=100 M | **ramp, I=50 M** |
|---|---|---|---|
| founder can no longer stop the network (5/12) | 5I/7 | 33.9 d | **60.0 d** |
| mined overtakes genesis (50 %) | I | 47.4 d | **71.0 d** |
| ledger runs without the genesis capital (7/12) | 7I/5 | 66.4 d | **84.0 d** |
| emission complete | R | 426.7 d | **426.7 d** |
| end-state mined share | | 90 % | **95 %** |

Total transits **900 135** against 900 000 today, average `A` over the run **1055 PROX**. The
tail is unchanged to within a day, which is the point: the ramp redistributes emission inside
the launch without lengthening it.

`A` over time: 9.4 PROX on day 1, 66 by day 7, 282 by day 30, 564 by day 60, 847 by day 90,
1250 from day 133 on.

## 2. Why the dates land where they do

With `A` linear from zero, cumulative emission is **quadratic**, so a milestone at mined `M`
arrives at `t ∝ √M`. That has two consequences worth knowing before tuning anything.

**`k` alone fixes all three early milestones.** They all fall inside the ramp, so their dates
are in fixed ratio `√(5/7) : 1 : √(7/5)` = `0.845 : 1 : 1.183`. Pinning 5/12 at 60 days forces
50 % at 71 and 7/12 at 84. They cannot be moved apart without abandoning the linear shape.
`A_max` moves only the tail.

**The transition is quicker once it starts.** Under flat `A` the span from 5/12 to 7/12 is
1.96×; under the ramp it is 1.40× (60 → 84 days). So the launch stays centralised longer but
passes through the handover faster — 24 days from "cannot be stopped" to "does not need the
genesis capital", against 32 today.

## 3. What it buys

**Half the founder's share.** 5 % instead of 10 %, and 95 % of supply mined instead of 90 %.
That is the headline, and it is what the whole exercise is for.

**A real runway.** `claude/delegation_scalability.md` establishes that more than 7/12 of supply
must be actively participating or branches stop being produced, and the genesis capital is what
covers that single-handedly until enough delegated mined capital exists. Halving the genesis
share halves the runway — 5/12 would arrive in 17 days on the flat schedule, which is not long
enough for a participating community to form from nothing. The ramp buys it back to 60 days
*while still halving the share*. That is the trade the whole design is making.

**A smaller early land-grab.** `fairlaunch-research.md` §9 expects the top actor to take
30–50 % of month-one emission. Under the ramp, month one emits ~4 M PROX rather than ~63 M, so
whatever concentration happens there applies to a far smaller slice.

## 4. What it costs, and the risk to watch

**Early miners earn far less than late ones.** 9.4 PROX per transit on day 1 against 1250 at
steady state — a 133× spread, and the reward is smallest exactly when the network is least
proven. This inverts the usual launch incentive, and it should be stated in the launch document
rather than discovered by the first miners.

**The schedule assumes the pace is maintained, and the ramp is where that assumption is
weakest.** Every date above is emission at `A(slot)/4` per slot, which holds only while
somebody is actually mining every ~4 slots. If the early reward is too small to attract anyone,
transits stop, emission stalls and the whole schedule slides right — and it slides in the wrong
direction, extending the centralised period.

Two things say it should hold. Difficulty retargets down to its floor when hashrate is absent,
so the cost of a transit collapses along with the reward — at `E = 10` a solve is ~1024
attempts. And the tag-along fee is capped at 1 % of `A`, which at 9.4 PROX is 0.094 PROX,
comfortably above what a sequencer will charge, so a transit remains payable. Mining stays
viable; whether it stays *interesting* is the open question, and it is the one to watch on the
pre-launch testnet.

## 5. Mechanics

`constMineAmount` stops being a constant and becomes a function of the transaction slot:

    _mineAmount = min(mul(constMineAmountPerSlot, txSlot), constMineAmountMax)

Two constants replace one — `constMineAmountPerSlot` = 1115 motes, `constMineAmountMax` =
1 250 000 000 motes — and every current reference to `constMineAmount` in `lock_mine.easyfl`
becomes a call: the inflation equality, the payout cap and the 1 % fee cap. `R`'s decrement
follows the same value, and the terminal condition `R_pred < A` still works with `A` varying.
`constMineRemainingInit` goes to 950 M and `constInitialSupply` to `constTargetBaseSupply/20`.

Integer arithmetic throughout, no rounding subtleties: `k` is a whole number of motes per slot.

## 6. Why the genesis lock was dropped

The previous idea was to bind the genesis capital to its chain with a balance floor. It failed
on two counts:

- **fault tolerance.** The genesis capital has to be split across ~5 sequencers on separate
  machines immediately, or the launch has a single point of failure. A rule pinned to one chain
  ID does not generalise to five without becoming a whitelist of chains, and the whole appeal
  was that it was two lines in the chain constraint;
- **it did not actually decentralise anything.** One entity holding 10 % under a covenant is
  still one entity holding 10 %. The lock changed what the founder could *do* with the stake,
  not the fact of the stake.

Halving the stake addresses the second directly, which is why it is the better lever.

## 7. Open

- **Is 60 days the right target?** It follows from wanting a participating community before
  control passes, but nothing measures that yet. The pre-launch testnet should show how fast
  delegated capital actually accumulates, and `k` follows from the answer.
- **Linear is the simplest shape, not necessarily the best.** It couples the three early
  milestones rigidly (§2). If 5/12 wants to move without 7/12 following, the shape has to
  change — a piecewise ramp, or a ramp that overshoots `A_max` and settles back.
- **`A0 = 0` exactly, or a small floor?** Starting from zero is one constant fewer and makes
  the first transits nearly worthless. A floor of, say, 50 PROX costs little and gives early
  miners something, but it does not solve the 133× spread, only soften its first days.
- **Nothing here is measured.** These are projections at the target pace, on a network that has
  never run a launch.
