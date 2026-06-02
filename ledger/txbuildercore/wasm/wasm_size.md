# txbuildercore wasm — size assessment

**Toolchain:** TinyGo 0.41.1 + Go 1.26 (linux/amd64 host).

## Headline

`./ledger/txbuildercore/wasm/main.go` is now the **syscall/js wallet
wrapper** (the JS-callable compose+sign API — see README.md), measured
2026-05-29:

| Build | Wasm | Gzipped |
|---|---:|---:|
| Default (with debug symbols) | 2.03 MB | 668 KB |
| **Size-opt (`-no-debug -opt=z`)** | **741 KB** | **274 KB** |

The 741 KB / 274 KB number is what a browser wallet would actually
ship. Default-build size is debug bloat.

The earlier **compose+sign floor probe** (no JS glue, no library JSON
parse) measured 2026-05-20 at 1.35 MB / 442 KB default and **370 KB /
151 KB** size-opt. The wrapper's extra ~120 KB gzipped over that floor
is almost entirely `encoding/json` (for `InitLibrary`) plus the
`syscall/js` host bridge. A wallet that parses `library.json` JS-side
and passes parsed descriptors in would drop `encoding/json` and return
close to the floor. The per-bucket breakdown below is from the original
floor probe and is still representative of everything except the
JSON-parse path.

## Where the bytes go

Code-section bytes from the **wrapper** build (measured 2026-05-29):

```
tinygo build -size=full -target=wasm -o /tmp/proxima_txb_dbg.wasm ./ledger/txbuildercore/wasm/
```

`flash` total = **433 KB** (code 358 KB + data 76 KB; this is the
loaded image, excluding the DWARF debug sections that inflate the raw
`.wasm` file to 2.0 MB). Unlike the old floor probe, the wrapper
**invokes the library** (`CompileExpression` + the helper emitters), so
the easyfl compile/decompile serde is now linked — the line item that
was missing before.

### Our own code (≈68 KB)

| Package | Code (B) | Notes |
|---|---:|---|
| `easyfl/engine` | 28 536 | **EasyFL compile + decompile serde** — the source→bytecode compiler and the bytecode parser/decompiler. Reachable now because the wrapper calls `CompileExpression`; DCE'd in the old placeholder-lock probe. |
| `main` | 19 151 | the syscall/js wrapper itself (arg marshalling, the export table, error wrapping) |
| `ledger/txbuildercore` | 12 094 | compose ops + the helper emitters (sigLock / tagAlong / chainLock / amounts / index-values) |
| `easyfl/tuples` | 5 513 | tuple builder + serialise (output/tx wire form) |
| `ledger/base` | 3 294 | tx-ID / output-ID / holder-ID / ledger-time types |
| `easyfl/easyfl_util` | 1 008 | int trim/widen, assert helpers |

The compose+serde core (engine + txbuildercore + tuples + base +
easyfl_util, **≈50 KB**) is the irreducible "build & sign a Proxima tx"
cost; `main` is the JS-binding glue on top.

### Library-JSON parse (≈47 KB)

| Package | Code (B) | Notes |
|---|---:|---|
| `encoding/json` | 46 566 | only for `InitLibrary`. A wallet that parses `library.json` JS-side and passes parsed descriptors in via a future import-based loader would DCE this entirely. |

(`encoding/json` also drags much of `reflect` / `reflectlite` /
`internal/fmtsort` below.)

### Crypto (≈57 KB)

| Sub-pkg | Code (B) |
|---|---:|
| `crypto/internal/fips140/edwards25519` | 14 622 |
| `…/aes` | 8 313 |
| `…/sha3` | 7 565 |
| `…/edwards25519/field` | 6 538 |
| `…/sha256` | 4 395 |
| `…/ed25519` | 3 569 |
| `…/aes/gcm` | 3 367 |
| `…/hmac` | 3 087 |
| `…/sha512` | 2 834 |
| `…/drbg` | 1 246 |
| `…/subtle` + `fips140` + other | ~1 300 |
| `golang.org/x/crypto/blake2b` | 4 967 |

ed25519's elliptic-curve math plus the FIPS-140 module's
transitively-linked-but-mostly-unused SHA/AES; blake2b for the input
commitment + tx-ID hash.

### Platform / stdlib (≈140 KB)

| Bucket | Code (B) | Notes |
|---|---:|---|
| `slices` | 35 005 | generic slice helpers (instantiated heavily by engine + json) |
| `internal/reflectlite` | 28 185 | pulled by `encoding/json`, `fmt`, `errors` |
| `internal/strconv` | 19 138 | int↔string + format primitives |
| `fmt` | 18 423 | error-message formatting paths |
| `runtime` | 16 190 | TinyGo runtime, alloc, gc |
| `syscall/js` | 7 784 | wasm host bridge |
| `strconv` | 6 042 | decimal-string amount parsing |
| `strings` | 5 150 | |
| `reflect` + `internal/fmtsort` | ~4 600 | via `encoding/json` |
| `math/rand` | 1 160 + 4856 data | dragged via `crypto/ed25519`'s deterministic signer |
| `bufio` / `bytes` / `encoding/base64,hex,binary` / `unicode` | ~10 000 | small direct deps |

## Interpretation

- The **EasyFL serde (`easyfl/engine`, 28.5 KB)** is the single
  largest line item in our own code — it is what turns the helper
  source strings (`sigLock`, `chain(…)`, `delegateLock(…)`) into the
  bytecode the node accepts, and decompiles bytecode back for display.
  It is the price of "compose any constraint client-side" and is not
  reducible without giving up source-level compile.
- **`encoding/json` (47 KB) is the biggest single removable cost** —
  it exists only to parse the library JSON in `InitLibrary`. Parsing
  JS-side and importing parsed descriptors would also shed most of the
  `reflect*` / `fmtsort` drag.
- **Crypto (~57 KB)** is the next bucket; most of the FIPS SHA/AES code
  is linked-but-unused and could be DCE'd by avoiding `crypto/rand`
  (ed25519 signing is deterministic) or by externalising signing/hashing
  to the browser's WebCrypto.
- The compose+sign core itself (engine + txbuildercore + tuples + base)
  is a modest ~50 KB.

## Optimisation headroom (not done — current size is fine)

| Lever | Estimated saving | Cost |
|---|---|---|
| Parse `library.json` JS-side, import parsed descriptors — DCEs `encoding/json` + most of `reflect*` / `fmtsort` | ~50–60 KB code | import-based loader + JS glue |
| Replace `crypto/rand` with a minimal entropy source — ed25519 signing is deterministic; this would DCE AES, SHA3, AES-GCM, HMAC, DRBG | ~20 KB code | small wrapper module |
| Swap `fmt.Errorf` for `errors.New` + fixed sentinels in hot paths | ~10–15 KB | mechanical refactor |
| Externalise signing and hashing to JS host (browser has native `WebCrypto`) | up to ~50 KB (skip ed25519 + blake2b + SHA-512) | host-bridge complexity; harder unit testing |

The compile/decompile serde (`easyfl/engine`, ~28 KB) is **not** on
this list — it is the wallet's core value (compose any constraint
client-side) and removing it would defeat the purpose.

274 KB gzipped is well within reasonable for a browser wallet bundle —
comparable to a small npm dependency. None of the above is needed
unless targeting a much smaller floor; the biggest easy win is the
JS-side JSON parse.

## How to reproduce

```bash
# Default (debug-built; larger raw file because of DWARF sections).
tinygo build -target=wasm -o /tmp/proxima_txb.wasm ./ledger/txbuildercore/wasm/
ls -la /tmp/proxima_txb.wasm            # 2.03 MB
gzip -c /tmp/proxima_txb.wasm | wc -c   # 668 KB

# Size-optimised (what a shipped wallet should bundle).
tinygo build -target=wasm -no-debug -opt=z -o /tmp/proxima_txb_opt.wasm \
    ./ledger/txbuildercore/wasm/
ls -la /tmp/proxima_txb_opt.wasm        # 741 KB
gzip -c /tmp/proxima_txb_opt.wasm | wc -c  # 274 KB

# Per-package attribution (only works with debug symbols).
tinygo build -size=full -target=wasm -o /tmp/proxima_txb_dbg.wasm \
    ./ledger/txbuildercore/wasm/
```
