# Proxima fair launch philosophy and plan

*Draft. This document describes a design and a set of projections. It will be revised
during and after the pre-launch testnet. It is not an offer, and nothing here is sold.*

Background reading: the [Proxima manifesto and documentation](https://lunfardo314.github.io/)
and the [technical whitepaper](https://arxiv.org/pdf/2411.16456).
The reasoning behind every number here, and the options that were rejected, is in
`launch_rationale.md`.

---

## 1. Goals

The goal of the founder is to launch Proxima as a decentralized ledger and hand over control
of it to a permissionless and decentralized community of token holders — to people seeking
radical innovation for the AI era: a Nakamoto-decentralized ledger without the unbounded costs
of proof of work and without the bottlenecks inherent to blockchains.

Decentralization is the prerequisite for the security of the ledger and the criterion of
the project's success. It is the main feature Proxima was designed for. Not reaching it
would mean failure.

Proxima is a cooperative consensus: a DAG of UTXO transactions where the consensus on the
ledger state comes from token holders cooperating to form a transaction set that covers
the biggest possible amount of their holdings. There are no miners voting, no validator
committee, no block producers, no staking contract. Proxima has only token holders, each
following economic constraints and playing a self-assigned role in the ecosystem, incentivized to
cooperate while seeking their own benefit.

The consequence is blunt: **Proxima is exactly as decentralized as its token holdings are
distributed.** Not as decentralized as its hashrate, its node count, or its governance
forum. If one entity holds most of the tokens, one entity decides the ledger. If nobody
does, nobody does.

That is why the token distribution is not a marketing exercise attached to the protocol.
It is the **security mechanism**. More decentralization, more security.

So the launch has exactly one job: move the token supply into as many independent hands as
possible, permissionlessly.

The starting positions are not equal, and nothing here pretends otherwise. The founder
begins holding everything; everybody else begins at zero. What is equal is the mechanism
that changes that: the rules for minting new tokens are fixed at genesis, identical for
everyone including the founder, and they run in one direction only. Nobody catches up by
being granted anything. They catch up by mining, on terms the founder cannot alter, improve
for itself, or withdraw.

---

## 2. Starting from absolute zero

Today Proxima may be one of the most centralized projects in crypto. One person authored
the concept, the whitepaper, the docs and the code. There is no funding, no team, no
company, no investors, no ICO, no presale, no VC allocation, no treasury, no foundation.
There is a MIT-licensed repository and a whitepaper.

Bitcoin, our benchmark, started the same way and more centralized still: one pseudonymous
author, one PDF, no publicity, one node, and for a while one miner who held the entire
supply and all the hashrate. It became decentralized by attracting participants.

Plus the same "code first" approach: the project is launched with an existing code base that
implements the concept, not a promise to build one.

That is the path here, and it is the only path any ledger has ever taken: **from a fully
centralized system to a fully decentralized one**. We claim this applies to any blockchain,
including proof-of-work systems that start from zero token supply. Every ledger begins as
code written by one entity that nobody else is running, and **decentralizes gradually or
not at all**. What can be engineered is not the absence of an initial center, but how fast
it dissolves, what dissolves it, and whether it can rebuild itself once it is gone.

The liabilities follow the same curve, and this is deliberate:

- at the start there is nothing of value and nothing sold. What is normally sold at a
  launch is a *promise* — a roadmap, a treasury, a team, a product that does not exist yet.
  That is the standard model in crypto, and it is precisely the model that creates
  obligations to somebody. Proxima, like Bitcoin, is **code first**: there is no promise to
  sell, before the launch or at it, and therefore nothing anybody can be owed;
- by the time the ledger has value, it is already outside the founder's control, and there
  is nobody left who could be made to answer for it.

There is no point on that curve where the founder holds both control and something of
market value that carries other people's expectations. Success of the project means taking
the control out of the founder.

---

## 3. Bootstrap capital

At genesis the ledger mints **50,000,000 PROX** (5×10¹³ motes), controlled by the founder —
one twentieth of the one-billion target supply. Call this amount, and all inflation it
generates in the course of operation, the **bootstrap capital**.

### Why it exists

Bootstrap capital is training wheels: it holds the system upright until it has enough speed
to balance on its own.

A consensus weighted by token holdings cannot start from zero token holdings. Coverage is
the anti-Sybil substance of a cooperative consensus, exactly as hashrate is for proof of
work — and at slot zero somebody has to hold it, or there is no ledger to mine on in the
first place. It is a direct logical consequence of not using proof of work to secure the
ledger.

It exists to be outgrown: 5 % of the target supply at genesis, 95 % minted by mining. The
smaller the genesis share, the larger the share that reaches people through mining.

### No distribution

This "premine" is **not** mined before or at genesis: there is no mining for the genesis
supply to precede. And nothing is allocated except to the founder, who exists by definition —
no investors, no advisors, no foundation, no recipients of any kind.

The whole genesis supply sits in one place because at genesis there is exactly one
participant, and a consensus weighted by token holdings requires the coverage to be held by
somebody from slot zero. It is a structural consequence of how the consensus works, not a
distribution event.

Its value at genesis is zero, and nobody was paid with it. A single entity cannot pay itself
with tokens it created out of nothing.

### What it is for, and what it is not

It is used to run the bootstrap network — to produce branch transactions (committed ledger
states) and keep the ledger alive and secure while there is nobody else to do it. During
that phase an attack on the ledger would be self-inflicted: zero cost of attack, and an
infinite cost of consequences — failure of the project.

Nothing is held on anyone else's behalf, and nothing is earmarked for release to anybody.
There is no treasury, no vesting contract, no lock, no distribution mechanism, no schedule,
and no undertaking of any kind attached to it. There are no interested insiders, only people
who started following and understanding the Proxima project earlier than the others: they are
all equal at launch.

The founder holds the bootstrap capital as any other holder holds tokens, with no obligation
to anyone and no commitment about what happens to it. Nobody should expect to receive any of
it, and nobody should plan on the basis that it will or will not move.

Two facts make that position coherent:

- **a change of hands does not weaken the ledger.** The incentive to put tokens to work in
  the consensus is a property of holding them, not of who holds them. Whoever ends up with
  the stake has exactly the same reason to use it. No security property depends on the
  founder specifically keeping it, so no undertaking to keep it is owed;
- **there is nothing here to promise that would not itself be a liability.** Every
  reassuring commitment available — a lockup, a vesting schedule, an undertaking not to
  sell — is precisely the kind of promise that creates an obligation to somebody and invites
  reliance on it. Saying nothing is the stronger position.

What matters is not what the founder promises about it, but what the founder can still do
with it — and what that is, and when it ends, is section 6.

---

## 4. Decentralization capital

**950,000,000 PROX** (9.5×10¹⁴ motes) is not held by anyone at genesis. It does not exist at
genesis. It is a ceiling written into the ledger, minted into existence one transit at a time
by whoever produces a valid proof of work.

Call it the **decentralization capital**, because that is its function: it is the substance
that takes the ledger out of the founder's hands.

Nobody grants it. There is no faucet, no airdrop, no registration, no whitelist, no
application and no distribution act. Nobody — the founder included — can accelerate it,
redirect it, mint it to a different key, or take it back once minted.

What the founder *can* do, for as long as the bootstrap capital can still produce healthy
branches on its own, is slow the process down or stop it. That is a real limitation, it is
temporary, and it is the same position a founder with majority hashrate occupies at the start of any
proof-of-work chain. Section 6 says how long it lasts and why using it would be
self-defeating.

The rules are a covenant fixed in the ledger at genesis, written in EasyFL, running on
every node, readable by anyone: `ledger/def/lock_mine.easyfl`. That covenant is the
authority. This document only describes it.

---

## 5. Mining

For miners. Everything below is enforced by the covenant; where this text and the covenant
disagree, the covenant is right.

### The shape of it

The mine chain is a single chained UTXO, open to everybody — no signature unlocks it, only
compliance with its rules. Each transit of the chain is a transaction that:

- consumes the predecessor and produces the successor;
- mints the current reward out of thin air;
- pays **at least 99 %** of it to the key that signed the transaction;
- pays **at most 1 %** as a tag-along fee to a sequencer of the miner's choice;
- decrements the remaining-mintable counter by the amount it minted;
- requires a proof of work.

About **907,000 transits** exhaust the mintable supply. Then the chain is dead and no further
token can ever be minted this way.

### The reward is flat, then it grows

The reward starts at **375 PROX** per transit and stays there for the first **~45 days**.
After that it grows linearly, by a fixed small amount every slot, ending near 1900 PROX when
the last transit lands.

The flat phase is not an arbitrary interval. It is exactly as long as the period in which
the bootstrap capital can still hold a majority — the period, in other words, in which the
founder can still stop the network. So the reward is constant for as long as the network is
centralized, and it starts growing the moment it is not.

Emission only advances when somebody transits the chain, so a reward too small to attract
miners in the first weeks would stall it and thereby *lengthen* the centralized period.
Growing afterwards keeps a transit worth attempting as difficulty rises with hashrate, which
is what carries the long tail to exhaustion.

The reward per transit is therefore lower early and higher late. Difficulty moves the other
way: it tracks whatever hashrate shows up, so it is lowest when there are fewest miners. A
given amount of CPU wins a larger share of the transits early and a smaller share late.

### The proof of work

`blake2b` of the **whole signed transaction** must end in at least `K` zero bits. The miner
varies a nonce in the input's unlock parameters, which changes the transaction id, which
changes the signature, which changes the hash.

Every attempt therefore requires a **fresh Ed25519 signature under the miner's own key**.
This is not conventional hashing, and it has three consequences that are the whole point:

- **Not outsourceable.** The private key has to sit inside the hot loop. Hand it to a pool
  and you hand over the reward, because the covenant forces ≥ 99 % of every payout to the
  signing key. There is no way to buy hashrate without buying trust in whoever holds your
  key. Mining pools — the single largest source of concentration in every proof-of-work
  chain ever launched — have no foothold here.
- **ASIC-hostile.** The inner loop is a signature, not a bare hash. Special hardware
  can shave a constant off it; it cannot build the orders-of-magnitude moat that a bare
  hash function invites.
- **CPU-egalitarian.** Flat marginal cost per attempt, no economy of scale, no discount for
  size.

Call it **proof-of-signing-work**.

### Difficulty and competition

Difficulty is **adaptive**: the covenant raises and lowers it to hold the pace at roughly one
transit per **41 seconds**, whatever hashrate shows up. The exact retarget rule
is in the covenant.

Everything else follows from this being a proof-of-work race on a chain, and will be
familiar: many miners work on the same tip, one lands the transit, the rest lose that round
and move on. Competing transits for the same step are double-spends of the mine-chain
output; the ledger settles them like any other double-spend. Miners build on the longest
mine chain — the one with the most work behind it.

Every node exposes a stream of mining transactions as they arrive, and the reference miner
subscribes by default. This matters for fairness more than it looks: without it, the only
way to learn that somebody else won a height is to wait for the ledger to confirm it, which
takes longer than mining a transit does — so whoever won once would stay ahead forever.
With it, that lead collapses to a gossip hop. A miner can subscribe to several independent
nodes at once, which makes withholding by any single node ineffective, and it verifies every
transit it receives from the raw bytes rather than trusting whoever relayed it.

The reference miner is `proxi node mine`. It is good enough, and it is not privileged in any
way. Write your own if you prefer; the only thing that decides anything is whether your
transaction is valid.

### The work stops

Proxima does not burn energy to defend the ledger, and never will. The work here is spent
once, to put tokens into the hands of everyone who wants them — because in a cooperative
consensus, distributing the tokens *is* securing the ledger. When the last transit lands,
the energy cost of Proxima becomes negligible and the ledger keeps running, secured by the
distribution the mining produced.

That is the difference this project exists to demonstrate.

---

## 6. When Proxima becomes decentralized

None of the numbers below is a promise. They are consequences of the covenant and of
arithmetic.

### The rule everything follows from

A branch — a committed ledger state — is healthy, and accepted by the network as a valid
tip, only if it carries more than **7/12** of the coverage. Everything in this section is a
corollary of that one constant. Read it three ways:

- **From the founder's side.** For as long as the bootstrap capital holds more than 7/12,
  the founder can produce healthy branches alone — which is another way of saying the
  founder can still *stop the network* by refusing to. When the decentralization capital
  passes **5/12**, that ability is gone: the network is healthy without the founder, and
  there is no way to get the ability back.
- **From the community's side.** When the decentralization capital passes **7/12**, the
  ledger can be run without the bootstrap capital at all. The founder becomes an ordinary
  holder — not by stepping aside, but because it no longer matters whether it does.
- **From an attacker's side.** Two healthy forks would need 7/12 each and there is only
  12/12 to go around, so an adversary needs the overlap — **2/12 = 1/6** of the supply — to
  keep two disconnected healthy forks alive at once. That 1/6 is the network's safety
  margin, and 7/12 is where it comes from. The constant is a parameter of choice, not a law
  of nature: raising it makes forking dearer and stalls more likely, lowering it does the
  reverse. It is the network's balance between safety and liveness, and it can be adjusted.

### Projections from genesis, at the covenant's target pace

| Milestone | Condition | Projection |
|---|---|---|
| Founder can no longer stop the network | decentralization capital > 5/12 | **~45 days** |
| Decentralization capital overtakes bootstrap capital | > 1/2 | **~62 days** |
| Ledger can run without the bootstrap capital at all | decentralization capital > 7/12 | **~82 days** |
| Emission complete | ~907,000 transits | **~14 months** |
| End state | decentralization capital share | **~95 %** |

The first date and the length of the flat reward phase are the same 45 days, by
construction — that is what the schedule in section 5 is for.

### Why participation, not holding, is what counts

Coverage, not holdings, is what the thresholds measure — and coverage is capped by
holdings. A token that sits idle contributes nothing to either side.

This is not a flaw in the accounting; it is the incentive the whole model runs on: **every
token is incentivized to participate in the consensus, and the decision belongs to its
holder.** Tokens that participate earn inflation; tokens that do not, do not. There should be
no class of passive hodlers in Proxima: they are disincentivized by design.

The systemic consequence is worth stating directly: since a healthy branch needs more than
7/12 of the coverage, **more than 7/12 of the supply has to be actively participating at
any time, or branches stop being produced.** A Proxima where most tokens sit still does not
become a slow Proxima; it becomes a stalled one. The incentives exist to make that outcome
expensive, and the milestones above assume miners do what those incentives push them
toward — running a sequencer with mined tokens or delegating them, rather than sitting on
them. How reliably that happens in practice is an open question; see section 9.

### Until then

For as long as the bootstrap capital holds more than 7/12 of the coverage, the founder can
halt the network or refuse to include mining transactions. During the bootstrap period, the
process described in this document runs because the founder lets it run.

This is not a peculiarity of Proxima. A founder who launches a proof-of-work chain with
more hashrate than anybody else, and who controls access to the nodes, is in exactly the
same position for exactly as long, and every chain in existence passed through that
interval. It is what launching means. What can be engineered is how long it lasts and what
ends it — not whether it happens.

What bounds it is not a promise but the fact that using it is self-defeating. Proxima makes
one claim: that a ledger can be Nakamoto-decentralized without proof of work securing it. A
Proxima that stays under its founder's control has not made that claim, it has refuted it,
and it is worth nothing — to the founder first of all. Nor is it a quiet lever: withheld
mining is visible on-chain to anyone watching, and it does not work in moderation. The only
way to keep control is to stop mining altogether, publicly and indefinitely, which ends the
project rather than saves it. The alternative is to let it run, which ends the control
instead. Those are the two options, and one of them is worthless.

Note what this power is not. It is **negative only**. The founder can delay the process; the
founder cannot direct it. Nothing in it lets the founder choose who mines, mint to a key of
its choosing, or take back a token once minted — the mining covenant forbids all three, from
slot zero, permanently.

The bootstrap capital itself is a different matter and should not be confused with the
above. It is ordinary tokens, and the founder can move them as any holder can. The covenant
governs what is *minted*, not what is already *held*.

---

## 7. Supply

50 M at genesis, 950 M mintable, and inflation on top of both.

Proxima's supply is not a deterministic curve of the Bitcoin kind, and it is not meant to
be. Inflation accrues only to capital that participates in the consensus, from two sources:
a chain inflation whose rate is a decaying fraction of the participating amount, and a flat
per-slot bonus to whoever produces the committed ledger state. The flat part is large
relative to a small supply and negligible relative to a large one, so observed supply growth
is high at the beginning and falls quickly as the supply fills in. How much is realized
depends on how much capital participates and on how mining goes — that is, on how people
respond to incentives. Every supply figure in this document is therefore an approximation,
and total supply will pass 1 billion PROX and keep growing slowly thereafter.

This is closer to how an economy works than to a fixed emission schedule, and it is
deliberate. The alternative — rewarding capital for doing nothing — is precisely the failure
mode a coverage-based consensus cannot afford.

---

## 8. Two networks

### Pre-launch testnet

The protocol and the node's code will be the ones intended for the main launch. Mining will
be open and permissionless. The goals:

- tune the constants against real hashrate and real participation;
- find bugs and attack vectors while nothing is at stake;
- reach the people who will run nodes, sequencers and miners on the real network, and other
  contributors.

Expected duration: a few months.

**The testnet ledger will be destroyed** before the founder loses control of it. This is
announced in advance, deliberately, and is not a contingency: the ledger will be discarded
and genesis regenerated with fresh keys before the main launch, at a moment chosen by the
founder. **Tokens mined during the pre-launch testnet carry no value of any kind, confer no
rights, will not be honoured, will not be carried over, and will cease to exist.** Anyone
mining on the testnet is doing so for the exercise.

### Main launch

Fresh genesis, fresh keys, same protocol and same mining covenant — or transparently
modified according to what the pre-launch phase teaches. From that point the covenant is
fixed and the schedule in section 6 runs. As long as it runs, nobody — the founder
included — can change the rules, the amounts, or the destination of a single mined token.

---

## 9. Risks and open questions

Stated because they are real, not because they are resolved:

- **The bootstrap interval.** Until the 5/12 threshold is crossed, the founder can stop the
  network or withhold mining. Section 6 says why that is bounded and self-defeating, but it
  is real, and it is the period in which everything else on this list also bites.
- **The length of the runway.** The whole design turns on one number nobody has measured:
  how long it takes for enough capital to be mined *and put to work* that the network no
  longer needs the bootstrap capital. 45 days is a judgement, not a measurement. Too short
  and the ledger stalls instead of decentralizing; too long and the founder's period of
  control is longer than it needs to be. The pre-launch testnet exists in large part to
  replace that judgement with a measurement.
- **Participation mechanics.** The milestones assume mined tokens are put to work: either run
  in a sequencer the miner controls, or delegated to somebody else's. The reference miner now
  delegates its payouts by default and can add to an existing delegation rather than only
  creating new ones, but how a miner should behave across many sequencers over a long run is
  still being learned rather than known.
- **Scalability of participation.** Many holders delegating to many sequencers is a regime
  the network has not yet been run in. How many delegations a sequencer can carry, and how
  that behaves as holders and sequencers multiply, has been modelled and now needs
  measuring.
- **Early concentration.** Whoever shows up first with CPU will take a large share of the
  first weeks' emission — expect the largest single actor somewhere around a third to a half
  of month-one emission. Non-outsourceability and the absence of an ASIC moat limit how
  large, the flat opening reward keeps the first weeks from being disproportionately
  valuable, and the ~14-month tail dilutes what remains. It will not be even. **Fair launch
  means equal rules, not equal outcomes.**
- **Where the launch is announced decides who mines first**, and therefore what early
  concentration looks like. Reaching the people who would mine is a practical problem rather
  than a protocol one, and it is unresolved. The two-network launch of section 8 is meant to
  mitigate it.
- **The rising reward is a design choice.** Section 5 gives the reasoning. It has not been
  tested against a real launch.
- **Information asymmetry between miners.** Whoever produces a transit knows it first — left
  alone, fatal to fairness, since the producer's lead outlasts the time to mine the next
  transit. This is broadly similar to the known *selfish mining* problem in proof-of-work
  networks. Addressed and shipped: nodes stream mining transactions as they arrive, the
  reference miner subscribes by default and to several nodes if asked, verifies what it
  receives itself, and never prefers a transit merely for being its own. What remains is one
  gossip hop, which decides anything only if difficulty falls so low that solving is far
  faster than the pace. On the testnet it does not: difficulty sits around twice its floor
  and a solve takes about as long as the pace allows, so work decides heights, not latency.
- **Difficulty tuning.** Adaptive difficulty on a chain with a miner-chosen pace is subtle
  and has been through several iterations. The current design holds the target pace on the
  testnet without the oscillation the earlier ones showed, but it has not yet been tested
  against hashrate arriving in the amounts a real launch would bring.
- **Everything else.** This is new consensus, new tokenomics, and unproven code. It may
  fail outright.

---

## 10. No offer, no promise, no expectation

- Nothing is offered or sold, at any price, to anybody. There is no sale, no subscription,
  no allocation, no fundraising, and no mechanism by which anyone can pay the founder for a
  token.
- Nothing in this document is an undertaking, guarantee, or commitment to anyone. Statements
  about the future describe how the software is designed to behave and the founder's present
  view, not obligations owed to any person.
- PROX has no issuer, no counterparty, no redemption, no backing, no support obligation, and
  no promise of value, liquidity, listing, or a market. The founder provides no service,
  holds nothing for anyone, and has no roadmap obligation to anyone.
- Tokens minted through the mine chain are created by the person who mints them, under rules
  fixed at genesis, out of nothing. They are not transferred, granted, distributed, or
  released by the founder, who cannot do any of those things.
- Anyone who acquires or mints PROX does so entirely at their own risk and on their own
  judgement.
