# Sequencer Private Key Security

## Problem

The sequencer controller's ED25519 private key was stored as plaintext hex in `proxima.yaml` with world-readable permissions (0666). Anyone with filesystem access could steal the key and impersonate the sequencer.

## Security Assessment

### Options Evaluated

| # | Strategy | Effort | Security | Ops Cost | Verdict |
|---|----------|--------|----------|----------|---------|
| 1 | File permissions 0600 | Very low | Baseline | None | Immediate fix |
| 2 | Separate key file | Low | Moderate | Low | Immediate fix |
| 3 | Environment variable | Very low | Moderate | Low | Standard practice, not sufficient alone |
| 4 | Passphrase-encrypted keystore | Medium | High | Medium | Planned (see below) |
| 5 | OS keyring / Vault | Medium-High | High | High | Future, enterprise |
| 6 | HSM / hardware wallet | High | Very high | High | Impractical for high-frequency sequencer signing |

### Key Findings

- **0666 permissions** on config files containing private keys — most egregious issue
- Private key mixed with non-sensitive config (version control risk, accidental sharing)
- No encryption at rest
- No key material zeroing in memory (Go runtime limitation)

## Implemented Changes (Options 1 + 2)

### Option 1: File permissions fix (0666 → 0600)

All `os.WriteFile` calls for files containing private keys changed to 0600:

| File | Line | Config File |
|------|------|-------------|
| `proxi/node_cmd/setup_seq.go` | 142 | `proxi.yaml` |
| `proxi/node_cmd/setup_seq.go` | 171 | `proxima.yaml` |
| `proxi/init_cmd/init_node_config.go` | 83 | `proxima.yaml` |
| `proxi/init_cmd/init_wallet.go` | 51 | `proxi.yaml` |

### Option 2: Separate key file (`proxima_sequencer.key`)

**Config loading** (`sequencer/config.go`):
- New `loadControllerKey()` function checks `controller_key_file` first, falls back to inline `controller_key`
- Reads file, trims whitespace, parses hex-encoded ED25519 key
- Clear error messages for missing/invalid key

**Setup command** (`proxi/node_cmd/setup_seq.go`):
- `updateNodeConfig()` now writes key to `proxima_sequencer.key` (0600 permissions)
- Sets `controller_key_file` in YAML instead of inline `controller_key`
- Removes any existing inline `controller_key` from config

**Documentation updated**:
- `proxi/init_cmd/init_node_config.go`: template shows `controller_key_file` as recommended option
- `docs/run_sequencer.md`: updated config example and field descriptions

**Backward compatibility**: Existing configs with inline `controller_key` continue to work.

## Option 4 Plan: Passphrase-Encrypted Keystore

### Design

Ethereum-inspired keystore format: the private key is encrypted with AES-256-GCM using a key derived from a user passphrase via Argon2id. Supports multiple key types to accommodate future BLS threshold multisig.

### Key Types

| Type | Value | Description | Key Size |
|------|-------|-------------|----------|
| ED25519 | 0 | Standard ed25519 signing key (current default) | 64 bytes |
| BLS partial | 1+ | Partial BLS key for threshold multisig (future) | TBD |

The `key_type` field is stored in the keystore. The encryption/decryption logic is key-type-agnostic (encrypts raw bytes), but verification after decryption is key-type-specific:
- Type 0 (ED25519): derive public key from private key, compare to stored `pubkey`
- Type 1+ (BLS): derive public key share from partial key, compare to stored `pubkey` (verification logic added when BLS is implemented)

### Why GCM + pubkey is sufficient for verification

AES-256-GCM is an authenticated encryption scheme. The GCM authentication tag is computed over the ciphertext and serves as a built-in integrity check. If the wrong passphrase is used, the derived key is wrong, and GCM decryption fails with an authentication error rather than producing garbage. This is the primary verification mechanism.

The `pubkey` field stored in plaintext in the keystore provides a second layer: after successful GCM decryption, the public key is derived from the decrypted private key (in a key-type-specific way) and compared against the stored `pubkey`. This catches any edge case and also allows identifying which account the keystore belongs to without decrypting it.

No separate "known plaintext" field is needed.

### Keystore File Format (`proxima_sequencer.keystore`)

```json
{
  "version": 1,
  "key_type": 0,
  "crypto": {
    "cipher": "aes-256-gcm",
    "kdf": "argon2id",
    "kdf_params": {
      "time": 3,
      "memory": 65536,
      "threads": 4,
      "salt": "<hex>"
    },
    "nonce": "<hex>",
    "ciphertext": "<hex>"
  },
  "pubkey": "<hex>"
}
```

- `version`: format version, currently 1
- `key_type`: 0 = ED25519 (default), future values for BLS partial keys
- `crypto`: encryption parameters and ciphertext
- `pubkey`: public key (or public key share for BLS) for identification and post-decryption verification

### How Argon2id Works (brief)

Argon2id is a memory-hard key derivation function (KDF). Given a passphrase and salt, it produces a fixed-length derived key. Parameters:
- **time** (iterations): number of passes over memory. Higher = slower to brute-force.
- **memory** (KiB): amount of RAM used. 65536 = 64 MiB. Makes GPU/ASIC attacks expensive.
- **threads**: parallelism degree.
- **salt**: random bytes (16 bytes), stored alongside. Prevents rainbow table attacks.

The derived 32-byte key is used as the AES-256-GCM encryption key.

### Implementation Plan

#### Step 1: Keystore library (`util/keystore/`)

Create `util/keystore/keystore.go`:

```go
// Key type constants
const (
    KeyTypeED25519 = 0
    // KeyTypeBLSPartial = 1  // future
)

type KDFParams struct {
    Time    uint32 `json:"time"`
    Memory  uint32 `json:"memory"`
    Threads uint8  `json:"threads"`
    Salt    string `json:"salt"`    // hex-encoded
}

type CryptoData struct {
    Cipher     string    `json:"cipher"`
    KDF        string    `json:"kdf"`
    KDFParams  KDFParams `json:"kdf_params"`
    Nonce      string    `json:"nonce"`      // hex-encoded
    Ciphertext string    `json:"ciphertext"` // hex-encoded
}

type Keystore struct {
    Version int        `json:"version"`
    KeyType int        `json:"key_type"`
    Crypto  CryptoData `json:"crypto"`
    PubKey  string     `json:"pubkey"` // hex-encoded
}
```

Functions:
- `Encrypt(keyType int, privateKey []byte, pubkey []byte, passphrase string) (*Keystore, error)`
  - Key-type-agnostic: encrypts arbitrary private key bytes
  - Generate 16-byte random salt, 12-byte random nonce
  - Derive 32-byte key via `argon2.IDKey(passphrase, salt, time, memory, threads, 32)`
  - Encrypt private key bytes with AES-256-GCM (ciphertext includes auth tag)
  - Store pubkey and key_type in plaintext
- `(ks *Keystore) Decrypt(passphrase string) ([]byte, error)`
  - Derive key from passphrase + stored salt using same Argon2id params
  - Decrypt with AES-256-GCM (GCM auth tag verifies correct passphrase)
  - Return raw private key bytes (caller interprets based on key_type)
- `(ks *Keystore) Verify(passphrase string) error`
  - Decrypt, then perform key-type-specific pubkey verification:
    - KeyTypeED25519: derive ed25519 public key from private key, compare to stored pubkey
    - Other types: return error "unsupported key type for verification" (until BLS is implemented)
  - Used by `check_keystore` command
- `(ks *Keystore) SaveToFile(path string) error` — marshal JSON, write with 0600
- `LoadFromFile(path string) (*Keystore, error)` — read file, unmarshal JSON

Dependencies: `golang.org/x/crypto/argon2`, `crypto/aes`, `crypto/cipher`, `crypto/rand` (all stdlib except argon2).

#### Step 2: CLI commands

**`proxi util encrypt_key`** — create a keystore from existing key:
- Read key from `proxima_sequencer.key` or `--key-file` flag
- Key type defaults to 0 (ED25519), overridable with `--key-type` flag
- Prompt for passphrase (twice for confirmation), or read from `--passphrase` flag
- Call `keystore.Encrypt()`
- Save to `proxima_sequencer.keystore`
- Update `proxima.yaml`: set `controller_key_file: proxima_sequencer.keystore`
- Prompt to delete plaintext key file

**`proxi util check_keystore`** — verify keystore integrity:
- Read keystore from `proxima_sequencer.keystore` or `--file` flag
- Display key type and account (derived from stored pubkey) without needing passphrase
- Prompt for passphrase (or read from `--passphrase` flag)
- Call `ks.Verify(passphrase)`:
  - GCM auth tag failure → "wrong passphrase or corrupted keystore"
  - Pubkey mismatch → "decrypted key does not match stored public key (keystore corrupted)"
  - Unsupported key type → "key type N: decryption OK, pubkey verification not available"
  - Success → "keystore OK, key type: ED25519, account: <address>"
- Exit code 0 on success, 1 on failure (scriptable)

#### Step 3: Config loading integration (`sequencer/config.go`)

Extend `loadControllerKey()`:
- Detect keystore format: try JSON parse, check for `"version"` field
- If keystore detected:
  - Verify `key_type == KeyTypeED25519` (reject unsupported types at config load time)
  - Check `PROXIMA_KEY_PASSPHRASE` env var first
  - If no env var: prompt on stdin using `golang.org/x/term.ReadPassword()` (no echo)
  - Call `ks.Decrypt(passphrase)`, interpret as ed25519.PrivateKey
- If plain hex: use existing path (backward compatible)

Priority chain: `controller_key_file` (keystore or plain) > `controller_key` (inline).

#### Step 4: Documentation

- Update `docs/run_sequencer.md` with keystore setup instructions
- Add security recommendations section

### CLI Output Examples

```
$ proxi util encrypt_key
Reading key from proxima_sequencer.key...
Key type: ED25519
Account: sigLock(0x0530b790e0e7de62...)
Enter passphrase: ********
Confirm passphrase: ********
Keystore saved to proxima_sequencer.keystore
Updated proxima.yaml: controller_key_file = proxima_sequencer.keystore
Delete plaintext key file proxima_sequencer.key? [y/N]: y
Plaintext key file deleted.

$ proxi util check_keystore
Reading keystore from proxima_sequencer.keystore...
Key type: ED25519
Account (from stored pubkey): sigLock(0x0530b790e0e7de62...)
Enter passphrase: ********
Keystore OK. Decrypted key matches stored public key.

$ proxi util check_keystore
Reading keystore from proxima_sequencer.keystore...
Key type: ED25519
Account (from stored pubkey): sigLock(0x0530b790e0e7de62...)
Enter passphrase: ********
ERROR: wrong passphrase or corrupted keystore
```

### Risks and Mitigations

| Risk | Mitigation |
|------|-----------|
| User forgets passphrase | Pubkey in keystore identifies which key is lost; no recovery possible (by design) |
| Unattended restart needs passphrase | Support `PROXIMA_KEY_PASSPHRASE` env var (weaker but pragmatic) |
| Argon2 dependency | `golang.org/x/crypto` is well-maintained, widely used |
| Key still in memory at runtime | Go limitation; out of scope for this option |
| Wrong passphrase → unclear error | GCM auth tag gives clear "authentication failed" error |
| Unknown key type in keystore | Config loader rejects unsupported types; check_keystore reports "decryption OK, verification not available" |

### Estimated Effort

- Keystore library: ~180 lines
- CLI commands (encrypt_key + check_keystore): ~150 lines
- Config loading changes: ~40 lines
- Tests: ~120 lines
- Documentation: ~50 lines
- Total: ~540 lines, ~1 session

## Session Log

- Analyzed current private key storage: plaintext hex in `proxima.yaml`, 0666 permissions
- Identified all `os.WriteFile` calls with 0666 for files containing keys (4 locations)
- Fixed permissions to 0600 in all 4 locations
- Added `loadControllerKey()` function with `controller_key_file` > `controller_key` priority
- Modified `setup_seq.go` to write key to `proxima_sequencer.key` and reference via `controller_key_file`
- Updated config template and documentation
- Verified build passes
- Planned option 4 (passphrase-encrypted keystore) with `encrypt_key` and `check_keystore` commands
- Added `key_type` field (0=ED25519, future values for BLS partial keys in threshold multisig)

### Session 2: Option 4 Implementation

- Created `util/keystore/keystore.go`: Encrypt/Decrypt/Verify/SaveToFile/LoadFromFile/IsKeystoreFile (~210 lines)
  - Argon2id KDF (time=3, memory=64MiB, threads=4) + AES-256-GCM
  - `key_type` field: 0=ED25519, extensible for future BLS
  - Pubkey stored in plaintext for identification + post-decryption verification
- Created `util/keystore/keystore_test.go`: 9 tests, all passing (~220 lines)
  - Round-trip, wrong passphrase, pubkey mismatch, file I/O, IsKeystoreFile, empty passphrase, unknown key type
- Created `proxi/util_cmd/util_encrypt_key.go`: `proxi util encrypt_key` command
  - Reads plaintext key, prompts passphrase (no-echo via x/term), encrypts, saves .keystore
  - Updates proxima.yaml, offers to delete plaintext key file
- Created `proxi/util_cmd/util_check_keystore.go`: `proxi util check_keystore` command
  - Displays key type + account from stored pubkey, prompts passphrase, verifies integrity
- Extended `sequencer/config.go` `loadControllerKey()`:
  - Auto-detects keystore JSON vs plain hex via `IsKeystoreFile()`
  - New `loadFromKeystore()`: checks `PROXIMA_KEY_PASSPHRASE` env var, falls back to stdin prompt
- Registered both commands in `proxi/util_cmd/util.go`
- Updated `docs/run_sequencer.md` with keystore section
- Added `golang.org/x/term` dependency
- Build passes, all tests pass

### Session 3: Option 3 (Environment Variable)

- Added `PROXIMA_SEQUENCER_KEY` env var support in `loadControllerKey()`
- Priority chain: env var > controller_key_file > controller_key (inline)
- Updated docs/run_sequencer.md with env var documentation

## Advanced Security Alternatives Assessment

### Remaining Attack Surface

Options 1-4 protect the key **at rest** on disk. Once the node starts, the raw ed25519 private key lives in Go process memory as a plain `[]byte`. Remaining attack vectors:

- **Memory dumps**: core dumps, `/proc/<pid>/mem`, swap files, hibernation images
- **Process access**: root or same-user can ptrace/attach and read memory
- **Cold boot attacks**: DRAM contents persist briefly after power-off
- **Compromise of the running process**: RCE in the node exposes the key

### Option 5: OS Keyring / Secret Store

**Linux**: kernel keyring (`keyctl`), GNOME Keyring, KDE Wallet, `libsecret`
**macOS**: Keychain Services
**Go library**: `github.com/zalando/go-keyring` (cross-platform wrapper)

| Aspect | Assessment |
|--------|-----------|
| What it solves | Key stored in OS-managed encrypted storage instead of files. Access control via user session, not file permissions. |
| What it doesn't solve | Key still loaded into Go process memory at runtime. Root can still extract from keyring. |
| Implementation | Medium — ~100 lines. Abstract behind interface, platform-specific backends. |
| Operational cost | Low on desktop, problematic on headless servers (GNOME Keyring needs D-Bus session). Linux kernel keyring works headless but has limited tooling. |
| Dependency risk | `go-keyring` is thin but adds platform-specific CGO dependency on Linux (libsecret). |

**Verdict**: Marginal improvement over encrypted keystore for server deployments. The keystore already encrypts at rest; OS keyring mainly adds convenience (auto-unlock on login) rather than meaningfully stronger security. Better suited for desktop wallet use cases, not headless sequencer nodes.

### Option 6: HashiCorp Vault / Cloud KMS

**Vault**: self-hosted secrets manager with ACL, audit log, lease/rotation, transit engine
**Cloud KMS**: AWS Secrets Manager, GCP Secret Manager, Azure Key Vault

| Aspect | Assessment |
|--------|-----------|
| What it solves | Centralized secret management, access audit trail, automatic rotation, no key material on disk. Vault Transit engine can do **remote signing** (key never leaves Vault). |
| What it doesn't solve | If using "fetch key" mode: key still in process memory. If using Transit (remote signing): adds latency per signature. |
| Implementation — fetch mode | Medium — ~150 lines. Vault client, auth (AppRole/token), fetch secret, parse key. |
| Implementation — transit mode | High — ~400 lines. Requires signing interface abstraction. Vault Transit supports ed25519 since Vault 1.13+. |
| Operational cost | High — requires running Vault cluster (or cloud subscription). Unsealing, HA, backup. Network dependency for node startup. |
| Latency (transit) | ~1-5ms per signing operation over localhost, ~10-50ms over network. Sequencer signs every few hundred ms, so tolerable if local. |

**Verdict**: Strong option for production multi-node deployments. Vault Transit with ed25519 would be the gold standard — key **never** enters the Go process. But it's heavy infrastructure for a single sequencer. Best offered as an optional integration, not a default.

**Practical path**: Implement fetch-mode first (simpler, key still in memory but managed centrally). Transit-mode as a future option requiring a signing interface refactor.

### Option 7: TPM 2.0 (Trusted Platform Module)

Hardware chip on most modern servers/laptops. Stores keys in tamper-resistant silicon.

| Aspect | Assessment |
|--------|-----------|
| What it solves | Key generated inside TPM, never leaves hardware. Signing operations happen on-chip. Survives OS compromise (key not extractable). |
| What it doesn't solve | TPM 2.0 does NOT support ed25519. Only RSA and ECDSA (P-256, P-384). Would require protocol-level signature scheme change. |
| Implementation | Not feasible without changing Proxima's signature scheme. |
| Performance | ~50-200ms per RSA-2048 sign on typical TPM. ECDSA P-256 faster (~5-20ms). Not suitable for high-frequency sequencer signing even if scheme were changed. |

**Verdict**: Blocked by ed25519 incompatibility. TPM 2.0 spec doesn't include ed25519. Some TPMs implement it as a vendor extension but this is not portable. Not a viable path unless Proxima adds ECDSA support.

### Option 8: YubiKey / Hardware Security Key

YubiKey 5 supports ed25519 via OpenPGP or FIDO2/PIV interfaces.

| Aspect | Assessment |
|--------|-----------|
| What it solves | Key generated on device, never extractable. Each signing operation requires physical hardware. |
| What it doesn't solve | Latency: ~50-100ms per ed25519 sign on YubiKey. Sequencer at pace=5 (400ms) signs every ~400ms — this is borderline. Touch requirement (if enabled) makes it impractical for automated signing. |
| Implementation | High — ~300 lines. PKCS#11 or `piv-go` library. Need signing interface abstraction. |
| Operational cost | Hardware procurement (~$50/key). Physical access. No redundancy without backup key ceremony. |
| Go libraries | `github.com/go-piv/piv-go` (PIV), `github.com/paultag/go-ykpiv` |

**Verdict**: Viable for low-frequency operations (e.g., signing the initial chain origin, emergency key rotation). Not practical for continuous sequencer signing due to latency and physical presence requirements. Could be useful as a **controller key** for administrative operations while a derived/delegated key handles routine signing.

### Option 9: Memory Protection (mlock + guard pages)

Prevent key material from being swapped to disk or appearing in core dumps.

| Aspect | Assessment |
|--------|-----------|
| What it solves | Key bytes locked in RAM (not swappable), excluded from core dumps. Reduces exposure in memory forensics. |
| What it doesn't solve | Root can still read `/proc/<pid>/mem`. RCE in the process can still read the key. Go's GC may copy the key during compaction. |
| Implementation | Low-Medium — ~80 lines. `syscall.Mlock()` on the key buffer. `prctl(PR_SET_DUMPABLE, 0)` to disable core dumps. But Go's runtime allocates/copies freely, so the key may exist in multiple memory locations despite mlock on one copy. |
| Go limitation | **Fundamental problem**: Go's garbage collector moves objects in memory. You cannot guarantee a `[]byte` stays at one address. Even with mlock, copies may exist in unlocked pages. `runtime.SetFinalizer` for zeroing is unreliable. |

**Verdict**: Provides defense-in-depth against swap/coredump but has fundamental limitations in Go. The GC problem means it's "best effort" rather than a guarantee. Worth doing as a low-cost hardening measure, but don't oversell its security properties.

**Practical implementation**:
- `syscall.Mlockall(syscall.MCL_CURRENT | syscall.MCL_FUTURE)` at process start (locks all memory, crude but effective)
- `prctl(PR_SET_DUMPABLE, 0)` to disable core dumps
- Zero key bytes when sequencer shuts down (defense-in-depth, not guaranteed by GC)

### Option 10: Threshold Signing (No Single Key Exists)

Split the signing key into N shares, require T-of-N to produce a signature. No single party ever holds the complete key.

| Aspect | Assessment |
|--------|-----------|
| What it solves | Eliminates single point of compromise. Even if one node is fully compromised, the attacker cannot sign. |
| What it doesn't solve | Requires coordination between T parties for each signature. Adds latency and complexity. |
| Implementation | Very high — requires BLS threshold signature scheme or ed25519 threshold signing (FROST protocol). ~1000+ lines. Requires network protocol between signers. |
| Protocol impact | The `key_type` field in the keystore was designed with this in mind. BLS partial keys would be key_type=1. |
| Performance | FROST ed25519: ~2 round trips between signers per signature. BLS: single round but different curve. |

**Verdict**: The strongest option architecturally — it's the only approach where compromising a single machine is insufficient. But it requires significant protocol-level work and a network of cooperating signers. This is the long-term vision that the `key_type` field anticipates.

### Comparative Summary

| Option | At-rest | In-memory | Root-proof | Hardware-bound | Effort | Practical for sequencer? |
|--------|---------|-----------|------------|----------------|--------|------------------------|
| Current (keystore) | Strong | None | No | No | Done | Yes |
| OS Keyring | Strong | None | No | No | Medium | Marginal gain |
| Vault fetch | Strong | None | No | No | Medium | Yes (with infra) |
| Vault transit | Strong | **Protected** | Partial | No | High | Yes (local Vault) |
| TPM 2.0 | N/A | **Protected** | **Yes** | **Yes** | Blocked | No (no ed25519) |
| YubiKey | N/A | **Protected** | **Yes** | **Yes** | High | No (latency) |
| mlock | Unchanged | Partial | No | No | Low | Best-effort |
| Threshold | N/A | **Protected** | **Yes** | Optional | Very high | Future |

### Recommended Next Steps (by priority)

1. **mlock + disable core dumps** (low effort, immediate hardening) — add `Mlockall` and `prctl` calls at node startup. Acknowledging Go GC limitations.
2. **Vault fetch-mode integration** (medium effort) — add `controller_key_vault` config option. Practical for operators who already run Vault. Key still in memory but centrally managed with audit trail.
3. **Signing interface abstraction** (medium effort, enabler) — refactor sequencer to use a `Signer` interface instead of raw `ed25519.PrivateKey`. This unlocks Vault Transit, HSM, and threshold signing without further refactoring.
4. **Threshold signing (FROST/BLS)** (high effort, long-term) — the `key_type` field is already in place. This is the architecturally correct solution that eliminates single-point key compromise.
