# proxima-keystore

The Proxima key file format in Rust, the twin of the Go package
`util/keystore` one directory up: a JSON file holding an Ed25519 key either in
clear or encrypted with Argon2id and AES-256-GCM, with the public key and the
holder ID in clear. Same functions as the Go package: `new_unencrypted`,
`encrypt`, `encrypt_keystore`, `decrypt_keystore`, `get_private_key`,
`decrypt`, `verify`, `read_passphrase_file`, `save_to_file`,
`load_from_file`, `public_key_bytes`, `is_keystore_file`, `key_type_name`.

Equivalence with the Go package is checked from the Go side:
`go test ./util/keystore/` builds the `keystore_tool` binary of this crate
with cargo and exchanges key files in both directions. The test is skipped
when cargo is not installed.

The crate itself: `cargo test`. Used by the reference nonce seeker in
`proxi/node_cmd/mine/nonce_seeker`.
