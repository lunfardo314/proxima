# External nonce seekers for `proxi node mine`

> **LIVE** — spec, written 2026-10-07, implementation pending. Binds
> `proxi/node_cmd/mine/` and the reference seeker in
> `proxi/node_cmd/mine/nonce_seeker/`. The protocol in section 3 is the
> contract for anyone writing a seeker; the rest explains why it is shaped
> that way.

## 1. Purpose

`proxi node mine` searches for a nonce whose VRF output under the wallet key
ends in enough zero bits. Today the search runs in the same process, in Go,
one goroutine per core. That loop is the whole cost of mining, and it is the
one part that a faster implementation can replace: hand-written field
arithmetic, a Rust binary, a GPU kernel.

This document makes the search pluggable. With one option in the wallet
profile the miner becomes a small HTTP server that hands out *jobs* and
accepts *solutions* from any number of external *nonce seekers*. Everything
else the miner does stays where it is: following the mine chain, choosing the
target slot, building and signing the transaction, submitting it, grinding a
contested slot. A seeker knows nothing about Proxima beyond one message
layout and one hash function.

Without the option the miner behaves exactly as before.

## 2. Division of work

| | `proxi node mine` | nonce seeker |
|---|---|---|
| follows the mine chain, picks the target | yes | no |
| decides K, the slot, the deadline | yes | no |
| holds the private key | yes | yes, same key file and passphrase procedure (section 5) |
| searches nonces | its local workers, optional | yes |
| verifies a solution | yes, always | no |
| completes the VRF proof | yes | never |
| builds, signs, submits the transaction | yes | no |
| measures the hashrate | from the attempt counts it is told | reports its attempts |

The seeker is deliberately dumb. Two reasons:

- The chain logic is consensus-adjacent. The miner's tree, the settlement
  deadline, the canonical winner rule and the transaction shape are mirrored
  from the ledger constraints and change with them. A seeker that
  reimplemented them would drift.
- A dumb seeker is portable. The same protocol serves a Rust binary on a
  CPU, a CUDA kernel on a rented GPU, or a cluster of either. The search is
  the only part worth porting.

A seeker never produces a proof. It returns the nonce; the miner recomputes
the VRF output for that nonce, checks it against the job, and only then
completes the RFC 9381 proof with the audited Go code in `util/vrf`. That
keeps one proof producer in the system and makes a buggy or hostile seeker
harmless: the worst it can do is waste the miner's time with solutions that
fail verification.

## 3. Protocol

JSON over HTTP/1.1. The seeker is the client and the miner is the server.
This direction is chosen because a seeker on a rented machine or inside a
container can usually dial out but not be reached, because either side can
restart without the other noticing more than a pause, and because every
language has an HTTP client.

Binary fields are lowercase hex strings. Integers are JSON numbers. Unknown
fields are ignored by both sides, so fields can be added later without
breaking existing seekers.

### 3.1 Authentication

If the profile sets `mine.seeker.token`, every request must carry

```
Authorization: Bearer <token>
```

and a missing or wrong token is answered with `401`. The token is not for
secrecy: jobs are public data (the predecessor output and the slot are on
the chain). It stops strangers from feeding the result endpoint, since each
posted result costs the miner one VRF evaluation. Without a token the
listener should be bound to the loopback address.

### 3.2 The job

```
GET /seeker/job?after=<id>&wait=<ms>
```

Returns the current job. If `after` equals the current job id, the request
blocks until the job changes or `wait` milliseconds pass (the server caps the
wait, at 10 seconds), then returns the current job either way. Omitting
`after` returns at once. This is the long-poll that lets a seeker react to a
new target within a round trip without hammering the server.

```json
{
  "id": 1759824000123,
  "alpha_prefix": "<hex, 37 bytes>",
  "k": 31,
  "beat": "<hex, 64 bytes>",
  "ttl_ms": 9800,
  "pubkey": "<hex, 32 bytes>",
  "pred": "<hex, 33 bytes>",
  "slot": 123456
}
```

| field | meaning |
|---|---|
| `id` | identifies the job. Opaque to the seeker: compare for equality, nothing else. `0` means there is no job (the miner is waiting for a tip, for the mine chain to open, or between rounds); all other fields are then absent and the seeker idles. |
| `alpha_prefix` | the fixed part of the VRF message. The message for a nonce is `alpha_prefix || nonce`. |
| `k` | required number of trailing zero bits of the VRF output. |
| `beat` | optional. When present the output must also be lexicographically smaller than this 64-byte value. Set while the miner grinds a contested slot for a better solution. |
| `ttl_ms` | how long the job is worth working on, from the moment of the response. After that the slot is settled and no solution reaches a sequencer in time; the seeker stops and polls for the next job. Relative, so clock skew between the machines does not matter. |
| `pubkey` | the Ed25519 public key the solution must be found under. A seeker holding a different key must refuse the job and say so in its log. |
| `pred`, `slot` | informational: the predecessor mine output and the target slot, for the seeker's log. `alpha_prefix` is their encoding, so a seeker never needs to assemble it. |

A new job replaces the previous one; there is at most one job at a time. A
job also ends when a solution is accepted, when the round deadline passes, or
when the target is superseded by a competing transit; the server then either
publishes the next job or `id: 0`.

### 3.3 The work

For a nonce `n` of 8 bytes, chosen freely by the seeker:

```
alpha = alpha_prefix || n
H     = ECVRF_encode_to_curve_try_and_increment(pubkey, alpha)
Gamma = x * H                      // x: the secret scalar of the key
beta  = SHA-512(0x03 || 0x03 || point_to_string(8 * Gamma) || 0x00)
```

This is `ECVRF_prove` of RFC 9381 for the suite
ECVRF-EDWARDS25519-SHA512-TAI (suite string `0x03`), stopped after Gamma,
followed by `ECVRF_proof_to_hash`. Precisely:

- `encode_to_curve`: for `ctr = 0, 1, 2, ...` hash
  `SHA-512(0x03 || 0x01 || pubkey || alpha || ctr || 0x00)`, take the first 32
  bytes as an Ed25519 point encoding; if it decodes, multiply by the cofactor
  8; if the result is not the identity, that is H. About half the counters
  decode, so the expected cost is two hashes and one decode.
- `x` is the clamped lower half of `SHA-512(seed)`, exactly as Ed25519 key
  expansion does it (RFC 8032). The upper half, the nonce prefix, is not
  needed: the seeker produces no proof.
- `point_to_string` is the 32-byte Ed25519 point encoding.

The nonce `n` solves the job when

- the number of trailing zero bits of `beta` is at least `k`. Trailing means
  counted from the least significant bit of the last byte, `beta[63]`,
  towards the front; and
- if `beat` is present, `beta < beat` comparing the 64 bytes as unsigned,
  first byte most significant.

Because the output is a deterministic function of (key, message), the same
nonce is the same attempt wherever it is tried. Seekers therefore pick a
random 64-bit starting nonce per job and each thread walks its own residue
class; several seekers and the miner's own workers then search disjoint
regions without talking to each other. There is no partition to hand out.

### 3.4 The solution

```
POST /seeker/result
{ "id": 1759824000123, "nonce": "<hex, 8 bytes>", "beta": "<hex, 64 bytes>",
  "attempts": 1234567, "seeker": "box-3" }
```

| field | meaning |
|---|---|
| `id` | the job this solves. |
| `nonce` | the 8 bytes that complete the message. |
| `beta` | the output the seeker computed. Optional, but strongly recommended: the server compares it with its own computation and the mismatch message is the fastest way to find a bug in a new seeker. |
| `attempts` | attempts made since the seeker's last report or result, for the hashrate (section 3.5). |
| `seeker` | a name for the log and the statistics. Free text, short. |

Responses:

| code | body | meaning |
|---|---|---|
| `200` | `{"accepted": true}` | verified and taken. The job ends; the seeker polls for the next one. |
| `409` | `{"error": "stale job"}` | the job id is not the current one, or the job is already solved. Not a fault, just late. |
| `422` | `{"error": "<reason>"}` | the nonce does not solve the job: too few zero bits, not below `beat`, or `beta` differs from the server's. A seeker bug. |

Verification on the server is the computation of section 3.3 under the
wallet key, nothing more. Only after it passes does the miner complete the
proof and build the transaction.

### 3.5 Attempts

```
POST /seeker/report
{ "id": 1759824000123, "attempts": 456789, "seeker": "box-3" }
```

Sent about once a second by every seeker, carrying the attempts made since
its previous report or result. Returns `200` with an empty object. `id` is
informational.

The miner needs these numbers. It sizes the mining window (how long a target
is searched before it is re-stamped to a later slot) and the stall timeout
from the measured hashrate, which is attempts divided by time. Seekers that
do not report make the miner believe it is slower than it is; the windows
then come out too short. The miner adds the reported attempts to its own and
shows each seeker's rate and last-seen time in its periodic totals line.

### 3.6 Failure behaviour

- **Miner restarts.** Job ids are drawn from a counter seeded with the
  start time, so a new miner never repeats an id a seeker may still hold. A
  seeker sees a connection error, retries with backoff, and receives the
  new job.
- **Seeker restarts or loses the network.** Nothing is lost on the miner's
  side; `ttl_ms` bounds the work a disconnected seeker can waste on a dead
  target.
- **Two seekers solve the same job.** The first accepted result wins; the
  second gets `409`. A local worker finding it first ends the job the same
  way.
- **Wrong key.** The job carries the public key; the seeker refuses the job
  before doing any work. The miner cannot tell a wrong-key seeker from an
  idle one, so the seeker must log it loudly.

## 4. Configuration

In the wallet profile, `proxi.yaml`:

```yaml
mine:
    seeker:
        # address the miner serves jobs on; empty means no seekers, the miner
        # mines exactly as it did before this option existed
        listen: 127.0.0.1:8100
        # shared secret seekers present as a bearer token; empty means none,
        # which is only reasonable on a loopback listener
        token:
```

With `listen` set:

- `proxi node mine` starts the server before the first round and prints the
  address in its banner.
- the local workers keep running next to the seekers; `--workers 0`, which
  is otherwise clamped to 1, turns them off and leaves the search entirely
  to the seekers. The miner then waits on each job until a result, the
  deadline or a supersession.
- `--max-hashrate-khs` caps the local workers only. Seekers pace
  themselves.
- `--nonce-start` applies to the local workers only.

Nothing else in the miner changes. Without `listen` no server is started,
no new code path runs, and the flags keep their current meaning.

`--workers 0` without any seeker configured is refused with a message, not
clamped to one worker as it was until 2026-10-09: a miner that was meant to
hand the search away must not quietly keep it on one core.

### 4.1 Spawning the reference seeker

A seeker on the same machine is only wiring to get wrong, as the first
attempt at it showed: the listener left out of the profile, the port and
token repeated by hand, the key passphrase needed by two processes, the
binary not on the right PATH. Since 2026-10-09 the miner does that wiring:

```yaml
mine:
    seeker:
        spawn: true      # same as 'proxi node mine --seeker'
        binary:          # empty: 'nonce_seeker' on PATH or beside proxi
        threads: 0       # 0: every core
```

With `spawn` (or `--seeker`) the miner listens on a free loopback port when
`listen` is empty, makes up a bearer token when `token` is empty, starts the
binary with `--proxi`, `--token`, `--key-file` and `--threads` filled in,
hands it the passphrase it unlocked the key with through the child's
`PROXIMA_KEY_PASSPHRASE`, logs the seeker's output under a `[seeker]` prefix,
restarts it with backoff when it exits, and kills it when the miner stops
(on Linux also when the miner is killed, by the parent-death signal). Local
workers default to zero; `--workers N` adds them. Remote seekers keep working
against the same server, with the token the banner shows. The protocol is
untouched: the spawned seeker is an ordinary client.

## 5. The key

Gamma needs the secret scalar, so a seeker holds the private key wherever it
runs. The protocol never carries it: the seeker loads it locally, derives the
public key, and refuses any job for another key.

The reference seeker reads the same keystore file `proxi` uses
(`wallet.key_file`, the JSON of `util/keystore`), encrypted or not, and
unlocks it the same way the miner does. The two therefore share one key file
and one passphrase procedure:

| keystore | how the key is read |
|---|---|
| unencrypted | `private_key`: the 64-byte Ed25519 private key in hex, seed first. |
| encrypted | `crypto`: Argon2id with the `kdf_params` from the file (time, memory, threads, salt) derives a 32-byte key; AES-256-GCM with `nonce` opens `ciphertext` into the same 64 bytes. |

The passphrase for an encrypted keystore is looked for in this order, the
order `proxi` uses, and the first source that yields one wins:

1. a file in the working directory named exactly as the keystore's
   `holder_id` (hex, no extension); its content, trimmed, is the passphrase;
2. the environment variable `PROXIMA_KEY_PASSPHRASE`;
3. a no-echo prompt on the terminal.

A seeker started as a service or in a container has no terminal, so it uses
the file or the variable; the prompt is for an operator at a shell. A wrong
passphrase is a fatal start-up error, reported as such, never retried.

The seeker also accepts a bare 32-byte seed in hex from the environment
variable `NONCE_SEEKER_SEED`, for images that carry no key file at all. The
key file takes precedence when both are present.

Encryption protects the key file at rest, on disk and in backups. It does
not protect the key from the machine it runs on: once unlocked, the seeker
holds the scalar in memory for as long as it mines, and the owner of a rented
machine can read that memory. The exposure is bounded: the key's only value
is the payouts it has received and not yet moved. Mine with a dedicated wallet
and run `proxi node consolidate` on it, which sweeps the payouts away as they
confirm.

## 6. Reference seeker

`proxi/node_cmd/mine/nonce_seeker/` is a Rust program implementing this
protocol on the CPU. It is built on the two Rust twins of the Go packages it
needs, `util/vrf/rust` (ECVRF, crate `proxima-vrf`) and `util/keystore/rust`
(the key file, crate `proxima-keystore`); each is kept byte-equivalent to its
Go package by a Go test that drives the crate's tool binary. The seeker is the
reference for anyone writing another seeker and a complete miner back end in
its own right. It runs about twice as many attempts per core as the Go loop.

```
nonce_seeker --proxi http://127.0.0.1:8100 [--token T] [--key-file proxima.key]
             [--threads N] [--name NAME]
```

- one poller thread holds the current job; `N` worker threads (default: all
  cores) each walk their own residue class from a random start, checking
  for a new job every few hundred attempts; one reporter thread posts the
  attempt counter once a second.
- the key comes from `--key-file` (default `proxima.key` in the working
  directory), encrypted or not, unlocked as section 5 describes, or from the
  environment variable `NONCE_SEEKER_SEED` (32 bytes hex).
- it exits only on a fatal configuration error (no key, unparsable address).
  Connection errors are retried with backoff forever, because a miner runs
  for days across restarts of everything around it.
- the attempt function is `Prover::output` of `proxima-vrf`, pinned to the
  RFC 9381 test vectors in that crate and to the Go package by the Go
  equivalence test.

Build with `cargo build --release` in that directory; the binary is
`target/release/nonce_seeker`. The directory is a Cargo project and carries
no Go files, so the Go build ignores it.

## 7. Writing another seeker

Everything a seeker must do is in section 3. The two checks that catch most
mistakes:

1. **The oracle.** For the same key and the same `alpha`, your `beta` must
   equal the one the miner computes. The RFC 9381 Appendix B.4 vectors for the
   TAI suite (seed, alpha, beta) are the first test; they are in
   `util/vrf/vrf_test.go` and in `util/vrf/rust`. The `beta`
   field in the result request is the second: the miner answers `422` with
   `beta mismatch` when yours differs.
2. **Trailing, not leading.** The zero bits are counted from the end of
   `beta`. A seeker counting from the front finds solutions that fail with
   `insufficient zero bits`.

A GPU seeker has one property worth knowing: the secret scalar is the same
for every attempt, so every thread executes the same double-and-add schedule
and there is no divergence to manage. The variable parts per attempt are
the hash-to-curve (one square root), the scalar multiplication on a different
point, and the compression (one inversion, which can be batched across
threads).

## 8. Out of scope

- **Pooling.** The output is bound to the key and the key signs the
  transaction and receives the payout. Seekers can share the key; they
  cannot share the work with someone who does not have it.
- **Proof generation in the seeker.** By design the seeker returns a nonce
  and nothing else is trusted.
- **Several keys per miner.** One `proxi node mine` serves one wallet. A
  second wallet is a second miner with its own listener.
- **Passphrase sources beyond the three of section 5.** Secret managers,
  agents and hardware tokens would be a change to `proxi` as a whole, not to
  the seeker.
