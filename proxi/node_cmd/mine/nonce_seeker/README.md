# nonce_seeker

Reference external nonce seeker for `proxi node mine`, in Rust. Protocol and
rationale: `kb/external_nonce_seeker.md` in the repository root.

The seeker holds the wallet key and searches nonces; the miner verifies each
returned nonce, completes the VRF proof and submits the transaction. On the
CPU this loop runs about twice as many attempts per core as the miner's own
Go workers. It is built on the Rust twins of two Go packages, `util/vrf/rust`
(the VRF) and `util/keystore/rust` (the key file), which the Go test suites of
those packages keep equivalent to the Go code.

## Build

Needs a Rust toolchain (https://rustup.rs). In this directory:

```
cargo build --release
```

The binary is `target/release/nonce_seeker`. Adding
`RUSTFLAGS="-C target-cpu=native"` lets the field arithmetic use the vector
instructions of the machine it is built on, for a few percent more.

## Run

1. In the miner's wallet profile (`proxi.yaml`) set `mine.seeker.listen`
   (and `mine.seeker.token` unless the listener is loopback), then start
   `proxi node mine`. Add `--workers 0` to leave the whole search to seekers.
2. Start the seeker with the same key file:

```
nonce_seeker --proxi http://127.0.0.1:8100 [--token T] [--key-file proxima.key] [--threads N] [--name NAME]
```

| flag | default | meaning |
|---|---|---|
| `--proxi` | required | address the miner serves jobs on |
| `--token` | none | the profile's `mine.seeker.token` |
| `--key-file` | `proxima.key` | the wallet keystore, encrypted or not |
| `--threads` | all cores | search threads |
| `--name` | `seeker-<pid>` | shown in the miner's totals line |

An encrypted key file is unlocked the way `proxi` unlocks it: a passphrase
file in the working directory named after the keystore's `holder_id`, else
the environment variable `PROXIMA_KEY_PASSPHRASE`, else a prompt. Without a
key file the environment variable `NONCE_SEEKER_SEED` (32 bytes hex) is
accepted.

The seeker runs until killed. A miner that is down is retried with backoff;
a job whose key is not the seeker's is refused and logged.

## Tests

```
cargo test
```

covers the seeker's own logic; the VRF and the key file are tested in their
crates and against Go from `go test ./util/vrf/ ./util/keystore/`.
