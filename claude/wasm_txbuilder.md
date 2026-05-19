# WASM transaction-builder core — analysis and spec

Status: **design / analysis, no implementation**. Started 2026-05-18;
spec refreshed 2026-05-19 after the easyfl `engine`/`embed` split
shipped.

Sibling: [wasm_easyfl.md](wasm_easyfl.md) covers the easyfl-side work
this depends on (now shipped — Phases A + B + C, easyfl `c2f3713`).

---

## Goal

Expose a minimal Go package — `ledger/txcore` — that can be compiled
with TinyGo to WebAssembly and run inside a browser-based wallet (or
any isolated frontend). The frontend's job is to:

1. Build a transaction from inputs + outputs + endorsements.
2. Serialize it to the on-the-wire byte form.
3. Sign it (ed25519 over the tx ID).

The frontend does **not** validate the transaction locally. Validation
is left to the host backend, reached through the REST API (see
"Companion API" below).

**Out of scope for the wasm core** (deliberately not a priority):

- Sequencer-specific compose operations. A wallet does not issue
  sequencer transactions, does not need to populate
  `SequencerDataBytes`, and does not need `MakeSequencerTransaction` /
  `CalcFrozenCoverageDelta` / `MustPutFrozenCoverage`. Eliminating
  these removes the `proxima/sequencer/seqdata` import from compose
  paths.
- Local validation. The host runs Stage-3.

Practical constraint: today's `ledger/txbuilder` transitively pulls in
roughly everything in `proxima/ledger` plus `proxima/sequencer/seqdata`,
`proxima/util/lines`, `proxima/ledger/multistate`,
`proxima/ledger/transaction`, and `unitrie/common`. That's far larger
than is acceptable for a wallet binary, and several of those packages
are not TinyGo-compatible.

Non-goal: rewriting the wallet UX, defining a new wire format, or
shipping an actual wasm binary. Those follow the refactor.

---

## Companion API — unified submit endpoint

The wallet is compose+sign only, but users want pre-submission
feedback ("will this transaction validate?"). Rather than introduce a
separate `/validate_dry_run` endpoint, **extend the existing submit
endpoint** to subsume both flows in one call.

Sketch:

```
POST /api/v1/submit
{
  "tx":             "<hex>",               // required, raw tx bytes
  "consumed_utxos": ["<hex>", "<hex>", …], // optional, enables full-context validation
  "validate_only":  false                  // optional; default false
}
```

Behaviour:

1. **Parse + partial-context validate** (always, synchronous).
   `transaction.Parse` + `ValidatePartialContext` covers tuple
   structure, txID, input/endorsement scan, signature, partial-context
   invariants. Fails fast on bad bytes.

2. **Full-context validate** if `consumed_utxos` is supplied. The
   handler treats them as the input loader, then runs
   `ValidateFullContext` (same as `transaction.ValidateFullContext` —
   input commitment, constraint scripts, ledger invariant).

3. **Submit** unless `validate_only=true`. Submission is the existing
   path (route into the workflow's `txinput_queue`).

Response shape: `{ "ok": true, "tx_id": "<hex>" }` on success or
`{ "ok": false, "stage": "parse|partial|full|submit", "error": "…" }`
on failure.

Why fold them together: the wallet's natural flow is "validate as
much as I can, then submit if ok". Two endpoints means the wallet
calls them sequentially with the same tx bytes; one endpoint with
flags is the same logical operation with less round-tripping. Host
already runs Parse + ValidatePartialContext at submit time — the only
new code is honoring `consumed_utxos` for the full-context branch and
`validate_only` for the early-return branch.

Independent of the wasm refactor; lands on the host side and the
wasm wallet calls it.

---

## Frontend use cases driving scope

From the existing CLI consumers in `proxi/node_cmd/`:

- `send` (PRXI transfer) — `New()` → `ConsumeOutput` × N →
  `ProduceOutput(SigLock-output)` → optional tag-along → `SignED25519`
  → bytes.
- `send_tagged` (native-token transfer) — same plus
  `tokenAmount(...)` extras on the produced output and a
  `token(tag, 0xFF)` pure-conservation declaration.
- `foundry create / mint / burn / retire` — chain origin + transit
  patterns using foundry helpers, `FinishChainUnlockParams`.
- `delegate amount / chain` — delegation lock outputs.
- `killchain` — `MakeEndChainTransaction`.
- `fund` — multi-input PRXI consolidation.

Every one of these is compose + sign. They differ only in which
constraint kinds they assemble.

Explicitly **not** wasm targets:

- Sequencer milestones, branch transactions, sequencer freeze
  (delegation-freeze) transactions. These come from the running
  sequencer process, not from a wallet.

---

## Two costs in the current stack

### Cost A — TxBuilder shape

`ledger/txbuilder` itself is ~1300 LOC and structurally compose-side.
Heavy pieces are confined to a handful of methods:

| Method | Purpose | In wasm core? |
|---|---|---|
| `New` / `ConsumeOutput` / `ProduceOutput` / `PutUnlockParams` / `PutSignatureUnlock` / `PutUnlockReference` / `PushTxConstraint` / `Push*` | builder ops | **YES** |
| `transactionData.ToTuple` / `Bytes` | serialise tx essence | **YES** |
| `SignED25519` | sign over tx ID | **YES** (needs blake2b + ed25519) |
| Common-element helpers (`NewSigLockOutput`, foundry/delegate convenience builders, tag-along, etc.) | wallet ergonomics | **YES** — inherited from full txbuilder |
| `Transaction()` / `BuildTransactionWithValidation()` / `BytesWithValidation()` | round-trip parse + Stage-3 validate | **NO** — full-build only |
| `GetChainAccount(...IndexedStateReader...)` | state-query helper | **NO** — wallet uses host API |
| `LoadInput` | feeds the validator | **NO** |
| `CalcFrozenCoverageDelta` / `MustPutFrozenCoverage` | sequencer freeze-tx convenience | **NO** — sequencer-only |

txcore's `TxBuilder` is the universal low-level compose API: it lets
the wallet construct *any* valid tx shape (any inputs, any outputs,
arbitrary constraints, endorsements, tx-level constraints, sig). The
common-element helpers (sigLock construction, common indices, chain
origin, tag-along, delegate, foundry transit, native-token amounts)
ride along **in the same package** — they're thin wrappers over the
universal builder and the easyfl source-compiler, no separate
"constraints" sub-package.

Sequencer-only and validation-side methods stay in the full
`ledger/txbuilder` (which becomes a wrapper that re-exports txcore and
adds the heavy pieces).

### Cost B — what `ledger.Output` drags in

`ledger.Output` is needed on the compose side too — every
`ConsumeOutput` / `ProduceOutput` takes one. But the current Output's
companions transitively import:

- `proxima/sequencer/seqdata` — **droppable** once sequencer compose
  paths leave the wasm scope.
- `proxima/util/lines` (pretty-printers; transitively pulls more).
- `proxima/util` (assertions, errors, hex tools — mixed).
- `proxima/ledger/multistate` (only via optional state-query helpers).
- Constraint serde wrappers (`chain.go`, `lock_*.go`, `foundry.go`,
  `native_token.go`, `delegate*.go`, …) — each adds compile-time deps
  on `easyfl` helpers.

**Simpler OutputBuilder for txcore:** rather than porting the full
`ledger.OutputBuilder` (which is monolithic and has constraint-kind-
specific methods), txcore introduces a thin extension of
`easyfl/tuples.TupleEditable`:

```go
// in ledger/txcore
type OutputBuilder struct{ *tuples.TupleEditable }

func NewOutputBuilder() *OutputBuilder
func (b *OutputBuilder) PutAt(idx byte, data []byte)   // overwrite slot
func (b *OutputBuilder) Append(data []byte) byte       // push, return idx
func (b *OutputBuilder) Bytes() []byte                 // serialise
func (b *OutputBuilder) Output() *Output               // wrap in Output value
```

That's the core. The constraint-helpers (`WithAmounts`, sig lock,
chain origin, etc.) become free functions that compose canonical
source via the loaded library, push into the builder, return the
result. No constraint-specific state on the builder itself.

The minimal compose-side surface for `Output` is:
- The `Output` value (raw bytes + accessors used by builders).
- The simpler `OutputBuilder` above.
- `HashOutputs(...)` (blake2b over each output's bytes, concatenated).
- Path constants (`ConstraintIndexLock`, `ConstraintIndexChain`,
  `ConstraintIndexAmounts`, `ConstraintIndexFoundry`,
  `ConstraintIndexFoundryPolicy`, `ConstraintIndexDelegationParams`).
- `base.OutputID`, `base.TransactionID`, `base.ChainID`,
  `base.LedgerTime`, `base.HolderID`, `base.MakeOriginChainID`,
  `base.SignatureTypeED25519`, etc. — `ledger/base` is small and
  already TinyGo-friendly.

Pretty-printer / debugging accessors (`String`, `LinesPlainSource`,
`LinesShort`, `_runOutputs`, etc.) stay in the full package as
methods on the same Output type (separate files).

---

## Architectural pivot: compile-from-source as the primary path

A wallet **fundamentally** needs only "compile this EasyFL source
expression with the loaded library, get bytes back". Every typed
wrapper today is sugar over:

```go
mustBinFromSource(fmt.Sprintf("<symbol>(<arg0>, <arg1>, ...)"))
```

— a Go-readable façade around what is, at the bytecode layer, just a
call to `easyfl/engine.Library.CompileExpression(source)`. Decoding
works the same way: pure EasyFL decompile already turns bytecode back
into source, no Go-side serde wrappers required for inspection.

txcore packages this as one cohesive surface:

```
ledger/txcore/
├── output.go               — minimal Output + OutputBuilder
├── tx_data.go              — transactionData + ToTuple + Bytes
├── txbuilder.go            — universal builder ops (compose only)
├── sign.go                 — SignED25519 + TxIDFromBytes
├── library.go              — TinyGo-clean library loader (delegates to engine)
├── library.json            — embedded definitions (host-canonical)
└── helpers.go              — common-element wallet helpers
   ├── NewSigLock(...)        — sigLock(addr) bytecode emitter
   ├── NewChainOriginConstraint(...)
   ├── NewChainConstraint(...)
   ├── NewTagAlongOutput(...)
   ├── NewDelegateLockOutput(...)
   ├── NewFoundryTransit(...)
   ├── NewTokenAmount(...) / NewTokenDeclare(...)
   └── …                       — each is a thin compose function
```

All in **one** package. No separate Layer 1 / Layer 2 split — the
helpers are inherent to the wasm builder's value proposition. They
add zero transitive deps because each is just
`library.CompileExpression("symbol(args)")` plus a put into the
builder.

The full `ledger` package keeps the **parsers / registrars / EasyFL
bodies / validation** — that side is unchanged. Helpers in txcore
emit canonical source strings; the full package's parsers expect the
same strings; they stay byte-for-byte compatible.

---

## Proposed package layout (Proxima side)

```
ledger/                          (kept — full backend / validator)
├── (everything as today)
│   – validators, parsers, serdes, EasyFL bodies …
│   – existing txbuilder/ is a thin wrapper over txcore for the
│     server-side compose+validate+sequencer-helpers path.
│
└── txcore/                      NEW — TinyGo-clean compose+sign core
    ├── output.go
    ├── tx_data.go
    ├── txbuilder.go
    ├── sign.go
    ├── library.go
    ├── library.json
    └── helpers.go               (or split per-kind files: lock.go,
                                  chain.go, foundry.go, …)
```

The full `ledger` package's existing `New<Foo>` constructors delegate
to `txcore/helpers.go` so there's **one source of truth** for each
constraint's canonical bytecode source string. The full package
retains its parsers, serdes, and EasyFL bodies; nothing in the
runtime validator changes.

---

## EasyFL coordination — what shipped

The easyfl side (`wasm_easyfl.md`) is done as of `c2f3713`:

| Item | Status |
|---|---|
| YAML → JSON cutover | shipped 2026-05-18 |
| Crypto out of base library | shipped 2026-05-18 |
| Tracing pruning, `reflect` removal from base, etc. | shipped Phase B |
| Compose / eval / embed split | shipped Phase C (renamed compose → engine) |

What txcore needs from easyfl:

- **`easyfl/engine`** — Library + CompileExpression + DecompileBytecode +
  registration primitives. This is exactly what the wallet imports.
- `easyfl/tuples` (tuple builder + serialize). Lazy-subtree
  thread-safety: TinyGo no-ops `sync.RWMutex` — accept the no-op cost
  rather than build-tag the file.
- `easyfl/easyfl_util` (`Uint64FromBytes`, `Concat`, etc.).

It does **not** need:

- `easyfl/embed` (no eval).
- The top-level `easyfl` facade's JSON serde (wallet parses JSON in
  its own environment, hands txcore a `*engine.LibraryFromJSON`).

txcore's library loader is:

```go
// in ledger/txcore/library.go
import "github.com/lunfardo314/easyfl/engine"

//go:embed library.json
var embeddedLibraryJSON []byte

// LoadEmbeddedLibrary parses the embedded library descriptor (a
// wallet-side decision: it's bundled at build time, no host fetch).
// The wallet's host wasm glue does the JSON parse and hands engine
// the parsed descriptor.
func LoadEmbeddedLibrary[T any]() (*engine.Library[T], error) { … }
```

For wasm minimality, the wallet can swap the embedded JSON for a
slimmed version that drops unused entries. Open question deferred to
Phase 5.

---

## Library-loading model

The wasm bundle embeds the canonical compiled library JSON. The host
ships a new wasm bundle when it upgrades the extended library; the
wallet always uses the matched library hash.

At wasm-init time:

1. Parse the embedded JSON in the wallet's environment (or, for the
   reference wallet, with Go's `encoding/json` since size budget
   allows).
2. Hand the parsed `*engine.LibraryFromJSON` to `engine.Library.Upgrade`
   (no embed callback — wallet doesn't need eval bodies).

The wallet does **not** register Go-side constraint serdes; bytecode
emission is pure source compile, decoding is pure source decompile.

---

## Signing surface

```go
// in ledger/txcore/sign.go
func (txb *TxBuilder) SignED25519(privKey ed25519.PrivateKey)
```

Implementation:

- Build the tx tree via `transactionData.ToTuple().AsTree()`.
- Derive the tx ID by `blake2b.Sum256` over the essence bytes
  (same `hashEssenceBytesFromTransactionDataTree` logic that
  `TxIDFromTransactionDataTree` uses today).
- `ed25519.Sign(privKey, txID[:])`.
- Concat `SignatureTypeED25519 || sig || pubKey` into
  `TransactionData.SignatureData`.

Both `crypto/ed25519` and `golang.org/x/crypto/blake2b` are
TinyGo-compatible (blake2b loses its asm fast path; acceptable).

`TxIDFromTransactionDataTree` itself lives in
`ledger/transaction/parse.go` today. The wasm core ports a stripped
version (no parse validation, just hash + prefix bytes) — call it
`TxIDFromBytes`. If the full-build path can use the txcore helper
directly, delete the duplication from `transaction/parse.go`.

---

## What the wallet API looks like

```go
import "github.com/lunfardo314/proxima/ledger/txcore"

lib, _ := txcore.LoadEmbeddedLibrary[any]()
txb := txcore.New(lib)
for i, oid := range inputIDs {
    txb.ConsumeOutput(consumedOutputs[i], oid)
}
txb.ProduceOutput(txcore.NewSigLockOutput(amount, recipient))     // uses helper
txb.ProduceOutput(txcore.NewTagAlongOutput(tagAlongAmount, seq))  // uses helper
txb.TransactionData.Timestamp = ts
txb.TransactionData.InputCommitment = txcore.HashOutputs(txb.ConsumedOutputs...)
txb.SignED25519(privKey)
rawTx := txb.TransactionData.Bytes()
```

The wasm export wraps this as a single JS-callable function that
takes JSON-shaped input (inputs, outputs, ts, optional endorsements,
private key bytes) and returns the raw tx bytes. The wallet UI never
sees the Go API directly.

For wallets that want to compose arbitrary constraints without going
through the helpers, the underlying `lib.CompileExpression(source)`
+ `OutputBuilder.PutAt(idx, bin)` path is fully exposed.

---

## What stays in `ledger` (full build, server-side)

- All EasyFL bodies (`def/*.easyfl`).
- The evaluator, constraint validators, closing balance checks (e.g.
  `NativeTokenAggregator.CheckBalances`, `validateOutputs`).
- The typed-constraint **parsers** (`<Foo>FromBytes`) and their
  `register<Foo>` calls. txcore helpers only emit; the full-build
  side keeps the reading half.
- All sequencer-related compose helpers
  (`MakeSequencerTransaction`, `CalcFrozenCoverageDelta`,
  `MustPutFrozenCoverage`, …).
- `multistate`, snapshots, the persistent index machinery.
- `proxi` CLI keeps using the **full** `ledger/txbuilder` for v1; it
  doubles as the canary for txcore once the refactor settles.

---

## Phase plan

Strict ordering — each phase must build green before the next starts.

### Phase 0 — Audit verification

- `go list -deps ./ledger/txbuilder/` and confirm the high-cost
  imports we expect (`multistate`, `transaction`, `sequencer/seqdata`,
  `util/lines`).
- Catalogue which `txbuilder` methods the proxi CLI actually calls
  (verify the working set table above).
- Confirm no non-sequencer compose path touches `sequencer/seqdata`
  or frozen-coverage helpers.
- Catalogue which constraint-construction helpers the CLI uses
  (`NewSigLockOutput`, `NewChainOrigin`, `NewTagAlongOutput`,
  `NewDelegateLockOutput`, foundry transit, tokenAmount …) — these
  are the helpers that move to txcore.

### Phase 1 — Output + OutputBuilder + library loader

Extract a minimal `Output` (raw bytes + accessors) and the new
tuple-based `OutputBuilder` into `ledger/txcore/output.go`.

Pretty-printer / debugging methods stay in the full package as
methods on the **same** `Output` type via separate `.go` files.

Aliasing approach: `ledger.Output = txcore.Output`. Methods declared
in the full `ledger` package are only available in the full build —
TinyGo never compiles them. (Same trick the easyfl facade uses.)

Stand up `ledger/txcore/library.go` with a thin wrapper that holds
the parsed `*engine.Library` and offers `CompileExpression` /
`DecompileBytecode`.

### Phase 2 — TxBuilder + transactionData

Move the compose-side TxBuilder methods into
`ledger/txcore/txbuilder.go`. Leave behind in
`ledger/txbuilder/txbuilder.go` a thin wrapper that re-exports
`txcore.TxBuilder` and adds `BuildTransactionWithValidation`,
`Transaction()`, `BytesWithValidation()`, `LoadInput`,
`GetChainAccount` (validation / state-query helpers).

Sequencer-only helpers (`CalcFrozenCoverageDelta`,
`MustPutFrozenCoverage`) stay in the full `ledger/txbuilder` or move
to the sequencer package.

`proxi` CLI continues to import `ledger/txbuilder` and gets the
full-build wrapper unchanged.

### Phase 3 — Sign / hash port

Port `TxIDFromTransactionDataTree` into `txcore.TxIDFromBytes` (no
validation, just hash + prefix). Implement `SignED25519`. Delete the
duplication from `transaction/parse.go` if the full-build path can
use the txcore helper directly.

### Phase 4 — Compose-helpers move

Inventory current `ledger/<foo>.go` constraint wrappers; for each
extract the compose-only half — `New<Foo>` constructors that emit
canonical source via the library — into `ledger/txcore/helpers.go`
(or per-kind files). The parser + registrar stay in the full
package; the full package's `New<Foo>` re-exports the txcore version
so callers don't change.

Risk: some constraint wrappers reach into types from the full
package (e.g. `Foundry` referencing `mustBinFromSource` which calls
`L(base.MaxSlot)`). Solvable by exposing a `Library` accessor on the
txcore side and wiring it once at init.

### Phase 5 — WASM entrypoint + measurement

Add `ledger/txcore/wasm/main.go` with a JS-callable "build and sign"
function. TinyGo-build it. Measure binary size.

**Status: shipped 2026-05-19.** Probe at
`ledger/txcore/wasm/main.go` exercises the compose + sign path
without invoking the library (raw lock-bytecode placeholder) — the
FLOOR measurement for "what does the wasm txbuilder cost".

Build:

```
tinygo build -target=wasm -o /tmp/txcore.wasm ./ledger/txcore/wasm/
```

**Measured size (TinyGo 0.41.1, Go 1.26):**

| Format | Size |
|---|---|
| Raw wasm | 1.8 MB |
| gzip -9 | 563 KB |

Two TinyGo-blockers had to be cleared before the build went green:

- `ledger/base/tx_signature.go` used `unitrie/common.Concat` for a
  trivial 1+N byte concat. `unitrie/common` transitively pulls
  `stretchr/testify/require` (testify is referenced in unitrie's
  production code), which in turn pulls `net/http`. TinyGo 0.41 on
  Go 1.26 fails to compile `net/http/roundtrip_js.go`. Replaced the
  Concat call with an inline `append` chain — drops the entire
  testify→net/http pull from the wasm path.
- `ledger/base/*.go` used `proxima/util.Assertf` / `AssertNoError`.
  `proxima/util` drags `golang.org/x/text/message` + `language`
  (used by `util.Th` for thousand-separator number formatting),
  adding ~85 KB of locale tables. Switched base to
  `easyfl_util.Assertf` / `AssertNoError` (same semantics, no
  dependencies). `proxima/util` is still pulled into base via
  `smallkv.KeysSorted` and `ledger_time.Maximum` — Phase 6 may
  inline these if size budget demands further trimming.

**Size breakdown (top contributors, raw bytes):**

```
fmt                17946     ← TinyGo's fmt impl
internal/reflectlite 14805
runtime            15282
internal/strconv   11024     ← BE int formatting
golang.org/x/text/internal/language  24585 (+ ~60K data)
ledger/txcore      6677
easyfl/tuples      5551
easyfl/blake2b     4919
math/rand (data)    4856     ← from time.Now() seed
crypto/ed25519 + chain      ~4K
ledger/base         659
easyfl_util         656
```

x/text is the single largest non-stdlib drag (~85 KB combined).
`fmt` + `reflectlite` + `strconv` + `runtime` are the irreducible
TinyGo floor (~58 KB). The rest is our code (~25 KB).

Wallet path verified end-to-end: TxBuilder ops compile, hash +
ed25519 signing run, output bytes serialize. No host call-out
needed.

### Phase 6 — Size optimisation

**Status: partly shipped 2026-05-19.** The x/text drag — the largest
non-stdlib contributor — was eliminated by dropping `proxima/util`
from `ledger/base`.

**Shipped:**

- **`proxima/util` removed from `ledger/base`.** Inlined `KeysSorted`
  (3 call sites in `smallkv.go` → one local `sortedKeys` helper)
  and `Maximum` (1 call site in `ledger_time.go` → inlined max
  loop). `util.Assertf` / `AssertNoError` were already replaced
  with `easyfl_util` equivalents in Phase 5.
- **`util/lines` DCE'd from the binary.** Still in the import graph
  (because `SmallPersistentMap.Lines()` is exported), but TinyGo's
  linker drops it because nothing reachable from the wallet probe
  calls Lines(). No code change needed.

**Measured (TinyGo 0.41.1, Go 1.26), after Phase 6:**

| | Phase 5 (before) | Phase 6 (after) | Saved |
|---|---|---|---|
| Raw wasm | 1.8 MB | **1.3 MB** | ~500 KB |
| gzip -9 | 563 KB | **429 KB** | ~134 KB |

Updated size breakdown (top contributors, raw bytes):

```
fmt                17689     (irreducible without rewriting compose-path error handling)
runtime            16084
internal/reflectlite 14726
internal/strconv   10749     (+ 13K data — used by fmt)
crypto/internal/fips140/*  ~18K combined (used by ed25519)
syscall/js          5462
slices              5531
easyfl/tuples       5700
ledger/txcore       6387
easyfl/blake2b      4919
math/rand           4506     (+ 4856 data, used by ed25519 random source)
ledger/base         2914
easyfl_util          656
```

x/text is GONE. The remaining heavy hitters are all stdlib /
TinyGo-runtime irreducibles: `fmt`, `reflectlite`, `strconv`,
`runtime`, the FIPS crypto chain (ed25519), `math/rand`.

**Remaining levers (deferred, by descending payoff):**

- **fmt stripping.** Most `fmt.Errorf` / `fmt.Sprintf` calls in
  base + txcore could be replaced with `errors.New` + manual
  concatenation, dropping fmt + fmtsort + reflectlite (~34 KB
  raw, ~12 KB gzipped). Significant churn (~30 call sites). Skip
  unless wallet bundle size pressure is acute.
- **`encoding/hex` replacement.** ~671 B; small.
- **`math/rand` removal.** The ed25519 signer takes an `io.Reader`
  for randomness; ed25519 ignores it for deterministic signing.
  Pass `nil` (or a stub) instead of `rand.Reader`. Saves ~9 KB
  combined (math/rand + its data segment).

### Phase 7 — Companion API on the host

Implement the unified `/api/v1/submit` endpoint described in the
goal section: `tx` always required, `consumed_utxos` optional for
full-context validation, `validate_only` optional to skip the actual
submit. Independent of the wasm-core phases — can land in parallel.

---

## Open questions

1. **Library JSON shape in the wasm bundle.** Full extended library
   (matches host hash byte-for-byte) vs slimmed library. Default to
   full for v1.

2. **TinyGo `crypto/ed25519` reality check.** Confirm signing works
   end-to-end in TinyGo wasm. Same for `golang.org/x/crypto/blake2b`
   (asm fast path unavailable; pure-Go fallback must compile).

3. **`util/lines` and `proxima/util` cleanup.** Several constraint
   wrappers call `util.Assertf`. Simplest: replace with
   `if !cond { panic(msg) }` in compose-only code.

4. **`unitrie/common.Concat`.** Used at exactly one compose-side
   site (`SignED25519`). Replace with a local `append` chain — no
   reason to pull in unitrie for byte concat.

5. **Output identity.** `ledger.Output` keeps its current shape;
   txcore exports the same struct, and the alias `ledger.Output =
   txcore.Output` keeps existing callers untouched. Confirm during
   Phase 1 that no embedded interface methods (`String`, `Lines*`)
   are referenced from compose paths today.

6. **Sequencer paths in `Output`.** Confirm during Phase 0 that no
   non-sequencer compose path reaches `seqdata.SequencerData`.

7. **Tests in the wasm path.** TinyGo's test runner is limited.
   Keep using the standard Go toolchain to test `txcore` (it's pure
   Go, just constrained). A small handful of JS-integration tests
   in headless Chrome cover the wasm boundary.

8. **Versioning / library hash.** A wasm bundle is pinned to one
   extended-library version. Wallet UI detects host upgrade and
   prompts user to update. Out of scope for this refactor.

---

## Cross-references

- [wasm_easyfl.md](wasm_easyfl.md) — the easyfl-side restructure
  this depends on. **Status: shipped 2026-05-19 as `c2f3713`.**
- `claude/native_token.md` — refers to `token`/`tokenAmount` as Go
  builtins; on the wasm side these become emitter-only (wallet
  pushes a `token(tag, 0xFF)` bytecode but never evaluates it).
- `CLAUDE.md` working rules — "Enforce constraints in EasyFL when
  possible" still applies; txcore is a packaging refactor, not a
  behavioural one.
