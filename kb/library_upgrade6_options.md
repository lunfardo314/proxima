# Library upgrade 6: three options for the mine chain

> LIVE, DECISION PENDING (2026-11-05). Compares three candidates for the next library upgrade of the
> mine chain. All three are one upgrade at one slot with miner version 6; none is listed. Builds on
> the external review summarised in `.internal/handoff_upgrade6_review.md`. Figures marked
> *estimate* are reasoning, not measurement. Archive once the choice is shipped.

| Option | Spec | Work | Payouts | State |
|--------|------|------|---------|-------|
| **A** | `kb/library_upgrade6.md` | ECVRF output under the signer's key (as today) | enforced: every output pinned, full reward a strict top-up | built on `feat/upgrade6` |
| **B** | `kb/library_upgrade6_1.md` | BLAKE2b of predecessor, slot, signer key, nonce | enforced, as A | spec, `feat/upgrade6-1` |
| **C** | `kb/library_upgrade6_2.md` | the transit's txid (BLAKE2b of the transaction without its signature) | fixed and plain: one `sigLock` of A − 1 PROX to any holder, one 1 PROX fee tag-along | spec, `feat/upgrade6-1` |

## 1. The choice in one paragraph

A keeps mining non-outsourceable in principle, but weak and medium keys get a speed edge no ledger
rule removes, and a miner with its own sequencer keeps a weak key's rewards out of reach. B removes
every key trick but allows pools and widens the GPU lead, and keeps upgrade 6's forced delegation.
C is the plainest proof of work, with the least ledger code and no exploit of its own; every reward
is a plain output, and delegating it is left to the miner and the consolidator. A and B force
delegation but share two escapes from it, so the forcing only charges a price to those who would not
delegate; C has nothing to escape.

## 2. Option A: VRF, enforced payouts

### Exploits

**A1. The weak-key speedup stays, and medium keys are invisible.** An attempt costs `Gamma = x*H`.
With scalar 1 the multiplication disappears and an attempt runs about 5x faster (the draft's figure,
not measured). Such a key is public (its public key is the base point), so upgrade 6 makes its
rewards a race. A **medium scalar** is worse (*estimate*): `x*H` for a k-bit scalar costs about
k/253 of a full one, so a 128-bit scalar gives about 1.6x and a 96-bit one about 2x. Recovering a
scalar known to lie under 2^k costs about 2^(k/2) curve operations: 2^64 at 128 bits, out of reach;
2^48 at 96 bits, days of GPU time per key. A miner can take a 1.6-2x edge with a key nobody will
ever recover, and nothing in the ledger sees it. Using such a key needs a non-standard signer (an
ed25519 seed always yields a 255-bit scalar), no obstacle for a miner already running custom code.

**A2. Private inclusion through the miner's own sequencer** (from the review). The transit is stamped
at tick 83 or later, the reward is spendable from tick 95, the milestone carrying both goes out by
tick 98, all in one slot. Nobody sees the transit before its reward is already spent, so there is no
public window to contest it. Option 2 makes it simple: a delegation origin cannot be frozen, so its
master spends it at once. Option 3 into the miner's own delegation is likely safe too: the miner's
sequencer serves its own askstops and ignores everyone else's, and acts first when a revocation
window opens. *Estimate*: a scalar-1 miner wins about 5x the transits, so about 5 x 50 = 250 PROX
per unit of hardware through option 2, against 95 for an honest miner. **Upgrade 6 does not remove
the reason to run a sequencer just to mine.**

**A3. The deterrent is theft.** A public weak key loses its rewards only if someone runs bots that
take them. The project cannot run or encourage them. Without them there is no deterrent; with them
the race goes to whoever controls inclusion, again sequencer operators.

**A4. Seekers hold the reward key.** A VRF seeker needs the secret scalar; a miner renting GPUs puts
the key that controls all its rewards on machines it does not control.

### Strategy

- The VRF does not deliver what it was chosen for: with scalar 1 the key is public, anyone can mine
  for it, and the reward goes to whoever controls inclusion. Honest miners pay about 5x per attempt
  for a property the cheaters do not respect.
- More sequencers for the wrong reason: the count (68 on 2026-11-05, rising) is driven by the exploit.
- A fairness story that is hard to defend: a 5x or an invisible 2x edge comes from a trick, not
  hardware.
- Complexity stays: an in-house RFC 9381 prover in every miner's key path, a split prover, a Rust
  twin kept byte-equivalent.

### Behaviour

Sophisticated miners move to weak or medium keys plus an own sequencer. Honest miners' option 3
targets, which must also include the transit, drift to large, reliable sequencers. Thief bots, if
any, hit only weak-key miners without a sequencer and push them to run one.

### Limits of fixes on this path

A reward maturity forces a public window, but the race in it still starts with the miner's own
sequencer. Nothing removes the speedup or reaches the medium keys.

## 3. Option B: key-bound hash, enforced payouts

### Exploits

**B1. Pools, and with them concentrated capital placement.** Work the ledger checks without the key
can be done without the key. A pool publishes (predecessor, slot, its public key), workers send
shares, the pool pays them off-chain. Workers cannot steal a solution, but under enforced payouts
every reward lands under the pool's key, so the pool is the master of every delegation its rewards
open or top up, chooses which sequencers that capital backs and can move it in each revocation
window. A worker's preference is a promise the ledger cannot enforce, and one payout output per
transit rules out paying workers directly. The mine chain's total emission (940 M PROX at genesis,
part mined since) is many times the genesis supply, so a few pools could place most capital in
consensus, likely through sequencers of their own.

**B2. The usual pool risks**, off-chain: a pool can cheat its workers, workers can withhold full
solutions while submitting shares, and a pool holding unpaid rewards is a custodian.

**B3. Emission can be bought** with rented hashrate. It cannot reorder the ledger (the mine chain
takes no part in consensus), and liveness is unaffected when it leaves.

**B4. Custom hardware becomes worthwhile sooner.** *Estimate*: the GPU lead per card over an 8-core
CPU grows by about an order of magnitude over the VRF's 10x. BLAKE2b has existing chips (Sia's, for
an 80-byte header); the 77-byte message does not match them, reuse not verified.

### Strategy

CPU miners become irrelevant; emission goes to GPU owners and to whoever rents hashrate. Fewer,
larger parties place mined capital. Migration: a difficulty jump at the slot (about 9 bits on CPU, 12
or more with GPUs, *estimate*), about 16 minutes of contested slots, a new seeker protocol.

### Behaviour

GPU miners and pools take most transits within weeks; pools compete on fees and on how they place
capital; the reason to run a sequencer just to mine shrinks to S3.

### What it fixes

Weak and medium keys gain nothing; seekers need no key; the ingress floor gate checks the exact work
value; validation is one hash; a transit is 80 bytes smaller; the in-house prover leaves the miner.

## 4. Option C: transaction-hash work, one plain reward

Details in `kb/library_upgrade6_2.md`. The work is the transit's txid; the payout is fixed: the
successor, one plain `sigLock` of A − 1 PROX to any holder, one plain tag-along of the fixed 1 PROX
fee; no transaction-level constraints, so the nonce is the only free field.

**C1. Pools, rented hashrate, hardware** as in B. Pools are custodial (one reward output), and their
rewards are plain outputs rather than delegations.

**C2. Settlement bias by an own sequencer** (S3), the same as under A and B: the fee is fixed and
there is one tag-along, so inclusion cannot be bought.

Nothing else: the payee and the sequencer paid are in the hash, so a solution cannot be redirected;
only the nonce is free, so every miner pays the same per attempt; the fee's sender must be the
signer, so a re-signed copy is invalid.

Strategy: **mined tokens land idle, and delegating them is the miner's choice.** A smaller loss than
it looks: enforcement under A and B can be bypassed (S1, S2), `proxi node consolidate` already sweeps
plain outputs into delegations automatically, and holders know delegation is how capital earns. The
share of mined supply in consensus then depends on how many miners run the consolidator: to be
measured after activation, with the docs and the miner's defaults as the remedy, not a ledger rule.
In exchange: the plainest proof of work, the least ledger
code, nothing to escape, verification free (the txid is computed anyway), mining from no funds as
today.

Behaviour: miners take 94 PROX liquid per transit, those running the consolidator delegate it once
their plain outputs pass its threshold; custodial pools form; GPU farms dominate.

## 5. Shared problems

- **S1. The reclaim escape (A, B).** The top-up guarantor steps aside after `constTagAlongSlots` (30
  slots) so the sender can reclaim an unserved request; a miner names a delegation that does not
  exist and takes the full A (95 PROX) back as idle tokens five minutes later. Closures in
  `kb/library_upgrade6_1.md` 2.1.
- **S2. Option 2 withdrawn at once (A, B).** A delegation origin cannot be frozen, so a miner can take
  option 2 and spend its 50 PROX on the master path immediately: forced delegation is a price, 50
  liquid against 95 delegated, not a rule.
- **S3. Settlement bias by an own sequencer (A, B, C).** "Settle an own transit only when no better
  validated competitor exists" is sequencer policy (`sequencer/mine`), not a ledger rule. A modified
  sequencer includes its own transit regardless and wins the slot whenever its branch wins.
- **S4. Hardware (A, B, C).** None keeps a CPU competitive with a GPU; they differ in how much.
- **S5. The txid does not commit to the signer (A, B, C).** Under A and B the signer is bound by the
  work and the payouts, under C by the fee tag-along, whose sender must be the signer. How the node handles transactions that differ only
  in their signature data is being reviewed separately; any fix is independent of this choice.

## 6. Side by side

| | A: VRF, enforced | B: key hash, enforced | C: tx hash, plain reward |
|---|---|---|---|
| Cost of an attempt | equal only for honest keys; scalar 1 about 5x cheaper, medium keys 1.6-2x (*estimate*) | equal | equal (only the nonce is free) |
| Weak or medium keys | public ones contestable (a race); medium ones invisible and safe | irrelevant | irrelevant |
| Own sequencer as a weak-key shelter | yes (A2) | no | no |
| Deterrent relies on | others stealing weak-key rewards | nothing | nothing |
| Pools | in practice no | yes, custodial (one payout output) | yes, custodial (one reward output) |
| Who places mined capital | the miners, many keys | pool operators and large miners | each payee: plain outputs, idle unless delegated |
| Mined tokens put to work | forced, with escapes S1, S2 | forced, with escapes S1, S2 | the miner's choice; the consolidator automates it |
| Inclusion | fixed fee, cannot be bought | fixed fee, cannot be bought | fixed fee, cannot be bought |
| Reason to run a sequencer just to mine | strong (A2, S3) | settlement bias only (S3) | settlement bias only (S3) |
| CPU vs GPU | GPU about 10x per card | about an order of magnitude wider (*estimate*) | as B |
| Custom hardware | hard | straightforward | straightforward |
| Buying emission with rented hashrate | limited | easy | easy |
| Seeker holds the reward key | yes | no | no |
| In-house crypto in the miner's key path | yes | no | no |
| Ingress floor gate | decodes an unverified proof | exact, one hash | exact, free (the txid) |
| Validation of the work | a VRF verify | one BLAKE2b | none extra |
| Transit size | about 700 bytes | about 620 bytes | about the same as B |
| Signer bound to the transit (S5) | yes, by the VRF and the payouts | yes, by the hash and the payouts | yes, by the fee tag-along |
| Ledger code | built | three mine functions | four mine functions plus upgrade 6's fee rule; its other payout rules removed |
| Node, miner, seeker code | built | work value by slot, miner loop, seeker | as B, plus template hashing and the unknown-sender exemption for the new layout |
| Activation | difficulty unchanged | difficulty jump, about 16 minutes contested | as B |
| Fair-launch story | key tricks beat hardware | money buys hardware | money buys hardware; mined tokens free to idle |

## 7. Whichever is chosen

- Under A or B, close S1 (the reclaim must create a delegation) and S2 (a mined delegation origin
  frozen, or not spendable for some slots) in the same upgrade, or forced delegation stays a default.
- S3 cannot be closed by a ledger rule today; measure how often a sequencer settles its own transit
  over a better competitor before deciding whether it matters.
- Correct `kb/library_upgrade6.md`'s description of the reclaim window: the sender alone from 30
  slots, anyone from 390.
