# Branch bonus set by work

> RESEARCH (2026-11-10). Nothing here is built. Question: the 5 PROX cap of the branch inflation
> bonus pays for running many sequencers; can a cap that the sequencer grinds, the number of
> trailing zero bits of the branch's transaction ID, move the reward from sequencer count to
> equipment? Earlier attempt, a mineable bonus reverted in May 2026:
> `kb/archive/superseded/branch_cost.md`. Numbers: `network_snapshot_2026-11-02.md`.
>
> **For discussion, nothing decided.** Two variants: section 3 drops the VRF and lets the work alone
> set the bonus; section 6 keeps it, work scaling the cap and the VRF draw inside it. The argument for
> keeping it: the VRF is the consensus tie-breaker, and its randomness keeps the best-equipped
> sequencer from dominating.

## 1. The problem

Today (`ledger/def/inflation.easyfl`, `branchInflationBonus`):

    B = randomFromSeed(beta, 5,000,000) + 1          beta = VRF output, message = hash(predecessor proof) || slot

The bonus counts in the branch's coverage, so among branches of equal coverage the larger bonus
wins; in 92% of slots of the 2026-11-02 snapshot the top candidates tie and the bonus decides.

- Every sequencer identity is one independent draw, so the share of tied slots won is the share of
  identities. Hardware, effort and latency do not matter once the branch arrives before tick 9.
- The prize is the largest of the tied draws, not one draw: with k branches tied, its average is
  `5 * k/(k+1)` PROX, about 4.2 to 4.4 PROX for the 5 to 8 candidates of a typical slot, about
  36,000 PROX a day at 8,438 slots. Shared among 100 identities that is about 360 PROX a day each,
  on the 10,000 PROX minimum balance a sequencer needs for a branch (upgrade 6), capital that keeps
  earning chain inflation anyway. One more identity is close to free and pays about 3.6% a day.
  That is the flood.
- beta is chained per key, `hash(predecessor proof) || slot`, so each identity knows its draw for
  the coming slot in advance. Nothing can be done with it today; it matters below.

## 2. The proposal as stated

Bonus capped by work: `B = U * cap(K)`, U the VRF draw, K the trailing zero bits of the txid,
`cap(K) = b + a*K`, the bonus counted F times (F = 10) in the coverage comparison.

**It does not remove the sybil incentive.** K grows with the logarithm of the work, U is uniform
on [0, 1), so U dominates the product. Example: one GPU sequencer at K = 32 against ten CPU
sybils at K = 25 each, a = 1, b = 0: caps 32 and 25, ratio 1.28.

    P(GPU wins) = P(1.28 U > max of 10 uniforms) = (1/1.28)/11 + (1 - 1/1.28) = 0.29

The GPU does about 13 times the work of all ten together and wins 29% of the ties. And since U is
known before grinding, a sybil operator puts all its hashrate behind its luckiest identity, which
makes it worse. An exponential cap (`cap ~ 2^K`, proportional to work) fixes the ratio but mints in
proportion to the network's hashrate; it needs a clamp, and at the clamp the VRF lottery is back.

## 3. Variant: the work alone sets the bonus

Drop the VRF from the bonus. With `D` the number of trailing zero bits of the branch's txid:

    B <= b + a * min(D, Dmax)                        ledger rule, the sequencer claims B
    ties on coverage: W(txid), then txid             W = the txid hash bit-reversed, smaller wins

The cap form avoids the circularity that killed the May attempt (B sits in the outputs the txid
hashes): the sequencer picks a target level, writes `B = b + a*target`, grinds the nonce until the
txid has at least that many zero bits, raises the target if time is left. No difficulty, no
retarget: D is whatever a sequencer reached.

**Why it removes the sybil incentive.** The winner among tied branches is the one holding the best
hash found by anyone. Every attempt is equally likely to be that best one, whichever identity made
it, so the share of tied slots won is the share of attempts. N identities on the same hardware win
exactly what one does, with N minimum balances locked: sybils are neutral, as in Bitcoin. W breaks
ties within one level by the same measure, which is why it replaces the txid as the first tie-break;
with the plain txid, each identity reaching the top level would be one more draw.

Same example as section 2: the GPU wins 128 / (128 + 10) = 93% of the ties.

**Issuance.** One doubling of work adds one bit, so the bonus grows with the logarithm of the
winner's hashrate and stops at `b + a*Dmax`; work above `Dmax` still decides ties through W without
minting more.

Measured on one core (Ryzen 7 8700GE, Go `blake2b.Sum256` of 128 bytes, the cost of an attempt with
the nonce hashed last): 160 ns, about 6 million attempts a second.

| Sequencer | Attempts/s | D in a 0.6 s window |
|-----------|-----------|---------------------|
| 1 core | 6M | about 22 |
| 8 cores | 50M | about 25 |
| one GPU (estimate) | several G | about 32 |

Example parameters: `a` = 0.1 PROX a bit, `b` = 1 unit (as the `+1` today), `Dmax` = 40, F = 10.
A CPU sequencer then mints about 2.5 PROX, below today's average winning bonus of about 4.3;
`a` = 0.17 matches it, `a` = 0.05 halves 2.5. The
GPU edge in the coverage comparison is `F * a * 7` bits = 7 PROX of coverage at a = 0.1.

### 3.1 What it reuses

The upgrade 6 transaction ID, built for the mine chain: BLAKE2b of the transaction without its
signature, the signer's holder ID hashed first (the work is bound to the key and cannot be taken by
another sequencer), the nonce an 8-byte `TxConstraints[0]` hashed last (an attempt compresses one
or two blocks). No branch transaction uses `TxConstraints` today. The Rust seeker and its job
protocol could serve a sequencer as they serve the miner.

### 3.2 What it removes

The VRF proof in `stemLock` and its verification from the slot on: from then the bonus check is a
trailing-zero count on a hash every node already computes. `util/vrf` stays for validating branches
before the slot, but leaves the hot path.

### 3.3 The grinding window

A branch's content is fixed once its predecessor, the final pre-branch milestone stamped 7 ticks
(560 ms) before the boundary, is built. Followers act from tick 9, 720 ms after it, and a branch
later than 4 ticks after the boundary is dropped by the sequencer itself. So the window is about
0.5 to 1 s, and its cost is latency: the sequencer grinds until the last moment its branch still
reaches the capital holders before tick 9. A sequencer close to them can grind longer, but a
doubling of time is one bit, so latency buys little against hardware.

## 4. Downsides and open questions

1. **The bonus concentrates on hashrate.** Intended, but with it the branch wins: one well-equipped
   sequencer wins most tied slots, and with them the settlement of competing mine transits, which
   is sequencer policy (`library_upgrade6_mining.md`, settlement deviation). It turns that section's
   incentive, "run more sequencers, not more hashrate", into the opposite; whether one operator
   winning most slots is acceptable is the question to decide. BLAKE2b ASICs exist (Sia); whether
   they fit this layout is not checked.
2. **Mining and branches compete for the same hardware.** Same hash, same seeker. Hashrate moves to
   whichever pays better per attempt; worth modelling before choosing `a`.
3. **Not a mandatory cost.** A zero-work branch stays valid and free, as the May finding says. The
   proposal removes the economic reason to multiply sequencers; it does not stop an attacker from
   issuing branches, which the minimum balance bounds. Sequencers without hardware may stop issuing
   branches, which lowers the attach load, but nothing forces them to.
4. **Early cut for more work.** A sequencer can stamp its final milestone earlier to grind longer,
   giving up late consolidation. It gains only where the coverage it gives up is under `F * a` per
   bit gained; F bounds that trade.
5. **F touches the coverage definition.** Counting the bonus F times means a node-side change to
   `coverageDeltaAdjustment` and to `A(b)` of `branch_comparison.md`, gated on the slot. The
   accounting must keep the property of `coverage_adjustment.md` (the bonus adjustment): the score
   never decreases when the branch's sequencer output is consumed, so the extra `(F-1)*B` must not
   vanish or count twice at that step. The tie-break by W changes the order in `dag_semantics.md`.
   Both bind a hard constraint and need explicit approval.
6. **Liveness.** No threshold, so no dependence on anyone grinding: with no work every level is 0
   and W decides, as the txid does today.

## 5. Ledger and node changes, if taken

- Library upgrade: on a branch from the slot, `B <= b + a*min(tz(txid hash), Dmax)` in place of
  `branchInflationBonus(vrfProof, slot)` in `chain.easyfl` (the cumulative bonus becomes the
  claimed B); `stemLock` accepts an empty VRF proof and stops verifying it.
- Node, gated on the slot: tie-break by W before the txid; the factor F in the coverage comparison.
- Sequencer: target-level grinding of `TxConstraints[0]` between the final milestone and the branch
  deadline, local workers or the seeker.
- Measure first: how many branches and identities compete per slot, and today's distribution of
  bonus income per identity, to set `a` and see the effect.

## 6. Variant: work scales the cap, the VRF stays

`B = randomFromSeed(beta, cap(D)) + 1`, D the trailing zero bits of the txid, the bonus counted F
times in the coverage comparison. The VRF keeps breaking ties, and luck keeps a share of the slots
for sequencers without hardware.

The trade-off is section 2: each identity is still one draw, so part of the sybil incentive stays.
The steepness of `cap(D)` sets the balance between hashrate and identity count:

- linear, `b + a*D`: hardware moves the cap by tens of percent, identity count still dominates;
- steeper, e.g. doubling every `c` bits: hardware weighs more, minting grows with hashrate until a
  clamp `Dmax`, above which only the draw decides.

Open, beyond section 4: the shape and constants of `cap(D)`, and the draw being known before
grinding (beta is chained per key), which lets a sybil operator grind only behind its luckiest
identity. Binding beta to the branch's predecessor milestone, fixed only shortly before the
boundary, would shorten that foresight to the grinding window itself.
