# Library upgrade 6.1: a hash-based proof of work for the mine chain

> SPEC, NOT BUILT. Option B of three (`kb/library_upgrade6_options.md`). Branch `feat/upgrade6-1`,
> made from `feat/upgrade6` (2026-11-05). Library upgrade
> 6 (`kb/library_upgrade6.md`) with one change: the mine chain's proof of work becomes a plain
> BLAKE2b hash instead of the ECVRF output of `kb/mine_vrf_pow.md`. Every payout rule of upgrade 6
> stays as built. Upgrade 6 is neither listed nor released, so this is **one upgrade, one activation
> slot, miner version 6**. Prerequisite: upgrade 5 active (slot 153000).

## 1. Why

The VRF work function was chosen for one property: an attempt needs the secret key, so the work is
*non-outsourceable* and the key, the signer and the payee are one (`kb/mine_vrf_pow.md` 2.3). It cost
more than expected:

- **Weak keys.** An attempt costs a scalar multiplication `x*H`; a key with a small scalar (`x = 1`)
  skips most of it and runs about five times faster. The ledger cannot recognise such a key; upgrade
  6 can only make its rewards contestable.
- **Complexity.** An in-house RFC 9381 prover in every miner's key path, a split prover for the hot
  loop, a Rust twin kept byte-equivalent for the seeker, and a seeker that must hold the private key.

The VRF also never bought hardware equality (`kb/mine_vrf_pow.md` 7). Under a hash the key is not in
the attempt, so a weak key gains nothing. The price is non-outsourceability: pools become possible
(section 6.3).

## 2. Upgrade 6's payout rules stay

Upgrade 6 forces every reward into a fixed layout: the opening a plain `sigLock` to the signer, a new
delegation a plain delegation with the signer as master, the full reward a strict top-up request
into a plain delegation of the signer, the fee a plain tag-along. They serve two purposes, and only
the second depends on the work function:

- **The reward goes to work, not idle.** The full reward A is the bulk of what a transit mints, and
  under upgrade 6 it can only top up a delegation, which backs a sequencer in consensus. The plain
  tag-along of today, which a miner can send to its own sequencer's chain, is gone. The pricing of
  the three options (`kb/mine_payout_options.md`) makes the idle opening the poorest choice. This
  purpose is unchanged by the hash and is the reason the rules are kept.
- **A weak key leaves every reward open.** Pinning every output element by element, and the
  plain-delegation rule of the strict form, stopped a weak-key miner from hiding a reward behind a
  script or a fake delegation. Under a hash nobody has a reason to use a weak key, so these parts
  become defence in depth. They cost nothing honest miners notice, since they match what
  `proxi node mine` builds, so they stay as built.

The rules, their reasoning and the guarantor's two forms are in `kb/library_upgrade6.md` and
`ledger/def/upgrade6.easyfl`; nothing of them changes here. In particular the guarantor's check that
the predecessor is a `delegateLock` output is what makes the forced top-up a real delegation: without
it, a chain under a sequencer's own `sigLock` with a frozen `delegateLockState` passes as one.

### 2.1 The escape left open: reclaiming the request

**Forced delegation can be escaped with a five-minute delay.** The guarantor steps aside once the
request is `constTagAlongSlots` (30) slots old, so that the sender can reclaim a request no sequencer
served; from `constTagAlongReclaimSlots` (390) anyone can take it. A miner who wants idle tokens names
a delegation that does not exist (the produced arm checks only the argument's length), or a target
that is down or whose `MinTopUp` is above A. Nobody serves the request, and 30 slots later the miner
reclaims the full A, 95 PROX, to a plain output. That is more than the opening's 15 PROX and delegates
nothing. The transit itself is not held back: it enters the DAG when the next transit consumes the
mine output, whether or not its tag-along is taken.

Under a VRF with a weak key this did not matter, since the reclaim was a race; with a strong key the
miner reclaims alone. Under either work function it defeats the purpose of section 2.

Possible closures, none built:

- **The reclaim must delegate.** In the step-aside branch of the strict form, the guarantor requires
  the consuming transaction to produce a delegation origin with the sender as master and at least the
  request's balance, named by an output index in the unlock parameters, the way `ensureStopDelegation`
  guarantees its payment. A request nobody serves still ends in a delegation, to a target of the
  sender's choice. Pure EasyFL, one function of the strict form. The anyone-path at 390 slots needs
  the same rule or the reward is open to the first taker, which then has no reason to delegate.
- **No step-aside for the strict form.** Simpler, but an unserved request is lost for good, for
  instance when the target sequencer stops: a cost on honest miners for a rule aimed at others.
- **Accept it.** The delay and the reclaim transaction are small costs; the forcing would then be a
  default rather than a rule.

The first keeps the rule whole at the cost of one more guarantor check. It is the recommendation, to
be specified before the slot is chosen.

## 3. The proof of work

From the activation slot, the proof of work of a mine transit is

```
w = blake2b-256( predecessorOutputID (33) || txSlot (4) || signerPublicKey (32) || nonce (8) )
```

and the low 64 bits of `w` (its last 8 bytes, big-endian) must end in at least
`K = _mineRequiredK(B, M)` zero bits, tested with the existing `_minePoWOK`. The unlock parameters of
the consumed mine output at the lock element shrink from 88 bytes (proof and nonce) to the **8-byte
nonce**.

| Part | Purpose |
|------|---------|
| predecessor output ID | no work before the predecessor exists, no stockpiling; as today |
| `txSlot` | an attempt is an attempt for one target slot, so the pace model and the retarget keep their meaning; as today |
| signer public key | **binds the solution to its payee.** Without it, anyone seeing a transit in gossip copies the nonce, signs a transit of their own and gets the same `w`; the tie would go to the txid and half the stolen solutions would win. With it, a copied nonce under another key is a different `w` |
| nonce | the search variable, last, so that everything before it is fixed for the whole search |

The signer is the beneficiary under every payout option (opening to the signer, delegation master the
signer, top-up request from the signer into the signer's delegation), so binding the signer binds the
reward. The option and layout are not in the message: they are the key holder's choice, and only the
key holder can sign.

The message is 77 bytes, inside one BLAKE2b block (128 bytes), so every attempt is exactly one
compression, with no fixed first block to precompute.

Added to `ledger/def/upgrade6.easyfl`, same arity; nothing else of `lock_mine.easyfl` changes:

```
func _mineNonce  : selfUnlockParameters
func _mineWork64 : tail(blake2b(inputIDByIndex(selfOutputIndex), txSlot, signaturePublicKey(txSignatureData), _mineNonce), 24)

func _minePaceAndPoW :
and(
   require(equalUint(len(selfUnlockParameters), u64/8), !!!mine_unlock_params_must_be_the_nonce),
   require(_minePoWOK(_mineWork64, _mineRequiredK($0, _mineM)), !!!insufficient_mine_proof_of_work)
)
```

`blake2b` is an existing Proxima builtin (`ledger/crypto_builtins.go`, variadic, concatenates its
arguments), so the change is pure EasyFL. `_mineProof`, `_mineAlpha` and `_mineBeta64` become unused
and stay harmlessly. The header and `_minePaceAndPoW` comments are rewritten: the
"non-outsourceable" wording goes.

Every transit takes the rules of its own slot, as since upgrade 1: stamped before the slot it
carries a VRF proof, at or after it the nonce alone. Two transits are compared by work value only
when they share a slot (section 5), so a VRF value and a hash value are never compared.

Miner version 6, already set by upgrade 6, covers this too: a miner of an earlier release, which can
only produce VRF proofs, stops at the slot.

## 4. Which hash

The user named SHA-256, BLAKE2b and Keccak. Measured per attempt, one core of an i7-1165G7 (Go 1.26,
77-byte message; a throwaway benchmark, not in the repo):

| Work function | ns per attempt | attempts vs VRF | Builtin today |
|---------------|---------------:|----------------:|---------------|
| ECVRF `Output` (today) | 53,600 | 1x | yes |
| SHA-256 (CPU has SHA-NI) | 111 | ~480x | no |
| BLAKE2b-256 | 145 | ~370x | **yes** |
| Keccak-256 / SHA3-256 | 365 | ~150x | no |

None of them keeps a CPU competitive with a GPU; that was true of the VRF too. What differs is the
gap to custom silicon and what the ledger must add.

- **BLAKE2b-256 (chosen).** Already a builtin and already in the validation path, so the change is
  EasyFL only. Fast in software. An add-rotate-xor design: adders cost area in silicon, so it gains
  less from an ASIC than the other two. Hardware for it exists (Sia's mining chips hash an 80-byte
  header with the nonce at a fixed place); our 77-byte layout does not match it, and whether such
  chips could be repurposed is not verified. The layout should never be shaped like another coin's
  header.
- **SHA-256.** Fastest on CPUs with SHA-NI, about twice as fast again with a precomputed first block
  (77 bytes is two SHA-256 blocks, the first fixed). Carries the largest custom-silicon industry
  there is. Bitcoin miners do double SHA-256 over an 80-byte header and cannot run this message as
  is, but SHA-256 cores are the most mature hardware building block in existence. Needs a new
  builtin.
- **Keccak-256.** Slowest in software and, by its designers' own account, cheap in hardware: mostly
  XOR, AND and rotations, no adders. It widens the CPU-to-silicon gap most. GPU kernels are
  widespread from Ethereum tooling. Needs a new builtin and a choice between legacy Keccak-256
  (Ethereum's padding, only in `golang.org/x/crypto/sha3`) and standard SHA3-256 (Go's
  `crypto/sha3`), which give different outputs.

A new builtin is possible (upgrade 1 added embedded resolvers), but it puts new Go code in every
node's validation path and must ship in the release before the slot. BLAKE2b avoids that.

## 5. Consequences in the code

Everything upgrade 6 built stays (`c1eb2d93`): the payout rules, both guarantor forms, the miner's
strict top-ups and plain-only placement, the sequencer's refusal of a strict request naming a
delegation with extras, and their tests. What follows is only the work function.

The PoW value moves from "VRF output decoded from the proof" to "BLAKE2b of the message, computed
directly". Every site reading it must take the rule of the transit's slot.

| Where | Today | Change |
|-------|-------|--------|
| `ledger/def/upgrade6.easyfl` | payout rules | add the three functions of section 3 |
| `ledger/txbuildercore/helpers_mine.go` | `MineVRFMessage`, `MineUnlockParams(proof, nonce)`, `MineUnlockParamsLen = 88` | add `MineWorkMessage(pred, slot, pubKey, nonce)` and the 8-byte layout; the VRF ones stay until the slot has passed |
| `ledger/transaction/tx.go` | `MineVRFOutput`, `MineProofOfWork64` decode the proof | one accessor `MineWorkValue()` choosing by the transaction's slot (a `HasUpgrade6` flag on the library, as `HasUpgrade4`): the BLAKE2b value, or the VRF output before the slot. Stage-1 safe as now: the public key is in the signature data |
| `core/core_modules/txinput_queue` | floor gate and duplicate-solution key read the decoded VRF output | read `MineWorkValue`. The floor gate gets **stronger**: it computes the exact value the covenant tests, instead of decoding an unverified proof. Core package: run `-race` |
| `sequencer/mine/transit.go`, `registry.go`, `deviation.go` | `VRFOutput`, winner rule "smaller VRF output" | renamed to the work value; rule unchanged (older slot, then smaller work value, then txid) |
| `proxi/node_cmd/mine/mine_tree.go` | the same tie-break on `vrfOutput` | the same rename |
| `proxi/node_cmd/mine/mine.go`, `mine_upgrade.go` | `vrf.Prover` hot loop, proof completed for the winner | from the slot: one BLAKE2b per attempt over a fixed 69-byte prefix and the nonce, no proof step; the VRF loop kept for slots before, chosen by the per-slot switch that already picks the top-up form |
| `proxi/node_cmd/mine/mine_verify.go`, `api/monitor/monitor.go` | `vrf.Verify` of a foreign transit | recompute the hash, by slot |
| `proxi/node_cmd/mine/mine_seeker*.go`, `nonce_seeker/` (Rust) | the seeker holds the key and computes the VRF output | the job carries the prefix and K, the seeker hashes. **No key, no keystore, no passphrase**: section 5 of `kb/external_nonce_seeker.md` goes away. Protocol version bumped; the Rust seeker drops `proxima-vrf` and the keystore code |
| `util/vrf` | prover, split prover, verifier | **stays**: the branch inflation bonus depends on it. The split prover (`Prover.Output`, `ProofFor`) and `util/vrf/rust` serve only mining and can be deleted in a release after the slot |

Size: a transit's unlock parameters go from 88 to 8 bytes, about 700 to 620 bytes per transit.
Verification: one BLAKE2b, well under a microsecond, instead of a VRF verify.

No change to `core/memdag`, `core/attacher`, `core/vertex`, sync, or the coverage rule; the DAG and
sync constraints (`kb/dag_semantics.md`, `kb/sync_semantics.md`) are untouched.

## 6. Consequences in behaviour

### 6.1 Difficulty at the switch

On 2026-10-13 the network ran about 8.5 MH/s of VRF attempts with B near 26. The same hardware does
150 to 500 times more BLAKE2b attempts on CPU, and GPUs gain more than CPUs (a VRF attempt is heavy
elliptic-curve arithmetic, a hash is light integer work). Expected jump: **about 9 bits for a CPU
fleet, likely 12 or more with GPUs.** Not measured.

The retarget hardens one bit per 8 full slots (`constMineHardenAfter`), so 12 bits take at least 96
slots, about 16 minutes. Meanwhile:

- **Emission is unchanged**: one transit per slot whatever the difficulty.
- **Every slot is full and contested.** The winner is the smallest work value the sequencers hold at
  the settlement tick, a draw weighted by hashrate (`kb/mine_conflict_rule.md`).
- **Load stays small.** A miner submits each improvement on the best solution it knows, so a slot
  sees a few extra transits network-wide (records in a run of draws grow with its logarithm), not one
  per solution.

So **no reseed rule is proposed.** If measurement before the slot shows a gap that would take hours
to close, the upgrade can add one: the successor of an output built by a miner before version 6
carries a fixed seed B. Seed high rather than low: an overshoot costs one empty slot per surplus bit,
an undershoot 8 full slots per missing bit.

The ceiling `constMineMaxDifficulty` (56) holds: 56 bits per 10.24-second slot is about 7 PH/s.

### 6.2 Who mines

- **CPU miners lose ground to GPUs** by a wider margin than under the VRF, and custom silicon would
  widen it further if the remaining emission ever pays for building it. The site's fair-launch and
  mining pages must say so plainly.
- **Hashrate can be rented.** Renting buys a share of emission for a while, nothing more: the mine
  chain takes no part in consensus, so rented hashrate cannot reorder the ledger. Liveness is
  unaffected when it leaves: K falls to the floor with the gap.
- **Weak keys are pointless**: the key is not in the attempt.

### 6.3 Pools

**The work becomes outsourceable and pools become possible.** Any proof of work the ledger checks
without the key can be done without the key. A pool publishes (predecessor, slot, its public key),
workers search and send it shares at an easier target, and the pool pays them off-chain or by
separate transactions. A worker who meets the real target cannot redirect the reward: the message
names the pool's key. The seeker protocol of `kb/external_nonce_seeker.md`, once it stops needing the
key, is a pool protocol in all but name.

Why it matters here more than on a proof-of-work chain: mined tokens are placed in consensus. Under
upgrade 6 almost every reward ends in a delegation whose master is the signer, and the mine chain's
total emission (`constMineRemainingInit`, 940 M PROX at genesis, part of it mined since) is many
times the genesis supply. A pool is the master of every delegation its rewards open or top up, so it
chooses which sequencers that capital backs and can move it in each revocation window. A worker can
ask the pool to delegate to a sequencer of its choice, and a pool may offer that, but the ledger
cannot enforce it; the worker's recourse is to leave the pool. Under the VRF a pool had to share its
key with its workers, which nobody does.

Paying many workers from one transit, as Bitcoin's P2Pool did, would remove the custodian, but it
needs a payout layout with many outputs, against upgrade 6's one reward output; not proposed.

Keeping non-outsourceability means keeping the key in the loop: a signature inside the hash brings
back the free signature nonce that sank the first work function (`kb/mine_vrf_pow.md` 1), and a
deterministic signature is exactly what the VRF is.

### 6.4 Things that do not change

Upgrade 6's payouts and guarantor, the payout amounts, the emission schedule, the pace, the retarget,
the settlement window and tick 83, the canonical winner rule, speculative mining on the miner's tree,
and the miner's choice of payout option.

## 7. Activation

- Miner version 6: a miner of an earlier release stops at the slot.
- The miner keeps both work functions until the slot, choosing by the slot it stamps, as it already
  does for the top-up form.
- Seekers: a VRF seeker cannot serve a hash job. The seeker protocol version is bumped and the miner
  refuses a seeker that does not speak it, so an old seeker fails loudly at the slot.
- No reset, no genesis change.

## 8. Tests

`ledger/tests/library_upgrade6_test.go`: the upgrade 6 tests stay (`TestUpgrade6Library`,
`TestUpgrade6MinePayouts`, `TestUpgrade6TopUpForms`, `TestUpgrade6FakeDelegation`), their transits
built with the work function of their slot. Added:

- a VRF transit valid before the slot, refused from it; a hash transit refused before, valid from it;
- from the slot: a nonce copied into a transit signed by another key refused; wrong unlock length
  (0, 7, 9, 88) refused; too few zero bits refused; a nonce replayed on the next transit or on
  another slot refused;
- `MineWorkValue` equals what the covenant tests on both sides of the slot;
- once 2.1 is decided: the reclaim of an unserved strict request, under the chosen rule.

`core/core_modules/txinput_queue`: floor gate and duplicate key on a hash transit (`-race`).
`sequencer/mine`: the winner rule on work values. `proxi/node_cmd/mine`: hot loop and `mine_verify`
across the slot, seeker job format.

## 9. Open

- **The reclaim escape** (2.1): which closure, specified and tested before the slot is chosen.
- **Pools** (6.3): accepted with the hash, or the VRF stays and weak keys stay an open problem.
- **Measure before choosing the slot**: the BLAKE2b rate of the current fleet (a benchmark build for
  miners), to decide whether a reseed is worth adding.
- **Documents**: `kb/library_upgrade6.md` to point here for the work function, and its table "Why it
  closes the gap" to correct the reclaim window (the sender alone from 30 slots, anyone from 390);
  `kb/mine_vrf_pow.md` to `kb/archive/superseded/` once the slot has passed;
  `kb/external_nonce_seeker.md` rewritten; `core/resilience.md` (the mine-copy and VRF-verify
  lines); `ARCHITECTURE.md`; the docs site's mining and fair-launch pages; the Rust seeker README; the
  CLAUDE.md `kb/` index (needs the user's confirmation).
