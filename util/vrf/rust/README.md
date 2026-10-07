# proxima-vrf

ECVRF-EDWARDS25519-SHA512-TAI (RFC 9381) in Rust, the twin of the Go package
`util/vrf` one directory up. Same functions, same byte formats, same
acceptance rules: `Prover` (`from_seed`, `new`, `output`, `proof_for`,
`prove`), `prove`, `verify`, `proof_to_hash`.

Equivalence with the Go package is checked from the Go side:
`go test ./util/vrf/` builds the `vrf_tool` binary of this crate with cargo
and compares proofs and outputs over the RFC vectors and random inputs. The
test is skipped when cargo is not installed.

The crate itself: `cargo test`. Used by the reference nonce seeker in
`proxi/node_cmd/mine/nonce_seeker`.
