# txbuildercore wasm — size assessment

**Measured:** 2026-05-20
**Probe:** `./ledger/txbuildercore/wasm/main.go` — exercises the full
compose + sign path (one consumed UTXO, one produced UTXO,
signature unlock, ed25519 sign).
**Toolchain:** TinyGo 0.41.1 + Go 1.26 (linux/amd64 host).

## Headline

| Build | Wasm | Gzipped |
|---|---:|---:|
| Default (with debug symbols) | 1.35 MB | 442 KB |
| **Size-opt (`-no-debug -opt=z`)** | **370 KB** | **151 KB** |

The 370 KB / 151 KB number is what a browser wallet would actually
ship. Default-build size is debug bloat.

## Where the bytes go

Code-section bytes from `tinygo build -size=full -target=wasm
-o /tmp/txbuildercore_dbg.wasm ./ledger/txbuildercore/wasm/` (default build —
attribution unavailable in `-no-debug` mode).

### Platform (≈354 KB)

| Bucket | Code (B) | Notes |
|---|---:|---|
| `crypto/internal/fips140/*` total | ~50 000 | ed25519 + transitively-linked SHA/AES |
| `fmt` | 17 689 | mostly error-message formatting paths |
| `runtime` | 16 084 | TinyGo runtime, alloc, gc |
| `internal/reflectlite` | 14 726 | pulled in by `fmt`, `errors` |
| `internal/strconv` | 10 749 | int→string + format primitives |
| `slices` | 5 531 | generic slice helpers |
| `syscall/js` | 5 462 | wasm host bridge |
| `golang.org/x/crypto/blake2b` | 4 919 | input commitment + tx-ID hash |
| `math/rand` | 432 + 4856 data | dragged via `crypto/ed25519`'s deterministic signer |

Sub-breakdown of `crypto/internal/fips140/*`:

| Sub-pkg | Code (B) |
|---|---:|
| `edwards25519` | 14 618 |
| `aes` | 8 379 |
| `sha3` | 7 565 |
| `edwards25519/field` | 6 538 |
| `sha256` | 4 353 |
| `aes/gcm` | 3 367 |
| `ed25519` (wrapper) | 3 316 |
| `sha512` | 2 834 |
| `hmac` | 3 087 |
| `drbg` | 1 246 |
| ...other | ~1 200 |

### Our own code (≈16 KB)

| Package | Code (B) |
|---|---:|
| `github.com/lunfardo314/proxima/ledger/txbuildercore` | 6 387 |
| `github.com/lunfardo314/easyfl/tuples` | 5 700 |
| `github.com/lunfardo314/proxima/ledger/base` | 2 914 |
| `github.com/lunfardo314/easyfl/easyfl_util` | 656 |

Plus `~1.5 KB` of small stdlib used directly (`bytes`,
`encoding/hex`, `encoding/binary`).

## Interpretation

- Our compose-side code is **~4%** of the binary.
- Crypto dominates (~50 KB across ed25519's elliptic-curve math +
  the FIPS-140 module's transitively-linked-but-unused SHA/AES).
- Error formatting (`fmt` + `reflectlite` + `strconv`) is the
  second-largest bucket at **~43 KB** — driven by `fmt.Errorf` /
  `fmt.Sprintf` call sites in stdlib + our code.

## Optimisation headroom (not done — current size is fine)

| Lever | Estimated saving | Cost |
|---|---|---|
| Replace `crypto/rand` with a minimal entropy source — ed25519 signing is deterministic; this would DCE AES, SHA3, AES-GCM, HMAC, DRBG | ~20 KB code | small wrapper module |
| Swap `fmt.Errorf` for `errors.New` + fixed sentinels in hot paths | ~10–15 KB | mechanical refactor |
| Externalise signing and hashing to JS host (browser has native `WebCrypto`) | up to ~50 KB (skip ed25519 + blake2b + SHA-512) | host-bridge complexity; harder unit testing |

151 KB gzipped is well within reasonable for a browser wallet
bundle — comparable to a small npm dependency. None of the above
is needed unless targeting <50 KB.

## How to reproduce

```bash
# Default (debug-built, slowest path, larger binary; matches the
# numbers the txbuildercore_wasm demo prints).
tinygo build -target=wasm -o /tmp/txbuildercore.wasm ./ledger/txbuildercore/wasm/
ls -la /tmp/txbuildercore.wasm        # 1.35 MB
gzip -c /tmp/txbuildercore.wasm | wc -c  # 442 KB

# Size-optimised (what a shipped wallet should bundle).
tinygo build -target=wasm -no-debug -opt=z -o /tmp/txbuildercore_opt.wasm \
    ./ledger/txbuildercore/wasm/
ls -la /tmp/txbuildercore_opt.wasm    # 370 KB
gzip -c /tmp/txbuildercore_opt.wasm | wc -c  # 151 KB

# Per-package attribution (only works with debug symbols).
tinygo build -size=full -target=wasm -o /tmp/txbuildercore_dbg.wasm \
    ./ledger/txbuildercore/wasm/
```
