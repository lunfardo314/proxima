# Fair Launch — Token Distribution by Mining

Status: DRAFT — requirements, constraints, and early implementation directions.
Date started: 2026-07-01

## 1. Goal

Distribute the majority of Proxima token supply to the community after network
start, in a way that is **fair** and creates **no personal legal liability** for
the founder (MiCA-avoidance: founder must not be an issuer/counterparty).

The narrative is:
- Proxima is as decentralized as token ownership is distributed.
- Proxima's core value proposition is **permissionless decentralization**
  (Nakamoto decentralization). The token price / market cap should reflect
  decentralization, no matter the metrics.
- Hence decentralization is a concern of every token holder, and the ledger is
  _cooperative consensus among token holders_.
- At launch, Proxima is fully centralized.
- Once no token-holding entity controls 50% or more of the supply, Proxima enters
  the decentralization zone.
- Once no entity controls more than 33% of the supply, the network becomes secure
  and decentralized.

## 2. Supply parameters (as sketched)

- Genesis supply `I = 10^14 + D` motes.
  - `10^14` belongs to the founder.
  - `D ≈ 10^13` distributed among 3–5 early contributors.
- Target supply `T = 10^15` + some inflation generated during mining period.
- The `T − I` tokens (~89% of supply) are to be obtained by anybody through mining.

## 3. Constraints

- **Fair launch.** Everyone has the same chance from the start; no insider/hardware
  head start on the mined portion.
- **No issuer liability (MiCA).** Founder must provably not be in the loop of
  distribution; founder is protocol author, not counterparty.
- **No locked reserve, no distribution act.** The founder never holds or hands out
  the `T − I` tokens. Distributing a reserve = issuer liability, therefore excluded.
- **Self-mint from open air.** Each participant mints their own tokens by producing
  a valid proof, permissionlessly, against protocol rules fixed at genesis. No one
  gives them anything; they create it themselves.
- **`T` is a mintable ceiling, not a held sum.** Supply grows from `I` toward `T` as
  people mint; the cap is enforced by covenant (mint refused at ceiling), never
  pre-allocated.
- **Mining must be slow.** Emission paced by one of: PoW, VDF, or randomized
  low-probability success.

## 4. Framing (input notes)

- This mining is a **distribution mechanism, not a consensus mechanism.** Proxima
  security comes from ledger coverage, not work. The proof's only job is to meter
  release fairly and slowly; it does not secure the ledger.
- Mechanism shape (as discussed, not decided): *inflationary self-mint gated by
  proof* — not faucet-from-reserve.

## 5. Precedents surveyed (reference input, not recommendations)

- **0xBitcoin / ERC-918 "Mineable Token Standard"** — contract mints its own token
  to anyone submitting valid PoW; autonomous on-chain difficulty adjustment; no
  premine; immutable/no admin key.
  - https://eips.ethereum.org/EIPS/eip-918
  - https://medium.com/coinmonks/what-are-mineable-tokens-f290cb3215b2
- **Proof-of-Burn (Counterparty XCP, Slimcoin)** — tokens materialize when a
  participant burns value to an unspendable address; founders receive nothing;
  branded "fair mint." Requires an external asset to burn into.
  - https://docs.counterparty.io/docs/advanced/specifications/fairminter/
  - https://www.gate.com/learn/articles/what-is-proof-of-burn/1667
- **Bitcoin / Monero** — block reward springs from nothing to the miner; no reserve.
  Monero: RandomX CPU-egalitarian, deliberately ASIC-resistant (hard-forks to kill
  ASICs).
  - https://bitcoinmagazine.com/culture/why-bitcoin-fair-launch-is-important
- **Chia (Proof-of-Space-and-Time + VDF)** — VDF-based; but company pre-farmed 21M
  XCH; egalitarian idle-disk promise eroded at scale (hardware-weighted).
  - https://messari.io/project/chia-network/profile
- **BRC-20 / ORDI first-come inscription** — no reserve, gas-only "fair mint";
  fairness degraded into bot gas-wars / front-running.
  - https://thebitcoinmanual.com/articles/fair-mints-brc-20-mania/
- **Fair-launch-as-property-not-security** — recurring legal logic: premine held by
  a profit-seeking team → regulators treat all tokens as securities; fair launch →
  token treated as property. Grin cited as not encumbered by securities law.
  - https://medium.com/alpineintel/on-fair-token-launches-3d500dc0576c
  - https://medium.com/@nic__carter/in-support-of-the-proof-of-work-un-fair-launch-cd6e8f06358f

## 6. Version 1 — the `mine` chain

Imagine a special _mine chain_ — a chained UTXO (covenant) whose genesis is at the
state genesis. Apart from being a usual non-sequencer chained UTXO, it has these
properties:
- its lock is **open** — no signature is required to spend this particular output
  (stem-style); anyone can transition it by satisfying the `mine` constraint. (The
  *transaction* still always carries the miner's one mandatory signature)
- it carries a special immutable `mine(R)` script that enforces a certain structure
  of the transaction;
- it has a predefined chain ID, fixed as a constant.

$R$ (_remaining_) is the amount of tokens (motes) still mintable out of thin air by transiting
this chained output. It is intended as a **counter carried in the chain output's
data, not a locked balance** — no reserve is held.
Tentatively, at genesis $R_{init} = T-I$, where $I=10^{14}$ is the initial supply in the genesis output owned by the founder.

Each transit of the _mine chain_ creates a constant amount $A$ of new tokens (motes).
Transaction validation invariants are aware of this special chain and allow $A$
newly created tokens on it, similarly to how inflation is handled. This is part of the *full context validation* enforced by the node.

When the chain reaches its end, total ledger supply will be approximately
$I + R_{init} + F$ where $F$ is inflation generated on the ledger during the lifetime of the _mine chain_.

`mine(R)` script on successor enforce following constraints on transaction :

- `mine(R)` must be present on the mining-chain successor;
- if $R_{pred} \ge A$, the produced $R=R_{pred}-A$; otherwise the transaction is invalid and cannot be created — this is the final mining state;
- chain pace is at least $P$ slots: $M = succ.slot - pred.slot \ge P$;
- the mining-chain successor must be at index 0 and carry the same token balance as its predecessor (the minted $A$ is fully paid out to the two outputs below, not accumulated on the chain);
- a siglocked output at index 1 sends amount $A' \le A$ motes to the holder ID of the transaction (enforced);
- a tag-along output at index 2 carries the tag-along fee $T=A-A'$, capped at $T \le 1\%$ of $A$. The cap prevents an *outsourcing attack*: without it a miner could route almost all value as fee $T$ to a sequencer it controls and keep $A'$ negligible, then safely hand its signing key to an outsourced/pooled signer — the key-locked reward it risks is tiny. The cap forces $\ge 99\%$ of value into the key-locked output, so outsourcing the key means risking the real reward, restoring non-outsourceability;
- the _PoW_-like constraint, with $M \ge P$ slots between predecessor and successor. A valid transaction must satisfy:
  - $blake2b(txBytes)$ must end in at least $K(M)$ binary zeroes, i.e. $K(M)$ is the mining difficulty;
  - $K(P) = B$ — the maximum difficulty, applied to the shortest allowed step;
  - $K(n+1) = max(K(n) − 1, E)$ — each additional slot of the step halves the difficulty, down to a floor $E$, with $0 < E < B$;
  - the miner varies a nonce to search for a valid hash. The best place for the nonce is the unlock parameters of the mining-chain input (index 0): they are free-form (the open lock ignores them), part of the essence so they perturb txID→signature→hash, and not carried in any UTXO;
  - the mining process:
    - is parallelizable across many processes;
    - each attempt includes signing the transaction bytes, so ASICs cannot meaningfully accelerate it;
    - is non-outsourceable: the private key must not be given to anybody;
    - is therefore *proof-of-signing-work* — CPU-egalitarian and ASIC-hostile by construction, a fairness engine materially different from classical PoW.

Notes:
- the step $M$ in slots is the miner's choice; once chosen it is fixed in the
  successor timestamp, so a mining result cannot be reused with a different $M$;
- there is positive probability that several miners will mine valid transactions for the
  same predecessor. These are double-spends; the ledger resolves them and only one
  successor for a given predecessor survives;
- every transaction carries mandatory signature therefore each mining step involves signing the transaction.
- taking hash of transaction bytes in the EasyFL is literally `blake2b(txBytes)`. This does not involve any circularity
- after seeing valid mining transactions on the DAG, miner may adopt many different strategies of how to choose one (or several) transctions 
  for (parallel) mining. It depends on:
  - how deep inclusion of transaction is. It may be behind LRB, or it may not even in any branch yet.
  - timestamp of the transaction and current wall clock. Node guarantees that any transaction miner sees in the tangle has timestamp earlier than the wall clock
  - miner may choose to mine any transaction with the future timestamp: further to the future, less the PoW difficulty. The miner holds it locally until wall clock catches up; any node receiving a future-timestamped tx rejects it (enforced invariant, not buffering)
  - under competition the far-future / low-difficulty step is a gamble, not an equalizer: if a stronger miner takes a shorter step and consumes the tip first, the future tx dies before its timestamp arrives. Short steps dominate whenever hashrate is present, so reward tends to be proportional to hashrate (Bitcoin-like). This does not violate the goal: "fair launch" means no premine, open rules, no insider/hardware head start — not equal reward per miner. The future-timestamp lever lets a weak miner participate at all; it does not equalize reward per unit of hashrate
  - miner may continue mining transaction even if it is in the past
  - finally, miners target choosing strategy depends on the CPU power in its possession 
- miner will likely abandon current effort when it sees direct competitor on the network and quickly switch to different target(s)
- in general, variety of strategies is huge, dependent both on own CPU power and on competition.
- it would be nice to have math model, however it is unlikely will be possible to determine optimal strategy for the miner,
  therefore adaptive difficulty is probably the way to go.
- fairness is achieved because mining constraints are publicly defined on the ledger

**Other constraints for the difficulty policy, anti-spam**

- should ensure reasonable number of double spends/collisions per mined output predecessor
- should ensure mining the whole or majority $R$ in some 6 to 12 months (open question)
- currently spam protection mechanism in `txinput` requires holder ID of the transaction to be represented in the LRB, i.e. it should be known to the ledger.
  That means absolutely new miners will be filtered out by this policy. We need some exemption policy for mining transactions 
- it seems reasonable:
  - to introduce mining tx flag at the transaction level (enforced by `mine` constraint)
  - a new universal spam-protection filter in addition to existing ones: by input — too many (e.g. more than 10) unsolicited transactions with the same input `P` arriving within a certain time window are dropped
  - mining transactions are exempt from the filter that requires the holder ID to be known on the ledger
- Miner will choose tag-along sequencers. Sequencer will choose among several mining transactions based on fee -> fee market with capped max fee.
- If two or more sequencers choose competing (double-spending) mining transactions, convergence requires one lineage to be reverted; many competing transactions therefore slow consensus convergence. The difficulty policy exists to keep this in check. Ideal is only a few competing mining transactions per step.
- note that a sequencer *adopts* a mining tx by consuming its tag-along, so two sequencers on two competitors become **conflicting lineages** resolved by coverage. Competition-per-step maps directly to coverage churn — the difficulty policy tuning "few competitors per step" *is* tuning convergence health, not just spam
**Open**

- how significant advantage of higher CPU power against inventive strategy? Math model would be nice
- simulation would be nice. To what extent it can be useful?
- we will need to implement reference miner in `proxi`. What mining strategy?

## Drafting

We have 525600 minutes per year, let's assume period of 500000 minutes.
We want to distribute $\approx 9 \times 10^{14}$ motes = $9 \times 10^8$ PROX over $500000$ minutes.
That makes $A=1800$ PROX/minute, i.e. with minimum pace $P=6$ slots, arrount 1 minute, 

The decentralization frontier will be in 50000 minutes = 35 days.

So, the target difficulty must be so that it issues 1 mining transaction per 6 slots on the average with A = 1800 PROX  
The equivalent would be 300 PROX per slot.

## 7. Difficulty / contention model (2026-07-06)

Constants used: slot τ = 10.24 s (128 ticks × 80 ms); ≈ 3.08 M slots/year;
one 8-core machine ≈ 1.6×10⁵ signing-attempts/s (measured, see §8); T ≈ 10⁹ PROX,
I = 10⁸ PROX, R_init = T−I = 9×10⁸ PROX; $N = R_{init}/A$ steps; H = aggregate network
attempts/s. P = 1 throughout this section.

### Contention per step

The future-timestamp rule synchronizes release: every valid PoW for pace M found
during the window [0, M·τ] can only be broadcast at slot s+M, so all such solutions
surface together. Their count is Poisson with mean

    Λ(M) = H · M · τ / 2^K(M)        with K(M) = B − M + 1 (P=1)

Λ = expected competing (double-spending) txs per step.

### Gamble/abandon dynamic self-limits Λ to ~1–2

Smaller M wins (earlier maturity) but costs 2× the work per slot dropped. Deviating
from M to M−1 wins iff a solution is found by (M−1)·τ — expected count ≈ Λ(M)/2.
Miners keep pushing the pace down until that is a coin-flip, so the **equilibrium sits
at Λ ≈ 1–2**. P(Poisson(2) > 10) ≈ 10⁻⁵. So under normal self-regulation contention is
~1–2; **the "≤10 competitors" limit is a tail/safety bound, not the operating point.**

Λ(M) explicitly contains H, so the self-limiting is *not* that Λ is H-independent for a
fixed pace — more hashrate at a fixed M means linearly more competitors. The claim is
that **H does not move the equilibrium count; it moves the equilibrium pace.** Inject
more H and Λ at the current pace jumps above 1; miners immediately shorten pace, K(M)=B−M+1
rises, and 2^K(M) grows to re-absorb the extra H, returning the count to ~1–2. H is spent
sliding along the K(M) curve (higher difficulty, shorter stretch S), not on more
double-spends. **This H-independence of the count holds only in the interior of the
curve** — while pace still has room to move. It fails exactly at the P=1 floor below.

### Where ≤10 actually binds: the P=1 floor

Pace cannot drop below P=1. Once H is high enough that the equilibrium wants M<1 it
clamps to 1 and contention can no longer be shed by pace — the count stops being
H-independent and grows linearly with H:

    Λ(1) = H·τ / 2^B ≤ 10   ⟹   B ≥ log₂(H·τ) − log₂10 ≈ log₂(H·τ) − 3.3

So **B is set by the maximum aggregate hashrate to tolerate at full speed:**

| participants | H (att/s) | B for Λ(1) ≤ 10 |
|---|---|---|
| 100     | 1.6×10⁷  | 24 |
| 1,000   | 1.6×10⁸  | 28 |
| 10,000  | 1.6×10⁹  | 31 |
| 100,000 | 1.6×10¹⁰ | 34 |

B ≈ 30 tolerates ~7–10k machines at the floor with ≤10 competitors/step. (Higher than
the "low-20s" solo-miner figure of §8: contention control scales B with *total* network
hashrate, so individual miners rarely win and the network advances ~1 step/slot.)

### Stretch S is pinned by A, not by the difficulty curve

At the floor pace=1, so the fastest possible emission is

    S_floor = N·τ = (R_init/A)·τ        — independent of H and B

- A = 300 PROX → N = 3×10⁶ → S_floor ≈ 0.97 yr
- A = 600 PROX → N = 1.5×10⁶ → S_floor ≈ 0.49 yr

To even reach S = 0.5 yr, A ≈ 600 PROX; A = 300 pins fastest emission at ~1 yr
regardless of hashrate. Above the floor (lower H) pace M̄ solves 2^(B−M̄+1) = H·M̄·τ
and S = N·M̄·τ grows as H falls. The curve span sets max pace M_max = B−E+1, so
S_max = N·M_max·τ. To hold S ≤ 1.5 yr at A=600: M_max ≤ 3 ⟹ B−E ≤ 2.

### Catch: a narrow S band across wide participation needs adaptive B

Pace absorbs hashrate swings only logarithmically and only within [1, B−E+1]. A 3×
stretch band ([0.5,1.5] yr) = pace range [1,3] absorbs just a ~4× hashrate swing; real
participation uncertainty is far larger. With static K(M): high H → pinned at floor,
Λ(1) climbing toward the B cap; low H → saturates ceiling, S balloons, steps stall.
Holding S in band across unknown participation **requires adaptive B** (retarget to
recent pace, Bitcoin-style) — which is also what keeps Λ(1) at the floor from drifting
past 10 as hashrate grows. This is the real justification for the adaptive-difficulty
option, over "target 4 slots".

### First cut (static, P=1)

- A ≈ 600 PROX (N = 1.5×10⁶) → S_floor ≈ 0.5 yr.
- B ≈ 28–31 for the max hashrate expected (B=30 ≈ 7–10k machines); self-regulation keeps
  normal contention ~1–2.
- E ≈ B−2 (M_max≈3 → S up to ~1.5 yr at low participation) — but this shallow curve only
  self-regulates over ~4× hashrate; beyond that, adaptive retargeting is required.

**Open assumption to verify:** propagation is treated as fast relative to τ (10 s), so
"abandon on sight" is effectively instant. If mine-tx gossip latency is a non-trivial
fraction of τ, the abandon dynamic weakens and cohorts widen — check against real
propagation numbers before fixing B.

## 8. PoC miner benchmark (branch `draft/proxi-mine`, 2026-07-06)

`proxi mine` — a draft, singleton-free command that mines a fabricated (fake-input)
mining tx to measure the proof-of-signing-work rate and validate difficulty bands. It
builds two byte templates once from the real `TxBuilder` (essence → txID; full tx → PoW
hash) with located placeholder offsets for nonce, txSlot and signature, then the hot loop
only patches those ranges and hashes. At startup the template is asserted byte-identical
to canonical `TxBuilder` output, so it emits valid transactions.

Measured (8 logical cores):

- **Per attempt = one ed25519 sign ≈ 22 µs/core** (the two blake2b hashes are noise) —
  confirms this is genuinely *proof-of-signing-work*; single-core rate ≈ 45.5k/s.
- **Aggregate ≈ 160k attempts/s** on the 8-core box; parallel scaling is **sublinear**
  (~3.5×, hyperthreading + all-core clock drop). This is the number used as "one machine"
  in §7. (An earlier single-thread×8 extrapolation overstated it at ~380k/s — the reason to
  run a real miner, not a cost model.)
- Solve path validated: attempt counts track 2^K (geometric).

Per-machine solo step time at 160k/s: K=20 ≈ 6.5 s, K=24 ≈ 1.8 min, K=30 ≈ 1.9 h.

Next step to a real miner: replace `fakeBuilder`'s fabricated blobs with a real `mine`-tx
template (open-lock input + `mine(R)` successor) built once from the node's library; the
offset-locating and hot loop are unchanged.

## 9. Adaptive difficulty, doubling deadline, fairness, LRB monitoring (2026-07-07)

§7 shows static K(M) cannot hold contention and stretch together across unknown H. Four
launch realities pin down what adaptive difficulty must actually target, and reframe the
free parameters (A, D_t target, floor pace).

### Retarget the emission *schedule*, not contention or a fixed pace

The retarget signal is the observed on-chain pace — the slot delta predecessor→successor —
a pure function of chain state, so an EasyFL constraint can verify the current difficulty
D_t deterministically (Bitcoin difficulty is likewise a pure function of the header chain).
Carry a small recent-pace EWMA/ring in the mine-chain UTXO state; D_t retargets to hold
observed pace at a target M̄*. B is just D_t's current value; E is a hard floor on D_t.

The right target for M̄* is the **doubling deadline** (below), not "≤10 competitors"
(self-regulated, §7) nor stretch per se (a consequence). Holding pace constant via retarget
makes emission timing **H-independent** — unknown hashrate is converted into a known
schedule. That is the real job of adaptive difficulty.

### (2) Doubling deadline — the control-loss event

> **Superseded by §10**, which redoes this from the shipped constants with the inflation
> term. Two corrections: total = 2I leaves miners at 49.3%, not "mined ≈ initial" — the
> premined stake inflates underneath them, so the real crossing is later; and A also fixes
> the total emission length (t_full ≈ 9·t_decentralization), which this section treats as
> independent. The A/M̄ scaling below is still right.

> **Superseded by §10**, which redoes this from the shipped constants with the inflation
> term. Two corrections: total = 2I leaves miners at 49.3%, not "mined ≈ initial" (the
> genesis pool inflates underneath them); and A also fixes the total emission length
> (t_full ≈ 9·t_double), which this section treats as independent. The A/M̄ scaling below
> is still right.

Once cumulative mint = I (initial supply), total = 2I and mined ≈ initial: initial holders
can no longer out-cover the network — cooperative-consensus control passes to miners
irreversibly. Steps to double = I/A; time:

    t_double = (I/A) · M̄ · τ

| A (PROX) | steps to 2I | M̄=1 | M̄=2 | M̄=3 |
|---|---|---|---|---|
| 300 | 333k | 39 d | 79 d | 118 d |
| 600 | 167k | 20 d | 39 d | 59 d |

Target window 1–2 months. **Failure mode is low H, not high:** if H cannot hold M̄* even at
the floor E, pace climbs and the deadline slips — so E sets the minimum viable launch
hashrate. High H is harmless (D_t just climbs).

### (1) Unknown participation → adaptive is mandatory, and fixes the seed

Launch H is unknown across ~3 orders (≈100 miners month 1 → possibly 10⁴+ later); pace
absorbs only ~4× (§7). So static B is hopeless. Seed D_t at the launch floor (~low-20s bits,
≈100 miners) so early miners aren't stalled; fix E there; let B float up as hashrate
arrives. The span B−E therefore widens over the launch (narrow at low H, wide at high H) —
harmless, since the easy long-pace tail exists but nobody uses it (they'd be outpaced).

### (3) Whales / concentration — what adaptive B does *not* fix

Proof-of-signing-work is the best-fairness PoW: flat marginal cost (1 sign/attempt), no ASIC
moat, pooling hostile (the signature commits to the key-locked reward, §6). So reward ∝ CPU
share, ~linearly, with no scale discount. But **adaptive B controls contention, not
concentration** — a dominant miner still wins ~its CPU share of steps at any D_t. Bootstrap
concentration is bounded only by participation breadth and stretch length: with ≈100 miners
and a realistic power-law CPU distribution, expect the top actor at **~30–50% of month-1
emission**, diluting with participants and time, not with difficulty. Fair-launch = equal
rules, not equal outcome.

Direct tension with (2): fast doubling moves control off the founder quickly but concentrates
early emission among whoever has CPU *now*; slow doubling lets late believers dilute but keeps
founder control longer. The 1–2 month window is where some concentration must be accepted.
Protocol anti-whale levers (per-key input rate-limit) are evadable by free key-splitting and
cut against fair-launch — the real levers are **time + openness**, not a protocol cap.

### (4) LRB monitoring lifts the effective floor pace

A miner reliably sees the predecessor mine-tx only once it is in the LRB, which lags the tip
by d ≈ 1–2 slots. For pace M the release time is (s+M)·τ, but the miner can't start until
~(s+d)·τ — so **any M ≤ d is unmineable**: the slot has already released by the time the tip
is seen. The real floor is

    P_eff = d + 1 ≈ 2–3

Pace-1 requires a live-tangle listener building on orphan-prone unconfirmed tips (a
to-be-built streaming service) — an exotic, high-risk strategy, not the norm. Consequences:

- The sharpest §7 contention point (M=1) does not occur in practice; the floor-binding B
  gains only +log₂P_eff ≈ +1–1.6 bits.
- Natural anti-latency-race: patient LRB miners aren't disadvantaged vs latency-optimized ones.
- d drift is auto-absorbed — retargeting on *observed* pace already folds in the LRB lag.

Cross-coupling with (2): with P_eff≈2, A=300 pushes doubling to ~2.6 months (over target)
while A=600 lands ~39 days. **The LRB-delay floor pushes A up to ≈600 PROX/step** to keep the
doubling deadline in the 1–2 month band — the same A the §7 stretch analysis preferred. Full
emission at A=600, pace 2 ≈ 1 yr.

### Summary of the reframed parameters

- **A ≈ 600 PROX/step** — pinned jointly by the doubling deadline (2) under floor pace (4)
  and by the §7 stretch band. **Revised by §10 to ≈1600 PROX**: the shipped target pace is 4
  (not the 1-2 assumed here) and A scales linearly with it. Shipped value is still 500. **Revised by §10 to ≈1600 PROX**: the shipped target pace is
  4 (not the 1-2 assumed here), and A scales linearly with it. Shipped value is still 500.
- **D_t adaptive**, retargeting observed pace to a schedule target M̄* chosen so
  (I/A)·M̄*·τ ∈ [1,2] months; seed at ~low-20s bits; **E** = launch-floor difficulty (also
  the min-viable-hashrate gate); **B** = D_t floats up with H.
- **Effective floor pace P_eff ≈ 2–3**, not 1 — a consequence of LRB-only monitoring.
- **Concentration** is a launch-breadth/marketing problem, not a protocol one; accept
  ~30–50% top-actor share in the first weeks, diluting over the stretch.


## 10. Sizing A against the decentralization point, with inflation (2026-07-16)

§9 sized A from `t_double = (I/A)·M̄·τ`, which ignores that the premined stake keeps
inflating while mining runs. This section redoes it from the shipped constants under the
right framing, and adds the consequence §9 missed: **A is not a free knob — it fixes the
decentralization point and the total emission length together.**

### Framing

The **decentralization point** is when mined capital crosses 50% of supply, i.e. when it
overtakes the premined stake. Two pools, creator assumed **not** to mine:

- **P(t)** — the creator's premined I, which inflates like anybody else's capital;
- **M(t)** — the mined pool: A every M̄ slots, which also inflates once its holders put it
  on a chain (`proxi node mine --mode delegate`; a raw sigLock payout does not inflate).

The event is `M(t) = P(t)`. Note this is **not** "total supply = 2I", which §9 used as a
proxy: because P inflates underneath the miners, total = 2I arrives while miners hold only
**49.3%**. The proxy is optimistic, and the error grows with the deadline.

### The inflation term, from the constraints

Chain inflation is capped per slot at a value **linear in the chained supply**
(`inflation.easyfl`):

    chainInflationOneSlot(amount, s) = amount / (minimumInflatableAmount0 + s)
    minimumInflatableAmount0         = constTargetBaseSupply / constSlotInflationBase
                                     = 10^15 / 33e6 = 30_303_030 = m0

so the whole supply chained at slot 0 inflates by exactly `constSlotInflationBase` = 33M
motes that slot — which is what the constant *means*. The fractional rate is `1/(m0+s)` per
slot, decaying as slots climb. Branch inflation is a flat VRF bonus, uniform on
[1, `constBranchInflationBonusBaseTail`=5M] ⇒ **b ≈ 2.5M motes/slot** (one canonical branch
per slot), independent of supply.

At genesis, essentially all of I sits on the bootstrap sequencer chain:

| source | motes/slot at genesis |
|---|---|
| chain inflation | I/m0 = 3.30M |
| branch bonus (expected) | 2.50M |
| **total** | **5.80M** |

Premined capital alone grows **+1.47%/month**, +4.4%/3 months, +18.3%/year.

### Model and result

    dP/dt = P/(m0+t) + (creator's share of b)
    dM/dt = M/(m0+t) + A/M̄ + (community's share of b)
    =>  P(t) = (m0+t)·[I/m0 + b_P·L]     M(t) = (m0+t)·(A/M̄ + b_M)·L     L = ln((m0+t)/m0)

Crossing `M = P` gives the closed form

    A = M̄ · [ I/(m0·L(t)) + b_P − b_M ]        L(t) = ln((m0+t)/m0)

**The branch bonus does not matter here.** Sequencers are permissionless, so the creator can
count on only a small share of b — but the answer barely moves either way:

| attribution of b | A for 30 d |
|---|---|
| all to the creator | 1597 PROX |
| ignored entirely | 1587 PROX |
| all to the community (permissionless — the realistic end) | 1577 PROX |

a ~2% spread. The reason is scale: at A≈1600 the mining flow is A/M̄ = 4.0e8 motes/slot
against 5.8e6 of total inflation — **mining is ~69× the inflation flow**. Over a
one-month deadline inflation is a ~1% correction on A; it is not negligible over the full
emission, and it dominates any comparison on year scales.

Note the correction **flips sign** with the framing: for "total = 2I" inflation *helps*
(it contributes supply, A ↓ to ~1550); for the real decentralization point it *hurts* (the
creator's stake is a moving target, A ↑ to ~1600).

### A vs deadline (τ=10.24 s, target pace M̄=4)

| deadline | A (mined delegated) | A (mined idle) |
|---|---|---|
| 14 d | 3403 | 3409 |
| **30 d** | **1597** | 1603 |
| 60 d | 807 | 813 |
| 90 d | 543 | 550 |
| 180 d | 280 | 287 |
| 365 d | 146 | 154 |

A scales linearly with M̄ (only A/M̄ — motes per slot — matters): at 30 d, A ≈ 1163 (M̄=3),
1597 (M̄=4), 1938 (M̄=5) PROX.

### The coupling §9 missed: t_full ≈ 9 · t_decentralization

Overtaking the premine means mining ≈I; exhausting R means mining R_init = T − I = 9I. Same
constant-rate process, so

    t_full / t_decentralization ≈ R_init / I = 9      (independent of A and of the pace)

| A (PROX) | decentralization point | full emission | transits N |
|---|---|---|---|
| 500 (shipped) | 98.1 d (3.2 mo) | 853 d (2.34 yr) | 1_800_000 |
| 1000 | 48.2 d | 427 d | 900_000 |
| **1600** | **29.9 d** | **267 d (0.73 yr)** | 562_500 |
| 2000 | 23.9 d | 213 d | 450_000 |

**A ~1-month decentralization point therefore forces a ~9-month total emission.** That is
structural — set by the I/T split (I = T/10), not by A: raising A shortens both in lockstep.
If a longer tail is wanted alongside a 1-month deadline, the lever is the **I/T ratio**
(mint proportionally more: raise T, or lower I), exactly as §3 of the spec notes for
threshold timing — not A, and not the pace.

### Candidates

| A (PROX) | decentralization point | full emission | transits N |
|---|---|---|---|
| 500 (was shipped) | 98.1 d (3.2 mo) | 853 d (2.34 yr) | 1_800_000 |
| **1000 — ADOPTED** | **47.2 d (1.55 mo)** | **427 d (1.17 yr)** | 900_000 |
| 1500 | 31.5 d (1.04 mo) | 284 d (0.78 yr) | 600_000 |
| 1600 | 29.6 d (0.97 mo) | 267 d (0.73 yr) | 562_500 |

### Decision: A = 1000 PROX

**A = 1000 PROX** (1e9 motes), shipped in `DefaultMineAmount`. This is a *correction*, not a
new policy: only A/M̄ (motes per slot) drives emission, and the original analysis in §1 of
the spec assumed A=500 at the then-expected floor pace M̄≈2 — i.e. 2.5e8 motes/slot. Once
the shipped target pace became 4 (§7), A=500 silently halved the emission rate and pushed
everything out 2× (98 d / 2.34 yr). **A=1000 at pace 4 restores exactly the intended
2.5e8 motes/slot**, and reproduces the figures §1 quotes:

| | spec §1 (A=500 @ M̄≈2) | this model (A=1000 @ M̄=4) |
|---|---|---|
| decentralization / doubling | ≈47 d | 47.2 d |
| full emission | ≈1.17 yr | 1.17 yr |

It also lands inside §9's stated **1-2 month target window** while keeping the longest tail.
A ~1-month deadline (A≈1500-1600) was considered and rejected: it buys ~16 days of earlier
decentralization at the cost of compressing the tail to ~8-9 months, and the 9× coupling
means the deadline pins the tail — the two cannot be separated by A. If a ~1-month deadline
*and* a long tail are ever both wanted, the lever is the **I/T split**, not A.

**Any future change to the target pace must move A with it** — they only ever act as A/M̄.

