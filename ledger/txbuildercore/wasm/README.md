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
const me = P.HolderIDFromPrivateKeyED25519(privKeyHex);  // 32-byte seed or 64-byte key

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

## Full workflow against a node (raw REST)

The complete round trip — fetch the library, fetch the UTXOs to spend,
compose + sign a realistic PRXI transfer (recipient + change +
tag-along fee), submit with full-context validation, and handle a
validation error. Only `fetch` and the `proxima` wasm exports are used;
nothing else.

Need a node to develop against? See
[`docs/run_standalone.md`](../../../docs/run_standalone.md) for spinning
up a throwaway single-node network with a bootstrap sequencer on your
laptop, serving the REST API used below at `http://127.0.0.1:8000`.

```js
const NODE = "http://127.0.0.1:8000";   // a node's REST API base

const getJSON  = async (p)    => (await fetch(NODE + p)).json();
const postJSON = async (p, b) => (await fetch(NODE + p, {
  method: "POST",
  headers: { "content-type": "application/json" },
  body: JSON.stringify(b),
})).json();

// --- boot the wasm module (TinyGo wasm_exec.js defines globalThis.Go) ---
const go = new Go();
const { instance } = await WebAssembly.instantiate(wasmBytes, go.importObject);
go.run(instance);
const P = globalThis.proxima;

// what we're sending, and to whom
const MY_PRIV_HEX        = "…";  // 32-byte seed or 64-byte ed25519 key
const RECIPIENT_HOLDER   = "…";  // 32-byte holder ID hex
const SEND = 1_000_000n;         // PRXI to send  (BigInt — amounts are uint64)
const FEE  = 500n;               // tag-along fee to a sequencer

// 1. fetch the compiled ledger library the node advertises, and install it
const def = await getJSON("/api/v1/get_ledger_definition");
const init = P.InitLibrary(def.library_json);
if (!init.ok) throw new Error("InitLibrary: " + init.err);
if (init.hash !== def.library_hash)        // we must build against the node's library
  throw new Error(`library hash mismatch: wasm ${init.hash} vs node ${def.library_hash}`);

// who am I (derive holder ID from the key)
const me = P.HolderIDFromPrivateKeyED25519(MY_PRIV_HEX);

// 2. fetch spendable sigLock UTXOs covering (send + fee). `for_amount`
//    lets the node return just enough inputs, newest-first.
const need = (SEND + FEE).toString();
const outs = await getJSON(
  `/api/v1/get_outputs?index_value=${me.holderID}&lock_type=sigLock&for_amount=${need}`);
if (outs.error) throw new Error("get_outputs: " + outs.error);
const inputs = outs.outputs || [];
if (inputs.length === 0) throw new Error("no spendable outputs");

// 3. compose a realistic transfer: recipient + change + tag-along fee
const { handle } = P.NewTxBuilder(0);     // upgradeIndex baked into the wasm at build time
try {
  let total = 0n;
  for (const u of inputs) {
    P.ConsumeOutput(handle, u.data, u.id);              // u.data = raw output bytes, u.id = 33-byte output ID
    total += BigInt(P.DecodeTokenBalance(u.data).amount);
  }
  const change = total - SEND - FEE;
  if (change < 0n) throw new Error("insufficient funds");

  P.ProduceSigLockOutput(handle, SEND.toString(), RECIPIENT_HOLDER);
  if (change > 0n) P.ProduceSigLockOutput(handle, change.toString(), me.holderID);

  // tag-along fee so a sequencer pulls the tx (pick any active sequencer,
  // e.g. from GET /api/v1/get_sequencers)
  const TARGET_SEQ_CHAINID = "…";  // 32-byte chain ID hex
  P.ProduceTagAlongOutput(handle, FEE.toString(), TARGET_SEQ_CHAINID, me.holderID);

  // input 0 signs; the rest reference its unlock
  P.PutStandardInputUnlocks(handle, inputs.length);

  // 4. timestamp = the node's current ledger time (authoritative clock —
  //    no client-side wall-clock conversion needed).
  const now = await getJSON("/api/v1/get_ledger_time");
  P.SetTimestamp(handle, now.slot, now.tick);
  // (the tx ts must be later than every consumed output by at least
  //  transaction_pace ticks; the node's "now" satisfies that for
  //  normal UTXOs.)

  P.ComputeInputCommitment(handle);
  P.SignED25519(handle, MY_PRIV_HEX);
  const txHex = P.TxBytes(handle).tx;

  // 5. submit WITH full-context validation. Passing consumed_utxos makes
  //    the node run Stage-3 (input commitment, all constraint scripts,
  //    ledger invariant) before enqueuing — fail fast on a bad tx.
  //    Set validate_only:true to dry-run without enqueuing.
  const res = await postJSON("/api/v1/submit_tx", {
    tx_bytes:       txHex,
    consumed_utxos: inputs.map(u => u.data),
    // validate_only: true,
  });

  // 6. handle the result
  if (res.ok) {
    console.log("submitted, txid =", res.tx_id);
  } else {
    // res.stage ∈ { "parse", "full", "submit" }; res.error is the message.
    //   parse  — malformed bytes / bad signature / partial-context invariant
    //   full   — Stage-3: input commitment, a constraint script, or conservation
    //   submit — accepted but the enqueue failed (e.g. node overloaded)
    throw new Error(`submit failed at '${res.stage}': ${res.error}`);
  }
} finally {
  P.FreeTxBuilder(handle);
}
```

Notes:

- **`get_outputs` returns raw bytes only** (`{ id, data }`), so the
  wallet totals input amounts with `DecodeTokenBalance` to size the
  change output. Conservation (`Σinputs == Σoutputs`) is enforced by
  the ledger; an off-by-one shows up as a `full`-stage error.
- **`consumed_utxos` is the `data` of each consumed output**, in input
  order — exactly the bytes passed to `ConsumeOutput`. Omit it to skip
  full-context validation and submit on parse-only (faster, less safe).
- **Library match matters.** Bytecode the wallet emits is only accepted
  by a node running the same library; the `init.hash === def.library_hash`
  check guards against a stale wasm bundle.

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
| `EncodeAmounts([amountStr, …])` | `{ ok, bytecode }` | Amounts vector (output tuple index 0). Position 0 = balance, 1 = inflation, 2+ = frozen-coverage; trailing zeros elided. |
| `EncodeIndexValues([hex, …])` | `{ ok, bytecode }` | Index-values tuple (output tuple index 1). Master/sender at position 0. |
| `BuildOutput([constraintHex, …])` | `{ ok, output }` | Assemble an output tuple from constraint bytecodes in tuple-index order. Fully generic — pair with `CompileExpression` / `EncodeAmounts` / `EncodeIndexValues`. |
| `DecodeTokenBalance(outputHex)` | `{ ok, amount }` | Token balance (amounts vector, position 0) of an output, as a decimal string. Total the consumed inputs with it to compute change. |
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
| `HolderIDFromPrivateKeyED25519(privKeyHex)` | `{ ok, holderID, publicKey }` | Derive the holder ID (and pubkey) from a private key. |
| `HolderIDFromPublicKeyED25519(publicKeyHex)` | `{ ok, holderID }` | Derive the holder ID from a public key. |

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
- [`docs/run_standalone.md`](../../../docs/run_standalone.md) — run a
  throwaway standalone node to develop the wallet against.
