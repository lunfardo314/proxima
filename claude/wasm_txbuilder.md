# WASM transaction-builder core — analysis and spec

Status: **design / analysis, no implementation**. Started 2026-05-18.

Companion document: `easyfl/claude/tinygo_wasm.md` covers the easyfl
side of the same refactor; this document covers Proxima.

---

## Goal

Expose a minimal Go package — `ledger/txcore` — that can be compiled
with TinyGo to WebAssembly and run inside a browser-based wallet (or
any isolated frontend). The frontend's job is to:

1. Build a transaction from inputs + outputs + endorsements.
2. Serialize it to the on-the-wire byte form.
3. Sign it (ed25519 over the tx ID).

The frontend does **not** validate the transaction locally. Validation
is left to the host backend, reached through the REST API.

Out of scope for the wasm core (deliberately not a priority):

- Sequencer-specific compose operations. A wallet does not issue
  sequencer transactions, does not need to populate
  `SequencerDataBytes`, and does not need `MakeSequencerTransaction`
  or related helpers. Eliminating this path removes the
  `proxima/sequencer/seqdata` import from `Output` entirely (see Layer
  B below).

Practical constraint: today's `ledger/txbuilder` package transitively
pulls in roughly everything in `proxima/ledger` plus
`proxima/sequencer/seqdata`, `proxima/util/lines`,
`proxima/ledger/multistate`, `proxima/ledger/transaction`, and
`unitrie/common`. Compiling that to wasm via TinyGo would produce a
binary far larger than is acceptable for a wallet, and several of
those packages are not even TinyGo-compatible.

Non-goal: rewriting the wallet UX, defining a new wire format, or
shipping an actual wasm binary. Those follow the refactor.

### Companion: dry-run validation API on the host

The wallet is "compose+sign only", but users still want pre-submission
feedback ("will this transaction validate?"). This is a host-side
concern, not a wasm-core concern, but it pairs naturally with the
refactor so it's worth recording:

Provide an HTTP endpoint that accepts a raw signed transaction PLUS
the bytes of each consumed UTXO (the "full-context inputs"), runs the
standard Stage-3 validator, and returns the verdict without side
effects. This is the same flow `BuildTransactionWithValidation` runs
today, lifted behind an API. The wallet POSTs to it before the real
submit endpoint to surface errors early.

Sketch:

```
POST /api/v1/validate_dry_run
{
  "tx":            "<hex>",                  // raw tx bytes
  "consumed_utxos": ["<hex>", "<hex>", ...]  // one per consumed input
}
->
{ "ok": true }                  // success
{ "ok": false, "error": "..." } // validator error
```

The host already has every validator component; this is just a
read-only thin wrapper around `transaction.Parse + SetFullContext +
ValidateFullContext`, with the consumed UTXOs provided by the caller
instead of looked up in state. Treat it as the natural follow-on
deliverable for the refactor, not a blocker.

---

## Frontend use cases driving scope

From the existing CLI consumers in `proxi/node_cmd/`:

- `send` (PRXI transfer) — `New()` → `ConsumeOutput` × N →
  `ProduceOutput(SigLock-output)` → optional tag-along →
  `SignED25519` → bytes.
- `send_tagged` (native-token transfer) — same plus
  `tokenAmount(...)` extras on the produced output and a
  `token(tag, 0xFF)` pure-conservation declaration.
- `foundry create / mint / burn / retire` — chain origin + transit
  patterns using `MakeFoundryOriginOutput`, `TransitFoundry`,
  `FinishChainUnlockParams`.
- `delegate amount / chain` — delegation lock outputs.
- `killchain` — `MakeEndChainTransaction`.
- `fund` — multi-input PRXI consolidation.

Every one of these is **compose + sign**; none requires validation
locally. They differ only in which constraint kinds they assemble.

Use cases that are explicitly **not** wasm targets:

- Sequencer milestones, branch transactions, sequencer freeze
  (delegation-freeze) transactions. These come from the running
  sequencer process, not from a wallet.

---

## Two layers of cost in the current stack

### Layer A — TxBuilder shape

`ledger/txbuilder` itself is ~1300 LOC and structurally compose-side.
Heavy pieces are confined to a handful of methods:

| Method | Purpose | In wasm core? |
|---|---|---|
| `New` / `PushTxConstraint` / `Push*` / `ConsumeOutput` / `ProduceOutput` / `PutUnlockParams` / `PutSignatureUnlock` / `PutUnlockReference` | builder ops | YES |
| `transactionData.ToTuple` / `Bytes` | serialise tx essence | YES |
| `SignED25519` | sign over tx ID | YES (needs blake2b + ed25519) |
| `Transaction()` / `BuildTransactionWithValidation()` / `BytesWithValidation()` | round-trip parse + Stage-3 validate | **NO** — full-build only |
| `GetChainAccount(...IndexedStateReader...)` | state-query helper | **NO** — wallet uses host API |
| `LoadInput` | feeds the validator | **NO** |
| `CalcFrozenCoverageDelta` / `MustPutFrozenCoverage` | sequencer freeze-tx convenience | **NO** — sequencer-only |

Frozen coverage in particular is the sequencer's accounting, not the
wallet's: when a delegation UTXO freezes, the *sequencer* that owns
the chain runs the math and emits the freeze tx. A delegating wallet
just creates `delegateLock` outputs; the coverage rollup is somebody
else's job.

So txbuilder is mostly already shaped right. The wasm core takes a
strict subset: builder ops + tuple serialise + sign. Validation,
state-query, and sequencer-only helpers stay in the full build.

Tuple thread-safety: `easyfl/tuples`' lazy-subtree deserialization
uses `sync.RWMutex`. Inside the wasm core, transactions are built and
serialized single-threadedly — no readers can race writers, so the
lock is dead weight. We want either a build-tag variant that strips
the mutex entirely or confirmation that TinyGo's no-op
`sync.RWMutex` is inlined to zero. This is a code-size concern, not a
correctness one. Aligns with `easyfl/claude/tinygo_wasm.md` R1.

### Layer B — what `ledger.Output` drags in

`ledger.Output` lives in `ledger/output.go` (1205 LOC) and is needed
on the compose side too — every `ConsumeOutput` / `ProduceOutput`
takes one. But `output.go` and its companions transitively import:

- `proxima/sequencer/seqdata` (sequencer wire format) — **droppable
  once sequencer compose paths leave the wasm scope.**
- `proxima/util/lines` (pretty-print helpers; transitively pulls more)
- `proxima/util` (assertions, errors, hex tools — mixed, some clean)
- `proxima/util/testutil` (only at lib init time — fine)
- `proxima/ledger/multistate` (only via the optional state-query
  helpers; not needed at compose time)
- the constraint serde wrappers (`chain.go`, `lock_*.go`, `foundry.go`,
  `native_token.go`, `delegate*.go`, …) — each adds compile-time deps
  on `easyfl` and `easyfl/easyfl_util` + helpers. Whether wasm needs
  these at all is the central question of the next section.

The minimal compose-side surface for `Output` is:

- The `Output` tuple (raw bytes + accessors used by builders).
- The `OutputBuilder` (the `Clone(func(*OutputBuilder){...})` pattern).
- `HashOutputs(...)` (blake2b over each output's bytes, concatenated).
- Path constants (`ConstraintIndexLock`, `ConstraintIndexChain`,
  `ConstraintIndexAmounts`, `ConstraintIndexFoundry`,
  `ConstraintIndexFoundryPolicy`, `ConstraintIndexDelegationParams`).
- `base.OutputID`, `base.TransactionID`, `base.ChainID`,
  `base.LedgerTime`, `base.HolderID`, `base.MakeOriginChainID`,
  `base.SignatureTypeED25519`, etc. — `ledger/base` is small and
  already TinyGo-friendly modulo our own utilities.

Pretty-printer / debugging accessors (`String`, `LinesPlainSource`,
`LinesShort`, `_runOutputs`, etc.) stay in the full package.

---

## Architectural pivot: compile-from-source as the primary path

The first draft of this spec proposed porting every typed constraint
wrapper (`Foundry`, `TokenAmount`, `SigLock`, `ChainConstraint`,
`DelegateLock`, …) into the wasm core as convenience emitters. That
is more than the wallet needs.

**Fundamental observation:** a wallet *fundamentally* needs only
"compile this EasyFL source expression with the loaded library, get
bytes back". Every typed wrapper today is sugar over

```go
mustBinFromSource(fmt.Sprintf("<symbol>(<arg0>, <arg1>, ...)"))
```

— a Go-readable façade around what is, at the bytecode layer, just a
call to `easyfl.Library.CompileExpression(source)`. Decoding works
the same way: pure EasyFL decompile already turns bytecode back into
source, no Go-side serde wrappers required for inspection.

This means the wasm core can be **two layers**, not one:

### Layer 1 (mandatory) — bare compile/decompile core

```
ledger/txcore/
├── output.go                — minimal Output + OutputBuilder
├── tx_data.go               — transactionData + ToTuple + Bytes
├── txbuilder.go             — builder ops (compose only)
├── sign.go                  — SignED25519 + TxIDFromBytes
└── library/                 — TinyGo-clean library loader
    ├── library.go           — Library wrapper (JSON load + compile)
    └── library.json         — embedded definitions (host-canonical)
```

This layer:

- knows nothing about typed constraints,
- emits constraint bytes by taking source strings the caller hands it
  (e.g. `sigLock(0xabcd...)`) and compiling against the loaded
  library,
- exposes `Library.Decompile(bytecode) → source` for inspection,
- is what a strictly-minimal wallet would import.

### Layer 2 (optional) — globally-known constraints

```
ledger/txcore/constraints/
├── amounts.go               — NewAmounts(amount, inflation, frozen)
├── chain.go                 — NewChainConstraint / NewChainOrigin
├── lock_signature.go        — NewSigLock + ED25519-key derivation
├── lock_tag_along.go        — NewTagAlongOutput
├── lock_chain.go            — NewChainLock
├── lock_delegate.go         — NewDelegateLock + DelegationParams
├── foundry.go               — NewFoundry + foundry-policy bytecodes
├── native_token.go          — NewTokenAmount + token() bytecode emit
└── unlock_params.go         — NewChainUnlockParams, FinishChainUnlockParams
```

Pure syntactic sugar on top of Layer 1: each `New<Foo>` produces a
canonical source string and calls into the Layer-1 compiler. No
parser, no `register<Foo>`, no validation. A wallet UI that wants
type-safe builders pulls Layer 2 in; a thin wallet that's content to
hand-write source strings does not.

The full `ledger` package keeps the **parsers / registrars / EasyFL
bodies / validation** — that side is unchanged. Layer 2 sources are
the same canonical strings the parsers expect, so emitters and
parsers stay byte-for-byte compatible.

Net effect on the wasm binary: Layer 1 is small (compiler + tuple
serialise + sign + library loader); Layer 2 adds ~one short file per
constraint kind, no transitive deps. The wallet picks how much to
include.

### Decompile for the inspection path

When a wallet UI needs to render "what does this UTXO actually do?"
it doesn't need typed `Foundry` / `TokenAmount` wrappers — it can
call `library.Decompile(constraintBytes) → "foundry(z64/1000)"` and
display the source. Layer 1 already supports this via the EasyFL
decompile path. Layer 2 wrappers exist for *writers*, not readers.

---

## Proposed package layout (Proxima side)

```
ledger/                          (kept — full backend / validator,
│                                 includes parsers + serdes +
│                                 EasyFL bodies + validation)
├── (everything as today)
│
└── txcore/                      NEW — TinyGo-clean compose+sign core
    ├── (Layer 1: see above)
    └── constraints/             OPTIONAL — Layer 2 emitter sugar
```

The full `ledger` package's existing `New<Foo>` constructors delegate
to `txcore/constraints/<foo>.go` so there is **one source of truth**
for each constraint's canonical bytecode source string. The full
package retains its parsers, serdes, and EasyFL bodies; nothing in
the runtime validator changes.

Rejected alternative: keep these inside `ledger/` with `//go:build
!heavy` tags. Sub-package factoring matches the easyfl side and
the dependency direction is easier to reason about.

---

## EasyFL coordination (companion doc)

The wasm core depends on easyfl reaching its TinyGo-clean state. Most
of that is already planned in `easyfl/claude/tinygo_wasm.md`. Status
update from this side:

| `tinygo_wasm.md` item | Today |
|---|---|
| YAML serde dropped from core | **DONE.** Easyfl shipped JSON persistence; `easyfl/library_yaml.go` is gone, `library.json` is the canonical asset. |
| Crypto embedded fns moved out of core | **DONE 2026-05-18.** `blake2b` / `validSignatureED25519` no longer live in `easyfl/library_embed.go`; they're registered by Proxima at `ledger/crypto_builtins.go`. For wasm the wallet doesn't need them either — `blake2b` we call directly from Go, ed25519 only for signing. |
| `reflect` removed from core | open |
| `slicepool` build-tag split | open |
| `easyfl/serde` sub-package (JSON serde + LibraryHash) | open — what's left of "serde tools" after the YAML drop is the JSON loader and the library hash. The wasm core only needs JSON load (no hash check against a stored value if we trust the embedded asset). |
| `fmt` (R2 binary-size risk) | open — measure once first wasm binary is buildable |
| `tuples` thread-safety | open — wasm wants the `sync.RWMutex` in `tuples/tree.go` stripped or build-tag-replaced; lazy-subtree deserialization is single-threaded under wasm. |

What the wasm core needs from easyfl:

- Library construction from JSON (read-only after init).
- Source compiler: source → bytecode.
- Source **decompiler**: bytecode → source (for inspection in the
  wallet UI).
- Symbol-prefix lookup (`FunctionCallPrefixByName`) — needed by tx-
  level constraint composition.
- The `tuples` sub-package (tuple builder + serialize), ideally with
  thread-safety stripped.
- `easyfl_util` (`Uint64FromBytes`, `Concat`, etc.).

It does **not** need:

- The evaluator (`eval.go`).
- The slicepool (transactions are short-lived; allocation churn is
  fine).
- Embedded function dispatch.

So the wasm core is a "compose+inspect-only" subset of the easyfl
TinyGo subset — even tighter than what `tinygo_wasm.md` envisions for
the standalone easyfl wasm build.

---

## Library-loading model

The wasm binary embeds the canonical compiled `library.json` (proxima's
extended library, not just easyfl base). This is the file the
proxima-side upgrade chain produces today via
`LibraryJSONFromParameters`.

At wasm-init time:

1. Load that JSON into the easyfl `Library` (compiler-only path; no
   evaluator wiring needed).
2. **No constraint serdes to register on the wasm side** — that's the
   pivot of the Layer-1/Layer-2 split. Layer 2 (optional) emitters
   produce canonical source strings without any registration, since
   they don't parse incoming bytecode.

No host call-out is needed; the library is constant within a wallet
version. When Proxima upgrades the extended library, the wallet ships
a new wasm bundle.

Open: do we embed the **full** Proxima library JSON (extended), or a
**slimmed** one with only the bytecode and metadata the wasm path
actually compiles against? The full one is the safer default for v1
(matches the host's library hash exactly, so the wallet can include
it in submission metadata and the host can refuse mismatches);
slimming can come later.

---

## Signing surface

```
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
TinyGo-compatible (blake2b loses its asm fast path; acceptable). No
host call-out.

`TxIDFromTransactionDataTree` itself lives in
`ledger/transaction/parse.go` today. The wasm core ports a stripped
version of it (no parse validation, just hash + prefix bytes) — call
it `TxIDFromBytes` and keep it tight. If the full-build path can use
the txcore helper directly, delete the duplication from
`transaction/parse.go`.

---

## What the wallet API looks like

### Layer 1 (bare compile-from-source)

```go
import "github.com/lunfardo314/proxima/ledger/txcore"

lib := txcore.LoadEmbeddedLibrary()

txb := txcore.New(lib)
for i, oid := range inputIDs {
    txb.ConsumeOutput(consumedOutputs[i], oid)
}
sigLockSrc := fmt.Sprintf("sigLock(0x%s)", hex.EncodeToString(recipient[:]))
sigLockBin, _ := lib.CompileExpression(sigLockSrc)
out := txcore.NewOutput(func(o *txcore.OutputBuilder) {
    o.WithAmounts(amount).PutConstraint(sigLockBin, txcore.ConstraintIndexLock)
})
txb.ProduceOutput(out)
txb.TransactionData.Timestamp = ts
txb.TransactionData.InputCommitment = txcore.HashOutputs(txb.ConsumedOutputs...)
txb.SignED25519(privKey)
rawTx := txb.TransactionData.Bytes() // -> POST to backend
```

### Layer 2 (typed-constraint sugar, optional)

```go
import (
    "github.com/lunfardo314/proxima/ledger/txcore"
    "github.com/lunfardo314/proxima/ledger/txcore/constraints"
)

lib := txcore.LoadEmbeddedLibrary()
txb := txcore.New(lib)
for i, oid := range inputIDs {
    txb.ConsumeOutput(consumedOutputs[i], oid)
}
txb.ProduceOutput(constraints.NewSigLockOutput(amount, recipient))
txb.TransactionData.Timestamp = ts
txb.TransactionData.InputCommitment = txcore.HashOutputs(txb.ConsumedOutputs...)
txb.SignED25519(privKey)
rawTx := txb.TransactionData.Bytes()
```

The wasm export wraps one of these as a single JS-callable function
that takes JSON-shaped input (inputs, outputs, ts, optional
endorsements, private key bytes) and returns the raw tx bytes. The
wallet UI never sees the Go API directly.

---

## What stays in `ledger` (full build, server-side)

- All EasyFL bodies (`def/*.easyfl`).
- The evaluator, the constraint validators, the closing balance
  checks (e.g. `NativeTokenAggregator.CheckBalances`,
  `validateOutputs`).
- The typed-constraint **parsers** (`<Foo>FromBytes`) and their
  `register<Foo>` calls. Layer 2 of the wasm core only emits; the
  full-build side keeps the reading half.
- All sequencer-related compose helpers
  (`MakeSequencerTransaction`, `CalcFrozenCoverageDelta`,
  `MustPutFrozenCoverage`, …) — used by the running sequencer
  process, never by a wallet.
- `multistate`, snapshots, the persistent index machinery.
- `proxi` CLI keeps using the **full** `txbuilder`, because for the
  CLI a 5 MB binary that can also build+validate is fine, and we want
  the CLI to catch builder bugs locally before submission.

Sub-question: should `proxi` switch to `txcore` too, so the CLI is
the canary for the wasm core? Arguments for: bug-for-bug parity
between CLI and wallet. Argument against: the CLI today builds and
**also** parses/round-trips the tx via `BytesWithValidation` to
surface errors at compose time — that's a useful safety net that the
wallet doesn't have (it can rely on the API to reject malformed txs,
or the dry-run-validate endpoint described above). Tentative answer:
leave `proxi` on the full builder for v1, revisit once the wasm core
has settled.

---

## Phase plan

Strict ordering — each phase must build green before the next starts.

### Phase 0 — Audit verification

- Build the current `ledger/txbuilder` import graph (`go list -deps`)
  and confirm the high-cost imports we expect (`multistate`,
  `transaction`, `sequencer/seqdata`, `util/lines`).
- Build the import graph from the proxi CLI commands to confirm
  which `txbuilder` methods they actually call (the table above is
  the working set; this phase verifies it).
- Confirm that no non-sequencer compose path touches
  `sequencer/seqdata` or frozen-coverage helpers. If anything is
  surprising here, the scope shrinks differently than predicted.

### Phase 1 — Layer 1 skeleton: Output + OutputBuilder + library

Extract a minimal `Output` (and `OutputBuilder`) into
`ledger/txcore/output.go`. Drop the pretty-printer / debugging
accessors — those stay in the full package as methods on the **same**
`Output` type via separate `.go` files.

Aliasing approach: `ledger.Output = txcore.Output`. Methods declared
in the full `ledger` package are only available in the full build —
TinyGo never compiles them, so they don't pollute the wasm binary.

In the same phase, stand up `ledger/txcore/library/` with a thin
wrapper that loads `library.json` and exposes compile / decompile /
prefix-lookup. No evaluator, no constraint registrars.

### Phase 2 — Layer 1 builder: TxBuilder + transactionData

Move the compose-side TxBuilder methods into
`ledger/txcore/txbuilder.go`. Leave behind in
`ledger/txbuilder/txbuilder.go` a thin wrapper that re-exports
`txcore.TxBuilder` and adds `BuildTransactionWithValidation`,
`Transaction()`, `BytesWithValidation()`, `LoadInput`,
`GetChainAccount` (the validation / state-query helpers).
Sequencer-only helpers (`CalcFrozenCoverageDelta`,
`MustPutFrozenCoverage`) stay where they are or migrate to the
sequencer package — they were never on the wallet path.

`proxi` CLI continues to import `ledger/txbuilder` and gets the
full-build wrapper unchanged.

### Phase 3 — Sign / hash port

Port `TxIDFromTransactionDataTree` into `txcore.TxIDFromBytes` (no
validation, just the hash + prefix-byte logic). Implement
`SignED25519` against it. Delete the duplication from
`transaction/parse.go` if the full-build path can use the txcore
helper directly.

### Phase 4 — Layer 2 emitters

Move the pure "bytecode emitter" parts of the typed-constraint
wrappers into `ledger/txcore/constraints/`. Each constraint wrapper
today has roughly:

- a `New<Foo>(...)` constructor,
- a `Source()` string method,
- a `Bytes()` method (= `mustBinFromSource(Source())`),
- a parser `<Foo>FromBytes`,
- a registrar `register<Foo>`.

Of these, the constructor / Source / Bytes are compose-only and
TinyGo-clean (assuming easyfl is). The parser + registrar stay in
the full `ledger` package. The full package's `New<Foo>` re-exports
the txcore one, so callers don't change.

Risk: some constraint wrappers reach into types from the full
package (e.g. `Foundry` referencing `mustBinFromSource` which uses
`L(base.MaxSlot)`). That's solvable by exposing a `Library`
accessor on the txcore side and wiring it once at init.

### Phase 5 — WASM entrypoint

Add `ledger/txcore/wasm/` with a `main()` exporting a JS-callable
"build and sign" function. TinyGo-build it. Measure binary size.
This is the gating phase: if the binary is too large, Phase 6 starts
on `fmt`/Trace stripping. If it's OK, ship it.

### Phase 6 — `fmt` / Trace stripping (deferred)

Only if Phase 5 measurements demand it. Likely candidates:
`fmt.Errorf` → static strings on hot paths; Trace calls gated by a
build tag.

### Phase 7 (host-side) — `/validate_dry_run` endpoint

Wallet-side ergonomics: implement the dry-run validation endpoint
described in the Goal section. Independent of the wasm-core phases —
can land in parallel.

---

## Open questions

1. **Library JSON shape in the wasm bundle.** Full extended library
   (matches host hash byte-for-byte) vs. slimmed library (smaller
   bundle, but a separate codepath that must be kept in sync). Pick
   full for v1.

2. **TinyGo `crypto/ed25519` reality check.** Confirm signing works
   end-to-end in TinyGo wasm. Same for `golang.org/x/crypto/blake2b`
   (asm fast path is unavailable; pure-Go fallback must compile).

3. **`util/lines` and `proxima/util` cleanup.** Several constraint
   wrappers call `util.Assertf`. Move the assertion shim into
   `easyfl_util` or duplicate a minimal `Assertf` inside `txcore`?
   Simpler: replace `util.Assertf` with `if !cond { panic(msg) }`
   inside compose-only code.

4. **`unitrie/common.Concat`.** Used at exactly one compose-side
   site (`SignED25519`). Replace with a local `append` chain — no
   reason to pull in unitrie for byte concat.

5. **Tuple thread-safety in wasm.** Either strip the
   `sync.RWMutex` in `easyfl/tuples/tree.go` behind a build tag, or
   accept that TinyGo's no-op locks are good enough. Measure during
   Phase 5.

6. **Sequencer paths in `Output`.** Confirm during Phase 0 that no
   non-sequencer compose path reaches `seqdata.SequencerData`. If
   anything does, decide whether to move it or build-tag-gate it.

7. **Tests in the wasm path.** TinyGo's test runner is limited. The
   easiest path is to keep using the standard Go toolchain to test
   `txcore` (since it's pure Go, just constrained), and only build —
   not test — under TinyGo. A small handful of JS-integration tests
   run in headless Chrome would cover the wasm boundary.

8. **Versioning / library hash.** A wasm bundle is pinned to one
   extended-library version. How does the wallet detect a host
   upgrade and prompt the user to update? Two options:
   - The wasm core exposes the library hash it was built with; the
     wallet UI fetches the current host hash and compares.
   - Submissions carry the wallet's library hash; the host rejects
     mismatches with a clear error.
   Out of scope for this refactor — pick during wallet integration.

9. **Layer 1 vs Layer 2 default.** What does the "official"
   reference wallet import? Layer 2 (typed sugar) is more ergonomic
   but adds emitter code per constraint kind. Default to Layer 2 in
   v1, drop down to Layer 1 only if the binary-size budget forces
   it.

---

## Cross-references

- `easyfl/claude/tinygo_wasm.md` — easyfl's side of the same refactor.
  Several of its open items are tighter now (YAML serde dropped,
  crypto moved out); the remaining work is `reflect` removal,
  slicepool build-tag split, and tuple-tree thread-safety.
- `claude/native_token.md` — refers to `token`/`tokenAmount` as Go
  builtins; these end up emitter-only on the wasm side (the wallet
  pushes a `token(tag, 0xFF)` bytecode but never evaluates it).
- `CLAUDE.md` working rules — "Enforce constraints in EasyFL when
  possible" still applies; the txcore split is a packaging refactor,
  not a behavioural one.
