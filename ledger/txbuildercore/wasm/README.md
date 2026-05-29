# txbuildercore/wasm — WebAssembly transaction-builder wallet

A `syscall/js` wrapper around `ledger/txbuildercore`, compiled with
**TinyGo** to a WebAssembly binary that runs in the browser (JS, React,
or any frontend). It is a **compose + sign** transaction builder: the
frontend assembles a transaction, signs it with an ed25519 key, and
gets back the raw canonical transaction bytes to POST to a node. No
local validation — the host backend runs Stage-3 at submit time.

Everything the wallet needs is supplied from the JS side:

- the compiled ledger-library JSON (the node advertises it; `InitLibrary`),
- the consumed UTXO bytes (fetched from the node over its HTTP API),
- the ed25519 private key.

See [`claude/wasm_txbuilder.md`](../../../claude/wasm_txbuilder.md) for the
refactor that made `txbuildercore` TinyGo-clean, and `wasm_size.md` in
this directory for the binary-size breakdown.

## Build

```sh
# Default build (debug symbols, larger).
tinygo build -target=wasm -o proxima_txb.wasm ./ledger/txbuildercore/wasm/

# What a shipped wallet should bundle (smaller, no debug info).
tinygo build -target=wasm -no-debug -opt=z -o proxima_txb.wasm ./ledger/txbuildercore/wasm/
```

This package is **wasm-only** (`//go:build js && wasm`). `go build ./...`
from the repo root silently skips it on non-wasm targets; building the
directory directly on a non-wasm target reports "build constraints
exclude all Go files", which is expected.

You also need TinyGo's `wasm_exec.js` glue:

```sh
cp "$(tinygo env TINYGOROOT)/targets/wasm_exec.js" .
```

## Model

- The ledger library is a single package-global, set once by
  `InitLibrary(<library json>)`.
- Multiple in-flight transactions are supported. Each is an independent
  builder addressed by an **int handle** kept in a package-global map.
  `NewTxBuilder(upgradeIndex)` allocates one and returns its handle;
  **every builder op takes the handle as its first argument**.
- All byte payloads cross the JS boundary as **hex strings**. `uint64`
  amounts cross as **decimal strings** (JS numbers lose precision above
  2^53). Small indices/counts cross as JS numbers.
- All exports live on the global object `proxima`. Every call returns a
  plain object `{ ok: bool, err?: string, ... }`. A thrown Go panic is
  caught and returned as `{ ok:false, err:"panic: …" }`, so a bad call
  never tears down the instance.

## JS usage

```js
// 1. boot the wasm module (TinyGo wasm_exec.js defines globalThis.Go)
const go = new Go();
const { instance } = await WebAssembly.instantiate(wasmBytes, go.importObject);
go.run(instance);                 // installs globalThis.proxima, then blocks
const P = globalThis.proxima;

// 2. install the library the node advertises
const r = P.InitLibrary(libraryJSONString);
if (!r.ok) throw new Error(r.err);   // r.hash is the canonical library hash

// 3. derive the wallet's holder ID from its key
const me = P.HolderIDFromPrivateKey(privKeyHex);  // 32-byte seed or 64-byte key

// 4. build a PRXI transfer: 1 input (fetched from node) -> recipient + change
const { handle } = P.NewTxBuilder(0);             // upgradeIndex baked at build time
P.ConsumeOutput(handle, consumedOutputHex, consumedOutputIDHex);
P.ProduceSigLockOutput(handle, "60000000", recipientHolderIDHex);
P.ProduceSigLockOutput(handle, "40000000", me.holderID);  // change
// optional fee to a sequencer so the tx gets pulled:
// P.ProduceTagAlongOutput(handle, "1000", targetSeqChainIDHex, me.holderID);

// 5. unlock, stamp, commit, sign
P.PutSignatureUnlock(handle, 0);                  // input 0 unlocked by the signature
P.SetTimestamp(handle, slot, tick);
P.ComputeInputCommitment(handle);
P.SignED25519(handle, privKeyHex);

// 6. raw signed tx bytes (hex) — POST to the node's submit endpoint
const tx = P.TxBytes(handle).tx;
P.FreeTxBuilder(handle);
```

For more than one input use `P.PutStandardInputUnlocks(handle, n)`
(input 0 signs, inputs 1..n-1 reference it) instead of step 5's single
`PutSignatureUnlock`.

## API reference

All functions are methods on `proxima` and return `{ ok, ... }`.

### Library / global

| Function | Returns | Notes |
|---|---|---|
| `InitLibrary(libraryJSON)` | `{ ok, hash }` | Parse + install the global library. Call once before any compose op. `hash` is the canonical library hash to compare with the host. |
| `LibraryHash()` | `{ ok, hash }` | The installed library's canonical hash. |
| `CompileExpression(source)` | `{ ok, bytecode }` | Escape hatch: compile any EasyFL source expression to bytecode hex. Build arbitrary constraints the convenience helpers don't cover (delegation, foundry, redeemers, …). |

### Builder lifecycle

| Function | Returns | Notes |
|---|---|---|
| `NewTxBuilder(upgradeIndex)` | `{ ok, handle }` | Allocate a builder. `upgradeIndex` is the library version, a build-time constant for the wallet. |
| `FreeTxBuilder(handle)` | `{ ok }` | Release a builder. |

### Generic compose

| Function | Returns | Notes |
|---|---|---|
| `EncodeAmounts([amountStr, …])` | `{ ok, bytecode }` | Amounts vector (output slot 0). Index 0 = balance, 1 = inflation, 2+ = frozen-coverage; trailing zeros elided. |
| `EncodeIndexValues([hex, …])` | `{ ok, bytecode }` | Index-values tuple (output slot 1). Master/sender at position 0. |
| `BuildOutput([constraintHex, …])` | `{ ok, output }` | Assemble an output tuple from constraint bytecodes in slot order. Fully generic — pair with `CompileExpression` / `EncodeAmounts` / `EncodeIndexValues`. |
| `ConsumeOutput(handle, outputHex, outputIDHex)` | `{ ok, index }` | Register a consumed UTXO (bytes + 33-byte output ID). |
| `ProduceOutput(handle, outputHex)` | `{ ok, index }` | Register a produced UTXO from raw bytes. |

### Convenience produce helpers

| Function | Returns | Notes |
|---|---|---|
| `ProduceSigLockOutput(handle, amountStr, holderIDHex)` | `{ ok, index }` | Standard PRXI output locked to a holder. |
| `ProduceTagAlongOutput(handle, feeStr, targetSeqIDHex, senderIDHex)` | `{ ok, index }` | Fee output that gets the tx pulled by a sequencer. |
| `ProduceChainLockOutput(handle, amountStr, chainIDHex)` | `{ ok, index }` | Output controlled by a chain's controller. |

### Unlocks / endorsements / tx-level

| Function | Returns | Notes |
|---|---|---|
| `PutSignatureUnlock(handle, inputIndex)` | `{ ok }` | Mark an input unlocked by the tx signature. |
| `PutUnlockReference(handle, inputIndex, constraintIndex, referencedInputIndex)` | `{ ok }` | Point an input's lock at an earlier input's unlock params. |
| `PutStandardInputUnlocks(handle, n)` | `{ ok }` | Input 0 signs; inputs 1..n-1 reference input 0's lock. |
| `PushEndorsement(handle, txidHex)` | `{ ok }` | Endorse a transaction ID. |
| `PushTxConstraint(handle, bytecodeHex)` | `{ ok }` | Append a tx-level constraint (e.g. a `redeemScript`). |

### Finalise + sign

| Function | Returns | Notes |
|---|---|---|
| `SetTimestamp(handle, slot, tick)` | `{ ok }` | Set the ledger timestamp. |
| `ComputeInputCommitment(handle)` | `{ ok }` | Hash the consumed outputs into the input commitment. Call after all compose ops, before signing. |
| `SignED25519(handle, privKeyHex)` | `{ ok }` | Sign the current state. `privKeyHex` is a 32-byte seed or 64-byte full key. |
| `TxBytes(handle)` | `{ ok, tx }` | Raw canonical (signed) transaction bytes as hex. |

### Key utilities

| Function | Returns | Notes |
|---|---|---|
| `HolderIDFromPrivateKey(privKeyHex)` | `{ ok, holderID, publicKey }` | Derive the holder ID (and pubkey) from a private key. |
| `HolderIDFromPublicKey(publicKeyHex)` | `{ ok, holderID }` | Derive the holder ID from a public key. |

## Composing constraints the helpers don't cover

Any output shape is reachable via the generic surface. For example a
delegation, foundry, or `tokenAmount` output:

```js
const amounts = P.EncodeAmounts(["100000000"]).bytecode;
const iv      = P.EncodeIndexValues([masterHolderIDHex, targetChainIDHex]).bytecode;
const lock    = P.CompileExpression("delegateLock(0x, z16/50, z32/100, 7)").bytecode;
const chain   = P.CompileExpression("chain(0x" + "00".repeat(32) + ", 0x, z32/123, 0x, 0x, 0x, 0x)").bytecode;
const state   = P.CompileExpression("delegateLockState(z32/0, 0)").bytecode;
const out     = P.BuildOutput([amounts, iv, lock, chain, state]).output;
P.ProduceOutput(handle, out);
```

The canonical source strings each constraint expects are the same ones
the server-side parsers accept (see `ledger/txbuildercore/helpers_*.go`),
so emitted bytes are byte-identical to the node's own builders.

## What's NOT here

- **No local validation.** The wallet hands the signed bytes to the
  node (HTTP POST `submit_tx`); the node runs Stage-3 and queues it.
- **No state queries.** Consumed UTXOs, the target sequencer's chain
  ID, baselines, etc. are fetched from the node's HTTP API by the JS
  host and passed in as hex.
- **No sequencer compose.** Milestone / branch / freeze transactions
  come from the running sequencer process, never from a wallet.
- **Library JSON parsing pays for `encoding/json`.** A wallet that
  wants the smallest binary can parse `library.json` on the JS side and
  pass already-parsed descriptors via a future import-based loader,
  dropping `encoding/json` from the wasm binary.

## Cross-references

- [`claude/wasm_txbuilder.md`](../../../claude/wasm_txbuilder.md) — the
  txbuildercore refactor spec (Phases 0–6 shipped).
- [`claude/wasm_txbuilder_helpers.md`](../../../claude/wasm_txbuilder_helpers.md)
  — the wallet-helper inventory.
- [`claude/wasm_easyfl.md`](../../../claude/wasm_easyfl.md) — the
  easyfl `engine` / `embed` split this depends on.
- `wasm_size.md` (this directory) — binary-size breakdown.
- `examples/txbuildercore_wasm/` — **deprecated** standalone demo,
  superseded by this wrapper.
