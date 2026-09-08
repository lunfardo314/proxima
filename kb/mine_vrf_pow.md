# VRF-bound proof of work for the mine chain

> **LIVE** — spec, **implemented on `develop` 2026-09-08** (hardfork of
> `mineLock`; testnet regenesis pending). What shipped: the covenant change in
> `ledger/def/lock_mine.easyfl`, the message and unlock-parameter helpers in
> `ledger/txbuildercore/helpers_mine.go`, the split prover in `util/vrf`, the
> miner and its verifier in `proxi/node_cmd/mine*.go`, ledger and miner tests,
> and the site pages. Section 6 is the risk assessment of the in-house VRF,
> with the review outcome; it applies to the branch inflation bonus as much as
> to this change. Kept in the working set until the regenesis confirms the
> first transits; then it belongs in `kb/archive/shipped/`.

## 1. Problem

The current work function is `blake2b(txBytes)` with at least K trailing zero
bits, where `txBytes` includes the ed25519 signature. The design intent was
"every attempt costs one signature under the miner's key", which was meant to
make the work key-bound and to keep the per-attempt cost at a scalar
multiplication.

The second half does not hold. An ed25519 signature is (R, S) with R = r·B and
S = r + H(R, A, M)·x. The verifier cannot see how r was chosen, so a miner may
fix the message, pick a random r, and step R by one point addition per attempt
(R, R+B, R+2B, ...), sharing the field inversion of the point compression
across a batch. Measured on an i7-1165G7, one core, Go 1.26:

| per attempt | µs |
|---|---|
| honest `ed25519.Sign` + two blake2b (today's `proxi node mine`) | 21.6 |
| related-nonce shortcut, batched compression | 1.3 |
| VRF-bound, minimal honest work (hash-to-curve + Gamma) | 53.7 |
| VRF-bound, full proof (needed for the winner only) | 131 |

Two consequences of the shortcut. It is a 16x edge available to whoever knows
it, on any hardware. And it is a key-leak footgun: two published signatures
whose nonces differ by a known or small value reveal the private key, and the
key is the miner's wallet key. Making the shortcut safe requires a nonce
construction of our own (hedged start, secret step, one publish per sequence).
That is bespoke cryptography in every miner's key path, which is the reason it
was rejected in favour of this spec.

What this change buys: the work function has no free variable. RFC 9381 gives
*full uniqueness*: for a given public key and message there is exactly one
output that verifies, for adversarial keys too. Every attempt therefore costs
one hash-to-curve plus one variable-base scalar multiplication, with nothing to
increment or batch, and the transaction signature goes back to being ordinary
deterministic signing outside the hot loop.

What it does not buy: hardware equality. A GPU runs variable-base scalar
multiplication in parallel, roughly an order of magnitude per card over an
8-core CPU. The change ends the hidden software edge and raises the bar for
FPGA and ASIC (a square root and a full variable-base multiplication per
attempt instead of a point addition and a hash). It does not make CPUs
competitive with GPUs, and the docs must not claim it does.

## 2. Design

### 2.1 Message, proof, placement

The consumed mine output's unlock parameters at the lock element, which today
carry the 8-byte nonce that `mineLock` ignores, become a fixed 88-byte layout:

```
[0:80)   pi     ECVRF-EDWARDS25519-SHA512-TAI proof: Gamma (32) || c (16) || s (32)
[80:88)  nonce  8 bytes, free
```

Why unlock parameters and not a `mineLock` argument on the successor, as the
stem does with its proof: the stem's proof must live in the state because the
next branch's VRF message is built from it and `chain.easyfl` reads it for the
inflation bonus. The mine proof is consumed once, by the predecessor's consumed
arm validating this transit, and nothing reads it afterwards. Keeping it in
the transaction leaves `mineLock`'s arguments as the mutable state (R, B) and
touches neither genesis nor the Go parser. The alternative is sound too, at
the cost of 80 bytes in the single live mine UTXO and an empty-proof rule for
genesis; it was not taken for churn reasons only.

The VRF message is

```
alpha = predecessorOutputID (33 bytes) || txSlot (4 bytes, as EasyFL returns it) || nonce (8 bytes)
```

The VRF key is the transaction signer's key, `signaturePublicKey(txSignatureData)`,
which `_minePayoutAndFee` already pins as the payout holder. Proof key, signing
key and payout key are therefore the same key.

The work condition is unchanged in form: the low 64 bits of the VRF output
`beta` (64 bytes; take the last 8) must end in at least K zero bits, with
K = `_mineRequiredK(B, M)` exactly as today.

### 2.2 Covenant change

Only `_minePaceAndPoW` and its helpers change. Sketch:

```
// consumed mine output's unlock params: ECVRF proof (80) || nonce (8)
func _mineProof  : slice(selfUnlockParameters, 0, 79)
func _mineNonce  : slice(selfUnlockParameters, 80, 87)
func _mineAlpha  : concat(inputIDByIndex(selfOutputIndex), txSlot, _mineNonce)
func _mineBeta64 : tail(vrfProofToHash(_mineProof), 56)

func _minePaceAndPoW :
and(
   require(lessOrEqualThan(uint8Bytes(constMineMinPace), uint8Bytes(_mineM)), !!!mine_pace_below_minimum),
   require(equalUint(len(selfUnlockParameters), u64/88), !!!mine_unlock_params_must_be_VRF_proof_and_nonce),
   require(vrfVerify(signaturePublicKey(txSignatureData), _mineAlpha, _mineProof), !!!mine_VRF_proof_check_failed),
   require(_minePoWOK(_mineBeta64, _mineRequiredK($0, _mineM)), !!!insufficient_mine_proof_of_work)
)
```

`_mineTxHash64` is deleted. `_minePoWOK` stays. Everything else in
`lock_mine.easyfl` (shape, successor state, retarget, payout and fee, pace) is
untouched. The library hash changes, so this is a hardfork; the testnet is
regenerated.

The two builtins already exist and are already validation-critical on the stem:
`vrfVerify` (RFC 9381 verify, returns 0xFF or empty) and `vrfProofToHash`
(decode only, cofactor-cleared). `vrfVerify` returns empty on any decoding
failure, including the astronomically unlikely hash-to-curve failure, so a
malformed proof fails the `require` rather than panicking.

### 2.3 Properties

- **No free variable.** `beta` is a deterministic function of (key, alpha).
  The miner's only levers are the nonce and the key. Trying another key is not
  cheaper than trying another nonce, and the key must also sign and receive the
  payout.
- **Bound to the transit.** `alpha` contains the predecessor output ID, so no
  work can start before the predecessor exists and no solution can be
  stockpiled for a future transit. Speculative mining on the tree
  (`mine.go`) is unaffected: the predecessor is the miner's own unconfirmed
  successor, whose ID is known.
- **Bound to the slot.** `alpha` contains `txSlot`, so an attempt is an attempt
  for one target slot, as today. This keeps the pace-relieved difficulty model
  and its measurements valid unchanged (see 2.4 for the rejected alternative).
- **Non-outsourceable.** Gamma = x·H needs the secret scalar. A pool would have
  to hold the key, and with it the payout. Same property as today, on a firmer
  footing.
- **Liveness.** Unchanged: K falls to E with the gap, whatever the hashrate.
- **Deterministic signing returns.** The signature is computed once, for the
  winning transaction, with the standard library. Nothing about the key path is
  new.

### 2.4 Rejected alternatives

- **Slot-free message** (`alpha` without `txSlot`): a miner would search once
  and stamp the earliest slot whose K its best `beta` meets, which removes
  re-stamping waste. Sound, but it changes the meaning of an attempt and would
  invalidate the pace model that was tuned on the testnet. Can be revisited
  with measurements; not now.
- **Related-nonce signatures with a hedged start and secret step**: 16x faster
  than today for honest miners, but a construction of our own in the wallet key
  path. Rejected for that reason alone.
- **Memory-hard work function** (Argon2id-class): the only thing that softens
  GPUs, and only by a few times. Needs a new builtin, tens of milliseconds of
  verification per mine transaction and a front gate against conflicting
  invalid mine transactions. Disproportionate for a bounded emission; keep as
  a documented option with a measurement trigger.

## 3. Miner

### 3.1 Hot loop

Per attempt the miner touches no transaction bytes at all:

```
alpha = predID || slot || nonce
H     = encode_to_curve_TAI(pk, alpha)        // ~2 SHA-512 + point decode on average
Gamma = x · H                                 // variable-base, constant-time
beta  = SHA-512(suite || 0x03 || (8·Gamma) || 0x00)
if trailingZeros(beta[56:64]) >= K: winner
```

For the winner only: k = nonce_generation(prefix, H), c = challenge(Y, H,
Gamma, k·B, k·H), s = k + c·x, pi = Gamma || c || s; then
`PutUnlockParams(predIdx, lockIndex, pi || nonce)`, sign, submit. The byte
template machinery in `mine.go` (`mineTemplate`, `mineWorker`,
`verifyMineTemplate`, `buildEssence`) is deleted; the winner is built with the
ordinary `TxBuilder`. Workers keep disjoint nonce spaces and the shared attempt
counter; `mineParallel` keeps its shape.

### 3.2 Prover API in `util/vrf`

`Prove` stays as it is. Add a split prover so the loop does the minimum work:

```go
type Prover struct{ /* x, prefix, pk, cached from the seed */ }
func NewProver(sk ed25519.PrivateKey) (*Prover, error)
// Output does hash-to-curve and Gamma; returns beta and what ProofFor needs.
func (p *Prover) Output(alpha []byte) (beta []byte, st *proofState, err error)
// ProofFor completes the RFC 9381 proof for a state returned by Output.
func (p *Prover) ProofFor(st *proofState) []byte
```

The split must not reorder or alter any RFC step. Test: for every RFC vector
and for random inputs, `ProofFor(Output(alpha))` equals `Prove(sk, alpha)`
byte for byte and its `beta` equals `Verify`'s.

### 3.3 Expected effect on the chain

Honest CPU attempts cost about 2.5x more than today, so for the same fleet the
equilibrium B settles about 1.3 bits lower. `constMineBaseDifficulty` needs no
change: it is a seed and the retarget adapts within a few transits. No other
constant changes.

## 4. Verification cost and resilience

A VRF verify is two double-scalar multiplications plus a hash-to-curve, three
to four times an ed25519 verify. Honest load is one mine transaction per few
slots and is negligible. The only new load is conflicting spends of the mine
output that fail the VRF check; they already pay a signature verify each, so
this is a small multiplier on one transaction type behind the existing dedup
and pace gates. Add one line to `core/resilience.md` under the transaction
path gates. No new gate.

The existing ingress floor gate in `core/core_modules/txinput_queue`
(`mineProofOfWorkMeetsFloor`) sheds unsolicited mining-shaped transactions
whose work is below the floor difficulty E, before persist and gossip. It used
to hash the bytes; it now reads the same value the covenant tests, through
`Transaction.MineProofOfWork64`: the proof is decoded with `ProofToHash`, not
verified, a few microseconds and no ledger state. Its strength is unchanged:
forging a value with E trailing zero bits costs 2^E hashes either way, and
only a genuine proof under the payee key passes the covenant.

Transaction size: the unlock parameters of the consumed mine output grow from 8 to 88 bytes, about 620 to
700 bytes per mine transaction.

## 5. Work plan

1. `ledger/def/lock_mine.easyfl`: section 2.2. Update the header comment
   (the "proof of (signing) work" wording) and the `_minePaceAndPoW` comment.
2. `util/vrf`: section 3.2, with the equality tests.
3. `ledger/tests/mine_test.go`: `buildMineTransit` searches the nonce with
   `Prover.Output`, tiny test difficulty as now. Negative tests: proof under a
   key other than the signer; proof for a different nonce, slot or
   predecessor; wrong unlock-parameter length; insufficient zero bits; a valid
   proof from the previous transit replayed on the next one.
4. `proxi/node_cmd/mine.go`: section 3.1; banner text.
   `proxi/node_cmd/mine_verify.go`: replace the blake2b check with
   `vrf.Verify(signerPK, alpha, pi)` and the zero-bit test on `beta`.
5. `ledger/txbuildercore/helpers_mine.go`: no constant changes; add a helper
   that builds `alpha` so the miner, the verifier and the tests share one
   encoding.
6. Docs: site `participate/mine.md` ("Proof of signing work" section) and
   `overview/fair_launch.md` (the work-function paragraph); both were reworded
   on 2026-09-08 to drop the CPU-egalitarian claim and must now describe the
   VRF. `ARCHITECTURE.md` if it mentions the signed-transaction hash. Whether
   the name "proof-of-signing-work" survives is the user's call; "key-bound
   proof of work" is accurate.
7. Testnet regenesis; confirm the first transits and that B settles.

Run `go test ./ledger/...` for the covenant work; the miner is not core, so no
race run is required for it, but `mineParallel` should still be run once under
`-race` since its worker structure changes.

## 6. Risk assessment of the in-house VRF

`util/vrf/vrf.go` is an in-house Go implementation of
ECVRF-EDWARDS25519-SHA512-TAI (RFC 9381) on `filippo.io/edwards25519`. It is
already validation-critical: every branch carries a proof, the stem verifies
it, and the branch inflation bonus is derived from its output. This change adds
a second consumer of the same three properties. It adds no new class of
failure. The exposure ranking below is the user's reading too: the branch bonus
is the larger risk, because sequencer keys prove thousands of times a day and
the sequencer chooses what to publish.

| Property | If it failed | Branch bonus | Mine chain |
|---|---|---|---|
| **Unforgeability** (a proof verifies under a wrong key or message) | Free output of the attacker's choosing | Sequencer picks the maximum bonus every slot: stolen inflation | Free mining: every transit at zero work |
| **Uniqueness** (more than one `beta` per key and message) | Bounded resampling (x8 at most for cofactor issues) | Slight bonus bias | Slight difficulty discount |
| **Prover key safety** (nonce k predictable or reused) | Private key recovered from published proofs | Sequencer key, proofs published every slot: catastrophic | Miner wallet key: catastrophic for that miner |

Evidence already in the tree:

- `TestRFC9381Vectors` reproduces the Appendix B.4 vectors for both the proof
  bytes and `beta`. Matching `pi` byte for byte is strong evidence that
  hash-to-curve, nonce generation, challenge generation and the scalar
  arithmetic all follow the RFC on the honest path, since any deviation in k
  or c changes `pi`.
- `proofToHash` multiplies Gamma by the cofactor, which is what gives full
  uniqueness without a separate key-validation step.
- Nonce generation is `SHA-512(prefix || H)` reduced mod q, deterministic and
  message-bound, the ed25519 construction; `ScalarMult` on the secret is
  constant-time in `filippo.io/edwards25519`. Hash-to-curve is variable-time
  but runs on public inputs only.

Review of 2026-09-08, line by line against RFC 9381 sections 5.1 to 5.4:

- **Prove, Verify, encode_to_curve, nonce generation, challenge generation,
  proof_to_hash and decode_proof follow the RFC step for step**, including the
  order of the five challenge points, little-endian scalar encodings, the
  16-byte challenge, cofactor clearing in hash-to-curve and in proof_to_hash,
  and the nonce as SHA-512 of the secret prefix and H reduced mod q. The
  secret scalar goes only through constant-time `ScalarMult` and
  `MultiplyAdd`; hash-to-curve is variable-time on public inputs only.
- **One gap found and closed: `ECVRF_validate_key` (RFC 5.6.1) was missing.**
  A small-order public key, the identity or the order-2 point (0, −1), has no
  secret scalar, yet a forged proof under it (Gamma = identity, s = k)
  verified and produced one constant output for every message. For the mine
  chain that constant has one trailing zero bit and could never meet the
  floor difficulty; for the branch bonus it is a fixed value with nothing to
  grind. So no exploit, but the RFC check is now in `Verify`. This changes
  validation for keys no honest party can hold; it ships with the hardfork.
- **Tests added** (`util/vrf/vrf_test.go`): the third RFC vector
  (example18); the RFC intermediate values H and k checked for all three
  vectors, so hash-to-curve (with the ctr = 1 retry of example17) and nonce
  derivation are pinned individually; decode strictness for empty, truncated,
  extended, non-point Gamma and non-canonical s (s + q), through both `Verify`
  and `ProofToHash`; wrong-length and non-point public keys; message prefix,
  suffix and empty variants; cross-key proofs; the forged small-order-key
  proof for both small-order encodings; a 100-round random round-trip with
  distinctness across messages and keys.
- **Proof-byte malleability**: none for honest proofs. Gamma is re-encoded
  canonically before hashing, and an alternative encoding of a prime-order
  point does not exist (only y < 19 or x = 0 have one). s + q is rejected.
- **Determinism**: pure Go, no `unsafe`, no floats; dependencies are the
  standard library and `filippo.io/edwards25519` only.

Still open, both optional: a differential check against a second final-RFC
implementation on random inputs (draft-03 suites used by some chains are not
comparable), and a second pair of eyes on the same 300 lines. The split
prover of 3.2 must keep the property that k is derived from the final H.

Side finding outside this package: Go's `ed25519.Verify` accepts the identity
public key with a trivially forged signature, so a sigLock to that holder is
spendable by anyone. Nobody is harmed unless they choose that lock; noted, not
acted on.

## 7. Out of scope

- GPU parity. Not achievable with any signature- or VRF-based work function.
- Changes to the emission schedule, retarget, pace or payout rules.
- A memory-hard work function (2.4).
