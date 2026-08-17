# 5 % genesis, and the emission schedule to go with it

Status: **implemented 2026-08-17** — candidate B at `L` = 45 d with a 430-day finish (§1).
`constMineAmount` became the three-constant schedule of §6, the genesis share is one twentieth
of the target supply and R_init is 950 M. Breaking; lands at the testnet reset. Written
2026-08-15 for the fair launch plan (`.internal/launch_v2.md`). Supersedes the scrapped
genesis-lock idea — §7.

Halve the genesis share from 10 % to **5 %** of the target supply. That alone would put the
5/12 crossing at 17 days, far too soon for a participating community to exist, so the amount
minted per transit stops being constant and grows with the slot. Two shapes were worked out;
**B is the better one**, and §2 is why.

Everything below keeps `I` = 50 M, `R` = 950 M, the 1 B ceiling and the 4-slot pace.

---

## 1. The schedule

**A — ramp from zero.** `A(slot) = min(k × slot, A_max)`, `k` = 1115 motes/slot,
`A_max` = 1250 PROX, flattening on day 133. Kept here for the comparison; not the choice.

**B — flat, then ramp.** `A(slot) = A1` up to slot `L`, then `A1 + (slot − L) × k2`. No cap; it
grows until `R` runs out. `A1` is set so the flat phase alone reaches `5I/7` exactly at `L`,
which makes `L` the 5/12 crossing itself. `L` = 45 days, and `k2` is then set by where the
emission should finish.

| | flat A=1000, I=100 M | A: ramp from 0 | **B — chosen** | B — long tail |
|---|---|---|---|---|
| genesis share | 10 % | 5 % | **5 %** | 5 % |
| `L` / `k2` | — | — | **45 d / 462** | 45 d / 230 |
| 5/12 — founder can no longer stop the network | 33.9 d | 60.0 d | **45.1 d** | 45.1 d |
| 50 % — mined overtakes genesis | 47.4 d | 71.0 d | **61.8 d** | 62.4 d |
| 7/12 — ledger runs without the genesis capital | 66.4 d | 84.0 d | **81.6 d** | 84.5 d |
| emission complete | 426.7 d | 426.7 d | **430.1 d** | 547.5 d |
| reward, day 1 | 1000 | 9.4 | **375** | 375 |
| reward at the end | 1000 | 1250 | 1876 | 1350 |
| acceleration over the run | 1× | 133× | **5.0×** | 3.6× |
| month-1 emission | 63 M | 4 M | 23.7 M | 23.7 M |
| transits over the run | 900 000 | 900 135 | **907 279** | 1 154 958 |
| average `A` | 1000 | 1055 | **1047** | 823 |
| end-state mined share | 90 % | 95 % | **95 %** | 95 % |

Constants for the chosen column: `A1` = 375 PROX, `k2` = 462 motes/slot, ramp starts slot
379 688.

Its profile over the run:

| day | 30 | 45 | 60 | 90 | 182 | 365 | 430 |
|---|---|---|---|---|---|---|---|
| `A`, PROX | 375 | 375 | 433 | 550 | 909 | 1622 | 1876 |
| mined, M PROX | 23.7 | 35.6 | 48.4 | 79.5 | 221 | 710 | 950 |
| share of `R` | 2.5 % | 3.7 % | 5.1 % | 8.4 % | 23 % | 75 % | 100 % |

## 2. Why B, and why these two numbers

**In B the flat phase and the bootstrap phase are the same interval.** `A1` is chosen so that
the flat emission alone reaches `5I/7` exactly at `L`, which makes the schedule
self-describing: *the reward is flat while the founder still controls the network, and starts
growing the moment it does not.*

That alignment is not decoration. It removes the one real hazard in A.

**A's schedule assumes miners it may not attract.** Every date in every column is emission at
`A(slot)/4`, which holds only while somebody actually mines every ~4 slots. A pays 9.4 PROX on
day one. If that attracts nobody, transits stop, emission stalls, and the schedule slides
*right* — lengthening the centralised period, the opposite of the intent, and self-reinforcing
while it lasts. B pays a flat 375 PROX through exactly the interval where that failure would
hurt most, and asks nothing of a reward curve.

**A's early-miner penalty is 133×**, smallest precisely when the network is least proven. B's
is 5.0×, and its opening reward is 40× A's.

**A rising reward also sustains the tail.** As difficulty climbs with hashrate, a growing `A`
gives miners a reason to keep going rather than stop.

### `L` = 45 days

`L` is the length of the flat phase and, by construction, the date the founder can no longer
stop the network. 60 days was the other candidate — `A1` = 280 PROX, `k2` = 296 — and it
maximises the runway a community has to form, which is the entire reason the genesis share can
be halved at all.

45 wins on everything else. Because `L` sits inside the flat phase while 7/12 sits inside the
ramp, shortening `L` by 15 days pulls full decentralisation in by 24: 81.6 days against 108.
The opening reward is higher (375 against 280) and the acceleration milder. What 45 gives up is
six weeks of runway rather than eight, and whether that is enough for delegated capital to
accumulate is the one question here that a testnet can answer and an argument cannot.

### `k2` = 462, finishing at 430 days

With `L` fixed, `k2` alone sets the finish, and 430 days was picked because **it restores the
current schedule's shape.** Against today's flat `A` = 1000: 430.1 days against 426.7, 907 279
transits against 900 000, average `A` 1047 against 1000. The "~14 months of emission" figure in
the launch document survives the change of genesis share unchanged, and so does the transit
count the mine chain has to carry.

The alternative was `k2` = 230, finishing at 547 days. It is gentler — the reward ends at 1350
rather than 1876, a 3.6× spread rather than 5.0× — but it adds four months of emission and 28 %
more transits for no gain that anyone outside this document would notice.

## 3. Why the dates land where they do

**A couples its three early milestones rigidly.** With `A` linear from zero the cumulative is
quadratic, so a milestone at mined `M` arrives at `t ∝ √M` and the three sit in fixed ratio
`0.845 : 1 : 1.183`. Pinning 5/12 at 60 days *forces* 50 % at 71 and 7/12 at 84; `A_max` moves
only the tail. There is nothing to tune.

**B separates them**, because its flat phase and ramp phase have different shapes. `A1` sets
the first milestone, `k2` sets the tail, and `L` decides where the schedule changes character.
That is what makes 45.1 / 61.8 / 81.6 reachable at all.

**The handover is more compressed than today's.** The span from 5/12 to 7/12 is 1.96× under the
current flat schedule and 1.81× here — so the launch stays centralised about eleven days
longer, then passes through the transition more quickly once it starts.

## 4. How the 5 % does against the three goals

**Decentralisation is a question about how token holding is distributed, and 10 % answers it
badly.** A single entity holding a tenth of supply is a whale on any reading, and in this ledger
the proximity is not generic: **1/6 of supply is what an adversary needs to keep two
disconnected healthy forks alive at once** (`launch_v2`, the 2/12 overlap). 10 % is sixty per
cent of the way to that number. Nothing has to be attempted for the closeness to matter — it is
near enough that the question gets asked, and the question being reasonable is what breaks the
narrative.

5 % is a different object. It dissolves against the 95 % that is mined, sits at thirty per cent
of the fork-safety threshold rather than sixty, and — split across the ~5 sequencers that fault
tolerance requires — is ~1 % per node. That is the range where the claim stops being a claim
and becomes arithmetic, and where the comparison to a PoW launch becomes available.

One cost, and it is the only one: the founder holds more than 5/12 for 45 days rather than 34.
The stake halves while the centralised window lengthens by eleven days. Worth knowing, but a
smaller thing than the holdings number.

**Premine blame: 5 % with nothing attached to it.** The genesis share is at the founder's
discretion. No time lock, no vesting, no covenant, no statement about its fate. §7 records why
the ledger-level lock was dropped, and nothing replaces it — deliberately, because every
replacement is a promise.

That posture is defensible rather than evasive, on two grounds:

- **A change of hands does not weaken the ledger.** The incentive to delegate and to sequence is
  an intrinsic property of holding the tokens, not of who holds them. Whoever ends up with the
  stake has precisely the same reason to put it to work. No security property depends on the
  founder specifically keeping it, so no undertaking to keep it is owed.
- **"Dumping" presupposes a market.** Through the mining phase there is no meaningful liquidity
  for PROX, so the scenario the accusation imagines cannot occur; by the time it can, the
  distribution is largely done. That a sale is possible at all would be evidence a market
  exists, which is not a failure mode.

**MiCA: improved, and precisely because 5 % makes silence affordable.** MiCA sets no premine
threshold — exposure turns on offer to the public, consideration, and promises, and the size of
a holding does not enter that test directly. What size changes is how much pressure there is to
*say something* about the holding, and every reassuring thing one could say — a lockup, a
vesting schedule, an undertaking not to sell before some date — is a promise, and promises are
what create the exposure. A 10 % stake invites the demand for that reassurance and makes
refusing it look like something. A 5 % stake makes saying nothing at all affordable, which is
exactly the posture `launch_v2` §10 has to hold.

Secondary, and weaker: the whitepaper exemption for crypto-assets automatically created as a
reward for maintaining the DLT fits an issuance that is 95 % mining reward more comfortably
than one that is 90 %.

**A smaller early land-grab.** `fairlaunch-research.md` §9 expects the top actor to take
30–50 % of month-one emission. This schedule emits 23.7 M PROX in month one against ~63 M on
the current one, so whatever concentration occurs applies to a much smaller slice.

### What Kaspa actually did

The benchmark the 5 % will be measured against, worth having straight. *Sourced from community
accounts and partly contested — verify before using any of it publicly.*

Kaspa launched 7 November 2021 with no premine, no ICO, no presale and no insider allocation at
launch: the clean end of the scale, and the comparison anybody will reach for. The rest of the
story is less clean.

- **DAGLabs mined after launch, with capital.** The company behind the research — funded by
  Polychain and Accomplice, and which had planned a presale that did not survive to launch day
  — spent several hundred thousand dollars mining on rented AWS hardware for about five months.
  The proceeds, reported as no more than 3 % and probably ~2.5 % of max supply, went to
  investors and to former DAGLabs employees and advisers.
- **So the realised insider share was ~2.5–3 %, not 0 %.** Against a 5 % genesis that is a
  factor of two, not a difference in kind. What differs is the mechanism: acquired rather than
  allocated, and not stated as a number on day one.
- **Launch reach decided who mined first.** Mainnet was announced in Discord, with no
  BitcoinTalk post — the forum hosting the largest mining community at the time. Where a launch
  is announced determines who the first miners are and therefore what early concentration looks
  like. That is an operational lesson for us, not a criticism of them.
- **The opening was deliberately unpredictable**: block rewards were random in the range
  1–1000 KAS for the first two weeks, replaced at the first hard fork by a flat 500 KAS/block.
  Two weeks in, at ~648 M coins mined, the network was halted and genesis remade to fix a bug.
- **Kaspa also opened flat.** Its pre-deflationary phase ran flat from launch to 8 May 2022 —
  six months — before the geometric decay began. A flat opening phase has precedent; the
  direction of the second phase is where B differs, and B's flat phase is much shorter.

The comparison to make, then, is *5 % disclosed and held openly against ~2.5–3 % acquired by
spending capital under the same rules as everyone else and named only afterwards* — rather than
conceding the premine point to a headline 0 %.

## 5. What it costs

**Eleven more days of founder control**, 45 against 34, and 7/12 at 81.6 days against 66.4. The
genesis capital stays load-bearing for liveness half a month longer. This is the price of
opening flat and it is unavoidable in any 5 % variant: halving the share halves the runway, and
the ramp is what buys it back.

**Emission accelerates rather than decays** — 0.79 M PROX/day through the flat phase, 3.96 M/day
at the end, then a hard stop at exhaustion. This is the inverse of every schedule a reader
knows, and someone will ask why late miners are paid more. It is defensible — emission
accelerates as the network decentralises — but it belongs in the launch document rather than
being discovered.

**A 5.0× spread between the first and last reward.** Bounded, and much smaller than A's 133×,
but not nothing: the same work is paid five times better at the end than at the start.

Everything else is unchanged from the current schedule: same finish, same transit count, same
average reward.

## 6. Mechanics

`constMineAmount` stopped being a constant and became a function of the transaction slot
(`_mineAmountAtSlot` in `ledger/def/lock_mine.easyfl`, mirrored wallet-side by
`Constants.MineAmountAtSlot`):

    _mineAmount = if txSlot <= constMineRampStartSlot
                     constMineAmountBase
                  else
                     constMineAmountBase + (txSlot - constMineRampStartSlot) * constMineAmountPerSlot

Three constants replace one — `constMineAmountBase` = 375 000 000 motes,
`constMineRampStartSlot` = 379 688, `constMineAmountPerSlot` = 462 motes — and every current
reference to `constMineAmount` in `lock_mine.easyfl` becomes a call: the inflation equality, the
payout cap and the 1 % fee cap. `R`'s decrement follows the same value, and the terminal
condition `R_pred < A` still works with `A` varying. `constMineRemainingInit` goes to 950 M and
`constInitialSupply` to `constTargetBaseSupply/20`.

Integer arithmetic throughout; `k2` is a whole number of motes per slot and the ramp start is a
slot number, so nothing rounds.

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

Halving the stake addresses the second directly, which is why it is the better lever. Nothing
replaces the lock and nothing should: §4 is why the promise-free position is the stronger one.

## 8. Open

- **How long a runway does the community actually need?** Everything else follows from it —
  `L` is that number, and `A1` follows from `L`. Nothing measures it yet; the pre-launch testnet
  should show how fast delegated capital accumulates, and `L` = 45 should be revisited against
  that measurement before genesis.
- **Should the ramp cap?** As written it grows until `R` is exhausted, ending at 1876 PROX. A
  cap would bound the acceleration at the cost of a fourth constant and a later finish.
- **`L` at the 5/12 crossing is a choice, not a constraint.** It makes the schedule
  self-describing, but `L` could sit earlier or later if there is a reason.
- **The Kaspa figures need verifying** before any of them is used publicly. The ~2.5–3 %
  insider-mining number comes from community accounts, some of them hostile.
- **Where the launch is announced** is not decided, and Kaspa's Discord-only announcement is the
  cautionary case: reach determines who the first miners are, and therefore month-one
  concentration.
- **Nothing here is measured.** These are projections at the target pace, for a network that
  has never run a launch.
