# Fair launch — rationale, decisions and rejected options

> **LIVE** — Why every launch number is what it is, what else was tried, what is still open.
> **Binds:** `ledger/def/lock_mine.easyfl`, `ledger/genesis.go`, `ledger/base/genesis.go`. Where this and the covenant disagree, the covenant is right.

Technical companion to `fairlaunch.md`, which is the public-facing document. That one says
what the launch is; this one says why every number is what it is, what else was tried, and
what is still open. Where this document and `ledger/def/lock_mine.easyfl` disagree, the
covenant is right.

Status: **implemented and running on the testnet.** The mine chain, proof-of-signing-work,
the pace-relieved difficulty and the flat-then-ramp emission schedule are all shipped. One
item is deferred (§9).

Units: 1 PROX = 10⁶ motes. Slot τ = 10.24 s (128 ticks × 80 ms) → 8437.5 slots/day,
≈3.08 M slots/year.

---

## 1. Parameters in force

| Symbol | Value | Meaning |
|---|---|---|
| T | 10¹⁵ motes (10⁹ PROX) | target base supply — a mintable ceiling, never a held sum |
| I | T/20 = 5×10¹³ motes (50 M PROX) | genesis supply, the bootstrap sequencer output |
| R_init | T − I = 9.5×10¹⁴ motes | initial remaining-mintable counter R |
| A₁ | 375 PROX = 3.75×10⁸ motes | reward per transit during the flat phase |
| L | slot 379 688 (~45 days) | last slot of the flat phase |
| k₂ | 462 motes/slot | linear growth of the reward after L |
| N | ~907 279 | transits to exhaust R (average reward ~1047 PROX) |
| C | 50 000 000 motes | the mine output's own balance, constant forever (§2) |
| B₀ | 24 | seed difficulty at genesis (tests use 8) |
| E | 10 | floor difficulty — the retarget never goes below |
| C_max | 40 | ceiling difficulty — must stay < 64 (§4) |
| P | 3 | minimum pace, slots between predecessor and successor |
| target | 4 slots (41.0 s) | pace the retarget aims to hold |

Ledger constants, set at ledger init the same way tick duration is, so unit tests can use a
low difficulty and each testnet can pick its own at genesis. Only the mutable per-transit
state — R and B — lives in the lock's own arguments.

---

## 2. The mine chain covenant

A single chained UTXO fixed at genesis, at genesis output index 3, carrying the constant
`MineChainID`. Its lock is `mineLock(R, B)` and it enforces the entire mining policy by
itself. The chain constraint beside it supplies ChainID preservation, predecessor/successor
linkage and the transition counter.

**Readability is a requirement, not a preference.** This constraint is public and will be
read by everyone who wants to mine, so it is written as small named predicates with a line
of comment each, and no clever encodings.

### The transaction template

`mineLock` enforces a fully static, known-in-advance shape: **one input** (the mine UTXO)
and **exactly three outputs**.

| output | content |
|---|---|
| `[0]` mine successor | balance = C unchanged, inflation = A, `mineLock(R−A, B_next)`, chain constraint continuing `MineChainID` |
| `[1]` payout | sig-locked, holder ID = the transaction signer's, amount A′ ≤ A |
| `[2]` tag-along | amount A − A′, capped at 1 % of A |

All of it is checked on the **consumed (predecessor) arm**, which always runs when the mine
UTXO is spent, so the template cannot be evaded — in particular a successor that dropped
`mineLock` is rejected there. The produced arm only pins the lock to the one mine chain,
which is what makes `mineLock` invalid on any other output.

Requiring the successor's lock to be `mineLock` again is what keeps the chain permissionlessly
mineable forever. Without it the chain could be captured by an ordinary signature lock and
mining would simply end.

### The rules, and why each exists

1. **Bound to one chain.** The predecessor's own sibling chain constraint must carry
   `MineChainID`. Same "read the sibling constraint" pattern the foundry uses.
2. **Open unlock.** No per-output signature, following the stem lock. The one mandatory
   transaction signature supplies the holder ID that `[1]` must pay.
3. **R decrement and terminal condition.** `R_succ = R_pred − A`, and if `R_pred < A` no valid
   successor exists — the chain has ended. Because A grows, the chain stops with up to one
   reward's worth of R unminted; that is accepted rather than special-cased.
4. **Difficulty retarget.** `B_succ` is one bit up, down or unchanged from the single last
   gap — §4.
5. **Fee cap at 1 % of A.** This is what blocks outsourcing by fee routing. Without it a
   miner could send almost all the value as a tag-along fee to a sequencer it controls, keep
   A′ negligible, and then safely hand its signing key to a pool. Forcing ≥ 99 % into the
   key-locked payout means outsourcing the key risks the real reward.
6. **Inflation.** `[0]` declares inflation = A and amount conservation balances on its own:
   consumed C, produced C + A′ + fee = C + A. The chain constraint's per-output inflation cap
   is relaxed for `mineLock`, since a mine transit legitimately mints more than a chain's
   ordinary inflation allowance.
7. **Proof of work** — §3.

Every use of A inside the constraint — the exhaustion guard, the successor's inflation, the R
decrement, the payout cap and the fee cap — reads one helper, so a transaction is checked
against a single value of A throughout and the schedule cannot be straddled.

### Why C is worst-case sized and not exempted

The minimum storage deposit is a pure function of the output's byte size (size × 50 000 below
100 B, size × 250 000 − 20 M above) and of fixed constants — **not** supply-relative — so a
fixed C stays valid however the supply grows. `mineLock` pins every successor's balance equal
to its predecessor's, so C must satisfy the deposit for the largest size the mine output ever
reaches. It stays well under 256 bytes for its whole life and the deposit for 256 bytes is
≈44 M motes, so C = 50 M is a safe permanent bound. R is widest at genesis and only narrows
from there.

The dust exemption used by the stem, tag-along and send-with-deadline outputs is deliberately
*not* used here. Those are justified by having a bounded lifetime, which a permanent mine UTXO
does not have; exempting it would reopen the dust vector.

---

## 3. Proof-of-signing-work

`blake2b` of the **whole signed transaction** must end in at least K zero bits, tested on the
low 64 bits (`lshift64(rshift64(h,K),K) == h`), which is why the difficulty ceiling must stay
below 64 — at K ≥ 64 no solution exists and the chain would stall permanently.

The miner varies a nonce in the mine lock's unlock parameters. Those are free-form and
ignored by the open unlock, but they are part of the transaction essence, so a new nonce means
a new transaction id, a new deterministic signature, and a new hash. **Every attempt is a
fresh Ed25519 signature under the miner's own key.**

Three properties follow, and they are the reason for the design:

- **Not outsourceable.** The private key is inside the hot loop, and rule 5 forces ≥ 99 % of
  the payout to the signing key. Handing the key to a pool hands over the reward. Pooling —
  the largest single source of concentration in every proof-of-work chain launched so far —
  has no foothold.
- **ASIC-hostile.** The inner loop is a signature, not a bare hash. Hardware can shave a
  constant; it cannot build the orders-of-magnitude moat a bare hash invites.
- **CPU-egalitarian.** Flat marginal cost per attempt, no economy of scale.

Reward is therefore ~linear in CPU share with no scale discount. Note what that does *not*
fix: a dominant miner still wins roughly its CPU share of transits at any difficulty.
Adaptive difficulty controls contention, never concentration — see §7.

Two invariants are relied on rather than re-implemented: the pace M is the miner's choice and
is fixed into the successor's timestamp, so a solution cannot be reused at a different pace;
and a transaction timestamped in the future is rejected outright rather than buffered, which
is an existing node rule.

---

## 4. Difficulty: three iterations and why

This is the part that took the most attempts. The record matters because the failure mode of
each design was only visible under load.

### Iteration 1 — K depends on the pace

`K(M) = max(B − (M − P), E)` with `M = succ.slot − pred.slot ≥ P`, and B static. Correct in
shape, but B never adapted, so it could not track unknown hashrate.

### Iteration 2 — flat `K = B` with a ±1 retarget, and the sawtooth

The pace dependence was removed on the theory that `K = B` gives a cleaner hashrate signal,
and B was retargeted one bit per transit against the last gap. **This oscillated on the live
network and was the wrong call.**

The reason: with `K = B` the pace is a *step function* of B. While B is below the solvable
level every miner solves faster than the pace floor, so every gap is exactly `M = P`, so the
retarget hardens on **every** transit with no feedback at all — until one bit finally tips
solve time past the floor and the pace jumps roughly 2×. A one-bit change in B swings the pace
between 3 and 6 slots, so the controller can never land on a target of 4. It overshoots and
oscillates. Measured: B climbed 24 → 29 and stalled at about 20 minutes per transit.

A relief valve was added on top (snap B down after a very long gap), which turned an
unrecoverable climb into a recoverable sawtooth without addressing the climb itself.

### Iteration 3 — pace-relieved K, which is what ships

Restore the pace term and make it always-on, anchored at the minimum pace:

```
K_required(B, M) = max(B − (M − P), E)
```

At the minimum pace `M = P` the full B is required; each extra slot of pace eases exactly one
bit; floored at E. The pace check already guarantees `M ≥ P`, so the subtraction is
well-defined for every valid transit, with an underflow-safe clamp.

The retarget stays a single-gap ±1 on B:

```
M < target  → min(B+1, C_max)   harden
M == target → B                 hold
M > target  → max(B−1, E)       ease
```

with a genesis gate: while the predecessor is the genesis mine output at slot 0 the gap is
meaningless, so B is held.

**Why this stabilizes.** `K(M)` spreads the exponential across the *time* axis, which
linearizes pace against B: the winning pace becomes `M ≈ B − log₂(H·τ) + P`, a smooth
~1-slot-per-bit function of B instead of a 2× step. A ±1 jitter in B now moves the pace by one
slot and self-corrects on the next transit. The retarget drives B to where the winning pace
equals the target and then holds it. Equilibrium is `B ≈ log₂(H·τ) + (target − P + 1)`.

**It also provides liveness for free.** As M grows, K falls to E, so however far B sits above
the network's actual capacity, waiting long enough always makes a transit solvable. The chain
cannot wedge on difficulty. This subsumes the relief valve of iteration 2 entirely, which was
deleted along with its constant.

**The snap-down was not merely unnecessary but wrong.** Snapping B to the difficulty actually
solved would hold at `M = P`, where the solved K equals B — exactly the case that must harden.
That is why the retarget remains ±1.

### Why the slot dependence is not gameable

`K(M)` makes the required difficulty depend on a slot the miner chooses, which looks like a
lever and is not.

To stamp a later, easier slot you must actually wait for the wall clock, because the node
rejects future-stamped transactions. While you wait, *everyone's* required K ramps down
together, and the first miner to solve at any pace wins. Submitting as soon as possible
dominates; delaying only risks losing the transit. The retarget is not gameable either:
stamping early hardens the next round for everyone including yourself, stamping late eases it
for everyone, and neither produces a private gain.

The miner therefore targets the oldest allowed slot (`predSlot + P`, the highest K and so the
heaviest transit) and re-stamps forward as the clock advances without a solution.

### Fork choice

Among competing transits the miner prefers, in order: the longest chain by transition counter;
then the **oldest successor slot**; then the biggest tag-along fee; then the lowest transaction
id for determinism.

The second criterion is the interesting one. A smaller successor slot required a higher
`K = B − (M − P)`, so it is the heavier transit — this is "prefer the heaviest difficulty"
expressed in a form that is **non-grindable**, since claiming an older slot means actually
meeting the higher K the constraint requires there. It replaced a raw trailing-zero-count
comparison, which was grindable.

### Live validation

Measured on the testnet over 12 hours with the constants above:

| Observable | Measured | Reading |
|---|---|---|
| Difficulty B | 20–21 for 69 of 73 samples, range [20, 23] | no sawtooth; iteration 2 climbed 24→29 and stalled |
| B against the floor | 20–21 vs floor 10 | not pinned at the floor |
| Pace while mining is active | 47.8 s vs 41.0 s target | controller holds target within the ±1-bit jitter |
| Pace overall | 440 s | miner uptime, not the controller — duty cycle 10.8 % |

The two pace figures are not a contradiction: idle buckets contain no transits, so the overall
average measures how intermittently miners run. The conditional pace is what the retarget
regulates, and it is within 17 % of target. The controller's own state confirms it
independently — a sustained pace above target would ease B one bit per transit down to the
floor, and B is stable at 20–21, so the winning gaps must be at or near target.

The predicted equilibrium was `B ≈ 22` at ~220k H/s combined; observed 20–21 at a pace of
~4.7 slots. The model is right to within a bit.

**This also closed the winner-take-all bias.** That failure mode required difficulty to
collapse to the floor, so that solve time fell far below the pace floor and every height
became a latency race among miners who had all already solved. With B at twice the floor and
solve time comparable to the pace, work decides heights again — a structural fix, not a
client-side mitigation.

---

## 5. Supply split and the emission schedule

### What only ever mattered: A/pace

Emission is A per `target` slots, so only the ratio drives everything. This has bitten once
already: A was originally sized at 500 PROX against an expected pace of ~2 slots
(2.5×10⁸ motes/slot). When the shipped target pace became 4, A = 500 silently halved the
emission rate and pushed every date out 2×. A was corrected to 1000 to restore the same
2.5×10⁸ motes/slot. **Any future change to the target pace must move A with it.**

### The coupling that forced the ramp

Under a constant A, the time to full emission and the time to any early milestone are the same
constant-rate process, so their ratio is fixed by the supply split alone and cannot be tuned by
A at all:

```
t_full / t_overtake ≈ R_init / I
```

At the old split (I = T/10) that ratio was 9: a one-month crossing forced a nine-month tail,
and raising A shortened both in lockstep. Halving the genesis share to I = T/20 makes the ratio
**19**, which is worse — a 5 % genesis with a flat reward would reach the 5/12 threshold in
about 17 days and then run a tail of well over a year.

Seventeen days is not enough time for a participating community to exist, and that is the
binding constraint: because a healthy branch needs more than 7/12 of the coverage, the
bootstrap capital is the only thing holding the network up until enough *mined and
participating* capital exists. Crossing the threshold before that capital exists does not
decentralize the ledger, it stalls it.

So a smaller genesis share needs the runway bought back some other way. Making A a function of
the slot is what does it: the observed ratio comes out at **7.0** rather than 19, which is the
whole point of the schedule. The lever that the constant-A analysis said did not exist — tuning
the deadline and the tail independently — is exactly what a non-constant A provides.

### The two candidates

Both keep I = 50 M, R = 950 M, the 1 B ceiling and the 4-slot pace.

**A — ramp from zero.** `A(slot) = min(k·slot, A_max)`, k = 1115 motes/slot, A_max = 1250 PROX.

**B — flat, then ramp.** `A(slot) = A₁` up to slot L, then `A₁ + (slot − L)·k₂`, uncapped.

| | flat A=1000, I=T/10 | A: ramp from 0 | **B — adopted** |
|---|---|---|---|
| genesis share | 10 % | 5 % | **5 %** |
| 5/12 — founder can no longer stop the network | 33.9 d | 60.0 d | **45.2 d** |
| 50 % — mined overtakes genesis | 47.4 d | 71.0 d | **61.8 d** |
| 7/12 — runs without the genesis capital | 66.4 d | 84.0 d | **81.6 d** |
| emission complete | 426.7 d | 426.7 d | **430.2 d** |
| reward, first transit | 1000 | 9.4 | **375** |
| reward, last transit | 1000 | 1250 | 1876 |
| ratio first→last | 1× | 133× | **5.0×** |
| month-1 emission | 63 M | 4 M | 23.7 M |
| transits | 900 000 | 900 135 | **907 279** |
| average reward | 1000 | 1055 | **1047** |
| end-state mined share | 90 % | 95 % | **95 %** |

### Why B, and not A

**B's flat phase and the bootstrap phase are the same interval by construction.** A₁ is chosen
so the flat emission alone reaches `5I/7` — the point where the genesis share falls to 7/12 —
exactly at L. That makes the schedule self-describing: the reward is flat while one party can
still stop the network, and starts growing the moment that stops being true.

**A assumes miners it may not attract.** Every date in every column is emission at A/pace,
which holds only while somebody actually transits the chain. Candidate A pays 9.4 PROX on day
one. If that attracts nobody, transits stop, emission stalls, and the schedule slides *right* —
lengthening the centralized period, which is the opposite of the intent, and self-reinforcing
while it lasts. B pays a flat 375 PROX through exactly the window where that failure would do
the most damage, and asks nothing of a reward curve.

**A's early-miner penalty is 133×**, smallest precisely when the network is least proven. B's
is 5.0×, and its opening reward is 40× A's.

**A couples its milestones rigidly.** With A linear from zero the cumulative is quadratic, so a
milestone at mined M arrives at `t ∝ √M` and the three early dates sit in the fixed ratio
0.845 : 1 : 1.183. Pinning the first at 60 days *forces* the others at 71 and 84; A_max moves
only the tail. There is nothing left to tune. B separates them because its two phases have
different shapes: A₁ sets the first milestone, k₂ sets the tail, and L decides where the
schedule changes character.

**A rising reward also sustains the tail**, which is where a fourteen-month emission needs the
help: as difficulty climbs with hashrate, a growing reward keeps a transit worth attempting.

### Choosing L = 45 days

L is the length of the flat phase and, by construction, the date the founder can no longer stop
the network. The alternative considered was **L = 60 days** (A₁ = 280 PROX, k₂ = 296), which
maximises the runway a community has to form — the entire reason the genesis share can be
halved at all.

45 days wins on everything else. Because L sits inside the flat phase while the 7/12 milestone
sits inside the ramp, shortening L by 15 days pulls full decentralization in by 24: 81.6 days
against 108.2. The opening reward is higher (375 against 280) and the acceleration is milder
(5.0× against 5.4× at a much shorter tail). What 45 gives up is six weeks of runway rather than
eight, and **whether six weeks is enough is the one question here that a testnet can answer and
an argument cannot.** It is the single most important number still unmeasured.

### Choosing k₂ = 462, finishing at 430 days

With L fixed, k₂ alone sets the finish. 430 days was chosen because it **restores the previous
schedule's shape**: 430.2 days against 426.7, 907 279 transits against 900 000, average reward
1047 against 1000. So halving the genesis share costs nothing in emission length, transit count
or average reward — the "about fourteen months" figure survives unchanged, and so does the load
the mine chain has to carry.

The alternative was k₂ = 230, finishing at 547 days. Gentler — last reward 1350 rather than
1876, a 3.6× spread rather than 5.0× — but it adds four months of emission and 28 % more
transits for no gain anyone outside this document would notice.

### Consequences accepted

- **The bootstrap period is longer in wall-clock terms**, 45 days against 33.9, and 7/12
  arrives at 81.6 days against 66.4. The genesis capital is load-bearing for liveness for
  half a month longer than it used to be. This is unavoidable in any 5 % variant: halving the
  share halves the runway and the ramp is what buys it back.
- **Emission accelerates rather than decays** — 0.79 M PROX/day through the flat phase, 3.96 M
  at the end, then a hard stop. That is the opposite of the decaying shape most emission
  schedules have, and it needs explaining wherever the schedule is presented.
- **A 5.0× spread** between the first and the last reward. Bounded, far below A's 133×, but the
  same work is paid five times better at the end than at the start.

---

## 6. Defending the genesis share

### Concentration, measured against 1/6

Decentralization here is a question about how token *holdings* are distributed, not about node
counts or hashrate. The relevant threshold is structural: an adversary needs 1/6 of the supply
to keep two disconnected healthy forks alive at once, which is the network's safety margin.

That gives a single holding a scale to be read against. 10 % is sixty per cent of the way to
1/6; 5 % is thirty per cent of it, and about 1 % per node once split across the sequencers
fault tolerance requires from day one. A share that sits well below the fork-safety threshold
is a materially different object from one approaching it, and the ledger's own security
argument is what makes the difference measurable rather than rhetorical.

### Zero promises

The genesis share is at the founder's discretion. No lock, no vesting, no covenant, no statement
about its fate. Nothing replaces the scrapped lock of §8, deliberately, because every
replacement would be a commitment of the kind this position exists to avoid.

Two things make that position coherent:

- **A change of hands does not weaken the ledger.** Incentives and rewards are an intrinsic
  property of *holding* the tokens, not of who holds them. Whoever ends up with the stake has
  precisely the same reason to delegate it or to sequence with it. No security property depends
  on the founder specifically keeping it, so no undertaking to keep it is owed.
- **"Dumping" presupposes a market.** Through the mining phase there is no meaningful liquidity
  for the token, so the scenario cannot arise while it matters most; by the time it can, the
  distribution is largely done. That a sale is possible at all would indicate a market exists,
  which is not a failure mode.

A third argument sometimes suggests itself and does not hold: that the founder effectively
cannot sell, because the network needs the stake to keep participating. The first point above
is why — participation follows the tokens, not the holder — and in any case an assurance of
that shape would be the very kind of commitment this position avoids.

### Regulatory exposure

MiCA sets no premine threshold. Exposure turns on whether there is an offer to the public,
consideration, and a promise — which §10 of `fairlaunch.md` addresses and which the *size* of
the holding does not touch directly.

Where the size does bear on it is indirect. The instruments normally used to reassure holders
about a large stake — a lockup, a vesting schedule, an undertaking not to sell before some
date — are all commitments, and commitments are what bring an issuance inside the regime. A
smaller stake needs none of them, so the promise-free position of §10 in `fairlaunch.md` costs
nothing to hold.

The whitepaper exemption for crypto-assets automatically created as a reward for maintaining
the DLT is a secondary and weaker consideration, and it fits an issuance that is 95 % mining
reward more readily than a smaller mined share.

It is worth being accurate about how mining relates to consensus here, because the two are
easily conflated. Mining does not secure the Proxima ledger the way proof of work secures
Bitcoin — nothing is burned to defend the ledger, and the work stops when emission ends. What
mining does is distribute the tokens, and in a consensus weighted by token holdings the
distribution *is* what secures the ledger. That is the accurate description of the mechanism.

### Kaspa, as a reference point

Kaspa is the nearest recent fair launch and the useful comparison. Two of its choices bear
directly on decisions made here.

**It also opened flat.** Its pre-deflationary phase ran from mainnet on 7 November 2021 to
8 May 2022 — six months — at a constant 500 KAS per block, before the geometric "chromatic"
decay began at 440 KAS per block, halving annually in twelve monthly steps. The first two
weeks used a *random* reward in the range 1–1000 KAS per block before a hard fork replaced it
with the flat rate.[^kas-emission] So a flat opening phase has precedent; what is unusual in
the schedule of §5 is the direction of the second phase, not the flat start.

**Its launch announcement was staged over weeks.** Progress was communicated through Discord,
Twitter, Telegram, GitHub and Reddit, and the BitcoinTalk announcement — the forum then hosting
the largest mining audience — followed on 26 November 2021, about three weeks after
mainnet.[^kas-history] Whatever the reason, the reach of a launch announcement over its first
weeks is one of the inputs to who mines early, which is why §9 lists it as an open practical
question rather than a protocol one.

On its distribution, Kaspa's own sources state a fair launch with no ICO, no vesting phase and
no premine, and equal access from block zero.[^kas-emission] [^kas-history] Third-party
accounts of insiders mining a few per cent of supply after launch circulate but are not
corroborated by those sources, so no figure is relied on here.

**The pre-launch positions differ, though.** By Kaspa's own account the research that became
GHOSTDAG was commercialized through DAGLabs, which raised venture funding from Polychain and
Accomplice; there were hardware plans and a startup structure, and a presale was planned before
being abandoned — none of it surviving to launch day.[^kas-prelaunch] Proxima's pre-launch work
was not financed at all: no funding round, no company, no investors, no presale planned or
abandoned.

That difference matters less for how either launch looks than for what each had to decide. A
funded project arrives at genesis with backers whose position must be resolved one way or
another, and choosing to give them nothing is a decision that has to be taken and then kept.
An unfunded one has nobody holding a claim, so the absence of an allocation table here is not
a policy adopted but a fact about the project — which is why "nothing is allocated except to
the founder" costs nothing to state in `fairlaunch.md` §3.

[^kas-emission]: <https://medium.com/kaspa-currency/tokenomics-emission-and-mining-6653cc473a7a>
[^kas-history]: <https://kaspa-lens.com/kaspa/wiki/introduction-to-kaspa/history-of-kaspa-and-fair-launch>
[^kas-prelaunch]: <https://x.com/KASPAglobal/status/2076917703265198190>

---

## 7. Concentration is not a protocol problem

Proof-of-signing-work is about the best a proof of work can do for fairness — flat marginal
cost, no ASIC moat, pooling structurally hostile — so reward is ~linear in CPU share. But that
bounds the *mechanism*, not the *outcome*: with on the order of a hundred miners and a realistic
power-law distribution of CPU, expect the largest single actor at roughly **30–50 % of month-one
emission**, diluting with participants and with time.

Adaptive difficulty does not help; it controls contention, not concentration. A dominant miner
wins its CPU share of transits at any difficulty.

Protocol anti-whale levers were considered and rejected. A per-key input rate limit is evadable
by free key-splitting, and it cuts against the fair-launch claim: the rules would stop being
identical for everyone. **The real levers are time and launch breadth, not a protocol cap** —
which is why the announcement question in §6 is a substantive risk and not a marketing detail.

There is a direct tension worth naming: a fast handover moves control off the founder quickly
but concentrates early emission among whoever has CPU at that moment, while a slow one lets
later participants dilute them but keeps the founder in control longer. Some concentration has
to be accepted; the flat opening reward at least keeps the earliest weeks from being
disproportionately lucrative per unit of work.

---

## 8. Why the genesis lock was dropped

The idea was to bind the genesis capital to its chain with a balance floor in the ledger: it
could be delegated, and so contribute coverage and run the bootstrap network, but never
transferred. It would have turned "trust me not to sell" into "cannot sell", verifiable at
genesis, with no liability because it is code rather than an undertaking.

It was specified and then scrapped, on two counts:

- **fault tolerance.** The genesis capital has to be split across several sequencers on separate
  machines immediately, or the launch has a single point of failure. A rule pinned to one chain
  ID does not generalize to five without becoming a whitelist of chains, and its whole appeal
  was being two lines in the chain constraint;
- **it did not decentralize anything.** One entity holding 10 % under a covenant is still one
  entity holding 10 %. The lock changed what the founder could *do* with the stake, not the fact
  of it.

Halving the stake addresses the second directly, which is why it was the better lever. A partial
variant — 90 % of genesis locked to the bootstrap chain forever, 10 % released at the projected
end of mining — was considered and dropped with the rest: it still leaves one entity holding a
whale's share, and it adds a promise-shaped structure where §6 argues for none.

---

## 9. Open and deferred

- **How long a runway the community actually needs.** Everything follows from it: L is that
  number and A₁ follows from L. Nothing measures it yet. The pre-launch testnet should show how
  fast delegated capital accumulates, and L = 45 days should be revisited against that
  measurement before genesis. This is the most consequential unmeasured number in the design.
- **Input-based double-spend flood filter** — the one deferred implementation item. Many miners
  racing the same predecessor produce conflicting successors on the single mine-chain input; a
  filter dropping more than N unsolicited transactions sharing an input per window is wanted.
  This is workflow/transaction-input work, not a constraint change. The related
  sender-known-in-LRB spam-filter exemption for mining transactions has shipped, so brand-new
  miners are no longer blocked.
- **Should the ramp cap?** As written A grows until R is exhausted, ending near 1876 PROX. A
  ceiling would bound the acceleration at the cost of a fourth constant and a later finish.
- **Zero-fee mine transactions** are permitted: the 1 % cap sets no minimum, so a miner may keep
  the whole reward and find another route to a sequencer. Left permissive.
- **Difficulty against real hashrate.** The controller holds target on the testnet, but it has
  not met hashrate arriving in the amounts a real launch would bring.
- **Participation at scale.** Many holders delegating to many sequencers has been modelled and
  not yet measured.

---

## 10. Where it lives

| Concern | Location |
|---|---|
| the covenant | `ledger/def/lock_mine.easyfl` |
| emission schedule (on-chain) | `_mineAmountAtSlot` in the same file |
| ledger constants | `ledger/def/def_constants0.json`, `ledger/def_constants0.go` |
| genesis mine output | `ledger/genesis.go`, `ledger/base/genesis.go`, `ledger/multistate/genesis.go` |
| wallet-side mirrors of K, the retarget and the schedule | `ledger/txbuildercore/helpers_mine.go` |
| reference miner | `proxi/node_cmd/mine.go` and siblings |
| streamed-transit verification | `proxi/node_cmd/mine_verify.go`, `api/streaming/mining_tx_server.go` |
| tests | `ledger/tests/mine_test.go`, `ledger/tests/mine_schedule_test.go` |

The wallet mirrors the constraint's arithmetic in Go so a miner can size a transit without
evaluating the constraint. Those mirrors must agree exactly with the EasyFL: if they drift,
every transit the miner builds past the drift point is rejected by the very lock it is trying
to satisfy. `mine_schedule_test.go` pins the agreement across the ramp boundary for that reason.

Related notes: `mining-bias.md` (the winner-take-all failure mode, now structurally closed by
§4), `mining_tx_streaming.md` (the transit stream), `delegation_scalability.md` (participation
at scale), `monitor.md` (the live fair-launch page).
