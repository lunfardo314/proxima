# txbuildercore_wasm — end-to-end wasm wallet demo

A minimal end-to-end demonstration of using `ledger/txbuildercore` to compose
and sign a Proxima transaction from a wasm-targeted Go binary. The
flow matches what a browser wallet would do:

1. Load the compiled library JSON (embedded at build time via
   `//go:embed`; in production the host ships a new wasm binary
   whenever the library version changes).
2. Parse it via `encoding/json` into `engine.LibraryFromJSON`.
3. Build a `txbuildercore.Library`.
4. Compose a transfer transaction:
   - one consumed sigLock output (synthesised here),
   - two produced outputs (recipient + change),
   - signature unlock at input 0.
5. ed25519-sign and emit the raw tx bytes.

## Run as a normal Go program

```sh
go run ./examples/txbuildercore_wasm/
```

Output is the library hash, the sender / recipient holder IDs, and
the signed tx bytes in hex.

## Build to wasm (TinyGo)

```sh
tinygo build -target=wasm -o /tmp/txbuildercore_demo.wasm ./examples/txbuildercore_wasm/
```

Approximate size (TinyGo 0.41.1, Go 1.26): **2.0 MB raw / 650 KB
gzipped.** This is larger than the floor measured for the
"transaction builder alone" wasm probe (1.3 MB / 429 KB gzipped at
`ledger/txbuildercore/wasm/main.go`) because the demo additionally pulls
in `encoding/json` for the library JSON parse and embeds the 93 KB
library snapshot. A production wallet has two options to reduce
that overhead:

- **Slim library snapshot** — drop functions the wallet doesn't
  compose against (delegation-only / sequencer-only entries on a
  pure-PRXI-transfer wallet, for example).
- **Host-side JSON parse** — let the JS host parse `library.json`
  and pass already-parsed descriptors via wasm imports. Drops
  `encoding/json` entirely; the wallet's wasm binary returns
  closer to the 429 KB floor.

Neither is implemented here. The demo prioritises self-contained
"`go run` works end-to-end" over minimum binary size.

## Regenerating the library snapshot

`library.json` in this directory is a snapshot of the host's compiled
ledger library, produced from the testing parameters. Regenerate
when the ledger library changes:

```sh
go run ./examples/txbuildercore_wasm/genlib/
```

The committed snapshot will then update with a new hash; the wasm
binary built against it commits to that hash.

## Files

| File | Purpose |
|---|---|
| `main.go` | The wasm-buildable demo. Loads library, composes tx, signs, prints. |
| `library.json` | Embedded compiled ledger-library snapshot (regenerable via `genlib/`). |
| `genlib/main.go` | One-shot helper that regenerates `library.json` from the running ledger code. NOT compiled into wasm. |
| `README.md` | This file. |

## What's NOT in this example

- **No host imports / JS glue.** A production wasm wallet exports
  one or more JS-callable functions; this demo just runs `main` and
  prints. Adapt with `//go:wasmexport` (TinyGo 0.41+) or the older
  `syscall/js` callback pattern.
- **No fetch of consumed UTXOs from a real host.** The consumed
  input is synthesised inline. A real wallet receives consumed
  UTXOs as bytes via an HTTP API.
- **No submit flow.** The wallet hands the signed tx bytes to the
  host (HTTP POST); the host runs validation + adds to the workflow
  queue. The unified `/api/v1/submit` host endpoint is tracked
  separately from this refactor.
- **No transaction-builder helpers beyond sigLock.** Tag-along,
  delegation, native tokens, redeemers, chain transitions, and
  sequencer-request encoding are inventoried in
  `claude/wasm_txbuilder_helpers.md` and tracked as a separate
  extension of the txbuildercore refactor.

## Cross-references

- [claude/wasm_txbuilder.md](../../claude/wasm_txbuilder.md) — the
  txbuildercore refactor spec (Phases 0-6 shipped).
- [claude/wasm_txbuilder_helpers.md](../../claude/wasm_txbuilder_helpers.md)
  — analysis of the next batch of wallet helpers.
- [claude/wasm_easyfl.md](../../claude/wasm_easyfl.md) — the
  easyfl-side `engine` / `embed` split this refactor depends on.
