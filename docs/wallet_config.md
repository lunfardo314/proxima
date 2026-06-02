# Proxi wallet configuration (`proxi.yaml`)

This document describes the configuration profile read by the `proxi` CLI wallet
(`proxi.yaml`). Every tag below was verified against its `viper.Get*` read-site
in the code, so the defaults and semantics reflect the actual implementation.

`proxi` is a simple CLI front end for the Proxima network, intended mostly for
admin tasks and as a demo. Most commands build and sign transactions and talk to
a node over the HTTP API (configured by `api.endpoint`). The exception is the
`proxi db …` family, which opens a local BadgerDB directly and does not use this
profile's API settings. The node itself is configured separately — see
[`node_config.md`](node_config.md) for `proxima.yaml`.

## File location and profile selection

- `proxi` loads a profile named **`proxi.yaml`** from the current working
  directory by default.
- The `--config <name>` / `-c <name>` global flag selects a different profile:
  `proxi -c mywallet …` loads `./mywallet.yaml`. (Default name: `proxi`.)
- Environment variables are also read (viper `AutomaticEnv`), so any tag can be
  overridden by its upper-cased env var.
- A profile is created with `proxi config wallet` (see below).

> This file documents the **wallet profile** only. For node (`proxima.yaml`)
> configuration see [`node_config.md`](node_config.md).

## Quick reference

| Tag | Type | Purpose                                                         |
|-----|------|-----------------------------------------------------------------|
| `default_sequencer_id` | hex chain ID | Fallback sequencer used when own / tag-along sequencer is unset |
| `wallet.key_file` | path | Keystore (`.key`) file holding the wallet's private key         |
| `wallet.holder_id` | hex | Optional consistency check against the key file                 |
| `wallet.sequencer_id` | hex chain ID | Sequencer controlled by this wallet (for `seq withdraw` etc)    |
| `api.endpoint` | URL | Node REST API endpoint `proxi` talks to                         |
| `api.timeout_sec` | int | Optional HTTP client timeout (seconds)                          |
| `tag_along.fee` | uint64 | Tag-along fee attached to outgoing transactions                 |
| `tag_along.sequencer_id` | hex chain ID | Tag-along sequencer (optional; falls back to default)           |

---

## `default_sequencer_id`

Top-level tag. A hex-encoded chain ID used as the fallback sequencer whenever a
more specific sequencer is not configured — both `wallet.sequencer_id` and
`tag_along.sequencer_id` fall back to it. Read by `GetDefaultSequencerID`
(`proxi/glb/profile.go`); an invalid value is treated as "unset".

`proxi config wallet` seeds this with the **bootstrap sequencer ID**
(`ledger.BoostrapSequencerIDHex`,
`9d2c6fedeb0f31a9a97d28c59b276402f6c8e78777b89a825e31496c08ef8d6d`).

```yaml
default_sequencer_id: 9d2c6fedeb0f31a9a97d28c59b276402f6c8e78777b89a825e31496c08ef8d6d
```

---

## `wallet.*`

The wallet account: its key file, an optional holder-ID check, and the
sequencer it controls.

| Tag | Type | Default | Description |
|-----|------|---------|-------------|
| `wallet.key_file` | path | `proxima.key` (when created via `proxi config wallet`) | Path to the JSON keystore (`.key`) file holding the wallet's ED25519 private key. Loaded (and decrypted if needed) by `GetPrivateKey`. Managed with `proxi util key` (below). |
| `wallet.holder_id` | hex | "" (no check) | If set, `proxi` asserts it matches the holder ID derived from the key file's public key, failing fast on a mismatched key/profile. Read by `GetWalletData`. |
| `wallet.sequencer_id` | hex chain ID | "" (→ `default_sequencer_id`) | The sequencer chain controlled by this wallet's key. Used e.g. by `proxi node seq withdraw` to pull tokens off the sequencer chain. If empty, falls back to `default_sequencer_id`. Read by `GetOwnSequencerID`. |

The holder ID is `hex(blake2b(sigType || publicKey))` — the same value the
keystore stores as `holder_id`.

```yaml
wallet:
  key_file: proxima.key
  holder_id: 7d3142a5af76d4be9de683d8f492dce2110936d553415102be768cf4df8cacc1
  sequencer_id: 9d2c6fedeb0f31a9a97d28c59b276402f6c8e78777b89a825e31496c08ef8d6d
```

---

## `api`

How `proxi` reaches the node's REST API.

| Tag | Type | Default | Description |
|-----|------|---------|-------------|
| `api.endpoint` | URL | (required) | Base URL of the node API, e.g. `http://127.0.0.1:8000`. Must point at the node's `api.port` (see [`node_config.md` § `api`](node_config.md)). Overridable per-command via the `--api.endpoint` flag on `node`/`snapshot` subcommands. |
| `api.timeout_sec` | int | (client default) | HTTP client timeout in seconds. Only applied when `> 0`; otherwise the client default is used. Not written by `proxi config wallet` — add it manually if needed. |

```yaml
api:
  endpoint: http://127.0.0.1:8000
  # timeout_sec: 30
```

> Cross-reference: `api.endpoint`'s port must equal the node's `api.port`
> (`proxima.yaml`). On the testnet, sequencer nodes serve the API on `:8000` and
> access nodes on `:8001`.

---

## `tag_along`

Outgoing transactions reach a sequencer's backlog via a small **tag-along**
output (fee). Only one tag-along sequencer is supported at a time.

| Tag | Type | Default | Description |
|-----|------|---------|-------------|
| `tag_along.fee` | uint64 | `1` (template) | Fee amount attached as the tag-along output. Read by `GetTagAlongFee`. |
| `tag_along.sequencer_id` | hex chain ID | "" (→ `default_sequencer_id`) | Sequencer that should pick up the transaction. If empty, falls back to `default_sequencer_id`. Read by `GetTagAlongSequencerID`, which also validates (via the node) that the ID is a live sequencer chain. |

```yaml
tag_along:
  fee: 1
  # sequencer_id: <tag-along sequencer ID>
```

---

## Generating a wallet profile: `proxi config wallet`

```
proxi config wallet [<profile name>]      # default name: proxi → proxi.yaml
```

Writes a new `<name>.yaml` profile (refuses to overwrite an existing one) and
ensures a key file exists:

1. **Key file** — if `proxima.key` already exists, offers to reuse it; otherwise
   prompts for ≥10 random seed characters, generates an ED25519 key, and
   optionally encrypts it with a passphrase.
2. **Profile** — renders the template (`proxi/config_cmd/wallet_profile.template`)
   with:
   - `wallet.key_file: proxima.key`
   - `wallet.holder_id:` derived from the generated/loaded key
   - `wallet.sequencer_id:` and `default_sequencer_id:` both set to the bootstrap
     sequencer ID
   - `api.endpoint: http://127.0.0.1:8000`
   - `tag_along.fee: 1` (and a commented `tag_along.sequencer_id`)

The file is written with `0600` permissions.

### Generated profile example

```yaml
# Proxi wallet profile

default_sequencer_id: 9d2c6fedeb0f31a9a97d28c59b276402f6c8e78777b89a825e31496c08ef8d6d

wallet:
    key_file: proxima.key
    holder_id: <derived holder ID hex>
    sequencer_id: 9d2c6fedeb0f31a9a97d28c59b276402f6c8e78777b89a825e31496c08ef8d6d
api:
    endpoint: http://127.0.0.1:8000

tag_along:
    fee: 1
#    sequencer_id: <tag-along sequencer ID>
```

> Cross-reference: when you run `proxi config node --standalone`/`--sequencer`,
> the node's `sequencer.controller_key_file` is filled from this profile's
> `wallet.key_file` (or the default `proxima.key`). See
> [`node_config.md` § Generating a starting config](node_config.md).

---

## Key management: `proxi util key`

The key file referenced by `wallet.key_file` is a JSON keystore holding an
ED25519 key (optionally encrypted with Argon2id + AES-256-GCM). Subcommands:

| Command | Purpose | Key flags |
|---------|---------|-----------|
| `proxi util key generate` | Generate a new ED25519 key pair into a `.key` file (prompts for entropy). | `--output`/`-o` (default `proxima.key`), `--encrypt`, `--hint` |
| `proxi util key encrypt` | Encrypt an existing unencrypted `.key` file with a passphrase. | `--file` (default `proxima.key`), `--hint` |
| `proxi util key decrypt` | Decrypt an encrypted `.key` file back to plaintext. | `--file` (default `proxima.key`) |
| `proxi util key info` | Print key-file metadata (version, type, encrypted, holder ID); `--verify` checks the private key matches the public key. | `--file` (default `proxima.key`), `--verify` |

Keystore fields: `version`, `key_type` (0 = ED25519), `public_key`, `holder_id`
(`hex(blake2b(sigType || publicKey))`), `private_key`, and — when encrypted —
the KDF/cipher parameters plus an optional `hint`.

### Passphrase for encrypted keys

When a command needs the private key from an encrypted file, `proxi` looks for
the passphrase in this order:
1. `PROXIMA_KEY_PASSPHRASE` environment variable;
2. interactive stdin prompt.

The decrypted key is cached for the lifetime of the (short-lived) `proxi`
process, so each command prompts at most once.

> Cross-reference: on a **node**, the sequencer's controller key is loaded from
> `sequencer.controller_key_file` and uses the `SEQUENCER_KEY_PASSPHRASE` env
> var instead. See [`node_config.md` § `sequencer`](node_config.md).

---

## Related global flags

These are command-line flags, not profile tags, but they affect how `proxi`
uses the profile:

| Flag | Effect |
|------|--------|
| `--config <name>` / `-c` | Load profile `<name>.yaml` instead of `proxi.yaml`. |
| `--target <lock>` / `-t` | Override the destination lock (EasyFL source); defaults to the wallet's own account. |
| `--force` / `-f` | Bypass yes/no confirmation prompts. |
| `--nowait` / `-n` | (node subcommands) Submit without waiting for ledger inclusion. |
| `--verbose` / `-v`, `--v2` / `-2` | Verbosity level 1 / 2. |

A couple of subcommands take a YAML/flag input that is *not* part of the wallet
profile: `proxi node fund --targets <file>` (default `distribute.yaml`) reads a
list of `{target, amount}` entries for a multi-output send.

> The `faucet.*` keys (`proxi node getfunds`) belong to the faucet client, which
> is currently disabled in the build, so they are not part of the active wallet
> profile.
