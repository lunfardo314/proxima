# EasyFL — Compose-Path Extraction for WASM (audit + plan)

Sibling document: [wasm_txbuilder.md](wasm_txbuilder.md) covers the
Proxima-side use of this work (the `ledger/txcore` wallet API). This
document covers everything inside `easyfl` itself that needs to change
for that.

Started as `easyfl/claude/tinygo_wasm.md`; moved here on 2026-05-18 to
keep the whole multi-repo refactor planned from Proxima (the only
consumer driving it).

**Rewritten 2026-05-19** under tightened design constraints (see
`memory/feedback_wasm_refactor_constraints.md`):

1. **No build tags.** One source per file.
2. **Backward compatible.** Existing Proxima imports of
   `github.com/lunfardo314/easyfl` must keep compiling.
3. **Slicepool and tuples-tree mutex stay untouched** — left in place,
   accept TinyGo's no-op semantics.
4. **Embedded function bodies must be isolated** out of the compose
   path so they (and the `fmt` they drag in) are not pulled into the
   WASM binary.
5. **JSON serde + blake2b live outside the minimal compose package.**
   Wallets parse JSON in their own environment and supply already-
   parsed descriptors.
6. **Tracing must be revisited** as part of stripping `fmt` from the
   eval-path Go code that wallet builds will inadvertently pull in
   through the EmbeddedFunction signature.

---

## Goal

Extract a minimal **compose-only** Go sub-package — `easyfl/core` —
that contains everything the wasm wallet needs (source compile, source
decompile, library construction from parsed descriptors, tuple
primitives) and **nothing else**. In particular it must not:

- import `encoding/json`, `golang.org/x/crypto/blake2b`, or
  `slicepool`;
- reference any embedded function body (`evalConcat`, `evalSlice`, …),
  so LLVM DCE drops them all from the WASM binary;
- pull in `fmt` for anything other than `errors.New`-grade error
  construction (no `fmt.Sprintf` on hot paths).

Existing `easyfl` package keeps every public symbol — via type aliases
and re-exports of `easyfl/core` — so the Proxima backend keeps
compiling unchanged.

## Size-budget rationale

Phase-A measurement is pending, but the structural reasoning:

- Slicepool + `sync.Pool` + `sync.RWMutex` are noise (~few KB).
- The **real** WASM-size drivers, if left reachable, are:
  - The 30+ embedded function bodies kept alive by map-based dispatch
    in `unboundEmbeddedFunctions[T]()`. They reach `par.Trace`,
    `par.TracePanic`, `easyfl_util.FmtLazy`, ⇒ `fmt`.
  - `encoding/json` (`serde_json.go`).
  - `golang.org/x/crypto/blake2b` (`serde_tools.go`, for `LibraryHash`).
- This refactor's job is to make sure the wasm import does not reach
  any of them.

## Target package layout

```
easyfl/
├── core/                       NEW — minimal compose-only sub-package
│   ├── library.go              Library type, register / lookup
│   ├── compiler.go             source → bytecode
│   ├── decompiler.go           bytecode → source (extracted from compiler.go)
│   ├── types.go                Expression, funDescriptor, EmbeddedFunction
│   ├── recursion.go
│   ├── local_script.go         compose half (Compile, FromBytes, Function)
│   ├── descriptor.go           FunDescriptor struct + Register API
│   └── errors.go               static error messages, no fmt
│
├── embed/                      NEW — embedded function bodies
│   ├── base.go                 evalConcat, evalSlice, evalByte, …
│   ├── arithmetic.go           evalAddUint, evalSubUint, …
│   ├── bitwise.go              evalBitwiseAND, …
│   ├── tuples.go               evalAtTuple8, evalNumElementsOfTuple
│   ├── parse.go                evalParseBytecode, evalParseInlineData, …
│   └── registry.go             DefaultRegistry / RegisterBase
│
├── slicepool/                  UNCHANGED
├── tuples/                     UNCHANGED
├── easyfl_util/                UNCHANGED
│
├── library.go                  thin facade — NewBaseLibrary wires core+embed
├── library_json.go             go:embed library.json (lives with backend)
├── serde_json.go               JSON library load/save (backend-only)
├── serde_tools.go              LibraryHash / ValidateCompiled (backend-only)
├── eval.go                     CallParams, EvalExpression, ... (eval engine)
├── trace.go                    GlobalData wrappers (trace.go is revisited; see below)
└── local_script_eval.go        LocalScript.Eval / EvalInPool methods
```

The wasm wallet does `import "github.com/lunfardo314/easyfl/core"`.
That is the only easyfl import it ever needs. Proxima backend does
`import "github.com/lunfardo314/easyfl"` exactly as today.

## What `easyfl/core` exposes

Roughly (final shape decided during implementation):

```go
package core

// Type parameter T is the data context (= *EvalContext on Proxima).
// Compose-only callers can use `any` if they never invoke Eval.

type Library[T any] struct { ... }            // unchanged shape
type Expression[T any] struct { ... }         // unchanged shape
type EmbeddedFunction[T any] func(*CallParams[T]) []byte
type CallParams[T any] struct { ... }         // unchanged shape

// FunDescriptor is the wallet-facing registration shape:
//   tag/funCode + arity + symbol + optional bytecode (extended fns).
// Wallet parses library.json in its own environment, iterates entries,
// and calls Register for each. The EmbeddedFunction pointer is OMITTED
// at the public surface — set internally only by easyfl/embed.
type FunDescriptor struct {
    Sym       string
    FunCode   uint16
    NumParams int
    Bytecode  []byte    // empty for embedded functions
    Source    string    // optional; used for re-serialisation
}

func NewLibrary[T any]() *Library[T]
func (lib *Library[T]) Register(desc FunDescriptor) error
func (lib *Library[T]) RegisterEmbedded(desc FunDescriptor, fn EmbeddedFunction[T]) error

func (lib *Library[T]) CompileExpression(source string) (*Expression[T], int, []byte, error)
func (lib *Library[T]) ExpressionFromBytecode(code []byte) (*Expression[T], error)
func (lib *Library[T]) DecompileBytecode(code []byte) (string, error)

// LocalScript compose:
func (lib *Library[T]) CompileLocalScript(source string) (LocalScriptBin, error)
func (lib *Library[T]) LocalScriptFromBytes(bin LocalScriptBin) (*LocalScript[T], error)
func (s *LocalScript[T]) Function(idx int) (*Expression[T], error)
// (no Eval / EvalInPool here — those live in top-level easyfl)
```

Key point: the `*Library[T]` and `*Expression[T]` returned by `core` are
**the same types** the eval path operates on. Top-level `easyfl` re-
exports them via type alias:

```go
package easyfl

type Library[T any] = core.Library[T]
type Expression[T any] = core.Expression[T]
type CallParams[T any] = core.CallParams[T]
type GlobalData[T any] = core.GlobalData[T]
type EmbeddedFunction[T any] = core.EmbeddedFunction[T]
type FunDescriptor = core.FunDescriptor
```

so every existing `easyfl.Library`, `easyfl.Expression`, etc. used by
Proxima code keeps compiling.

## What `easyfl/embed` does

Holds every embedded function body and exposes a registrar:

```go
package embed

func RegisterBase[T any](lib *core.Library[T])
```

`RegisterBase` calls `lib.RegisterEmbedded(...)` for each base
function (`evalConcat`, `evalSlice`, `evalAddUint`, …) with the right
funCode/symbol/arity + function pointer. This is the only place that
holds references to `evalConcat[T]` etc. — so importing only `core`
keeps the bodies unreachable, and LLVM DCE drops them from the WASM
binary together with all the `par.Trace(...)` / `par.TracePanic(...)`
inside them.

Top-level `easyfl.NewBaseLibrary[T]()` becomes:

```go
func NewBaseLibrary[T any]() *Library[T] {
    lib := core.NewLibrary[T]()
    embed.RegisterBase(lib)
    return lib
}
```

So existing callers (`ledger.lib.go` etc.) get the same wired-up
library they had before.

## Tracing revisit (in scope, modest)

The eval-path tracing API is `CallParams.Trace`, `CallParams.TracePanic`,
`CallParams.Require`, `CallParams.RequireNoError`. They live in
`eval.go` and are called from inside embedded function bodies (33×
`Trace`, 39× `TracePanic`/`Require` across `library_embed.go`,
`compiler.go`).

Today they all flow through `fmt.Sprintf`. After this refactor those
callers all live inside `easyfl/embed`, which is fine to import `fmt`
— it's never compiled into the wasm wallet binary. **So tracing
doesn't *have* to move for the wasm budget.**

But we want it minimal anyway:

1. **Delete `par.Trace(...)` non-panic calls** entirely. They are
   per-step verbose logging used only by `GlobalDataLog` /
   `GlobalDataTracePrint`, and even there only to debug ledger
   validation issues. `Tracef` in Proxima is the right tool for
   investigative logging; per-call tracing inside embedded bodies is
   dead weight. ~33 deletions across `library_embed.go`, `compiler.go`.
2. **Keep `par.TracePanic` / `par.Require` / `par.RequireNoError`**,
   but rename to `par.Panicf`, `par.Assertf`, `par.AssertNoError` and
   simplify implementation to `panic(fmt.Sprintf(...))` with no trace
   side-effect. `easyfl_util.Assertf` already does exactly this — we
   may not need a separate helper at all; embedded bodies can just
   call `easyfl_util.Assertf` directly.
3. **Delete `GlobalDataNoTrace`, `GlobalDataLog`, `GlobalDataTracePrint`
   and the `Trace()` / `PutTrace()` methods on `GlobalData[T]`.**
   `GlobalData[T]` becomes `interface { Data() T; Library() *Library[T] }`.
   Proxima's `validate.go` `traceOption` plumbing + `printTraceIfEnabled`
   gets removed too.

This is a separate, contained phase; it doesn't block the structural
split but is easier done in the same wave because most of the changes
land in the same files.

## Per-file audit (target layout)

| Current file | Goes to | Notes |
|---|---|---|
| `library.go` (types + ctor + register helpers) | split: types → `core/library.go`, `NewBaseLibrary` → top-level facade | |
| `compiler.go` | `core/compiler.go`; decompile helpers → `core/decompiler.go` | |
| `types.go` | `core/types.go` | `sync.Pool` for expressions stays inside core (TinyGo no-ops it). |
| `local_script.go` | split: compose half → `core/local_script.go`; `Eval` / `EvalInPool` → top-level `local_script_eval.go` | |
| `recursion.go` | `core/recursion.go` | |
| `library_embed.go` | split: helpers (`isNil` etc.) into `core`; all 30 bodies → `embed/*.go`; registrar in `embed/registry.go` | `reflect` use in `isNil` removed (use `p == nil`). |
| `eval.go` | top-level `easyfl/eval.go` | Stays exactly where it is. |
| `trace.go` | top-level `easyfl/trace.go` | Trimmed per "Tracing revisit". |
| `serde_json.go` | top-level `easyfl/serde_json.go` | Backend-only. |
| `serde_tools.go` | top-level `easyfl/serde_tools.go` | Backend-only. |
| `library_json.go` | top-level (`//go:embed library.json`) | Backend-only. Wallet receives JSON from API. |
| `slicepool/` | **unchanged** | Per constraint 3. |
| `tuples/` | **unchanged** | Per constraint 3. |
| `easyfl_util/` | **unchanged** | Already TinyGo-clean. |

`core` package's only stdlib imports should end up: `bufio`,
`bytes`, `encoding/binary`, `encoding/hex`, `errors`, `io`,
`math`, `strconv`, `strings`, `sync` (for the expression pool —
TinyGo no-ops it), `unicode`, `go/token` (for `IsIdentifier`).
No `fmt` beyond simple error wraps; no `encoding/json`; no `crypto/*`;
no `reflect`.

## Implementation plan

All phases are committed independently. Each phase keeps the tree
buildable+green for both easyfl and Proxima.

### Phase A — TinyGo build probe (baseline)

Add `easyfl/wasm/main.go`:

```go
package main

import "github.com/lunfardo314/easyfl"

func main() {
    lib := easyfl.NewBaseLibrary[any]()
    _, _, _, _ = lib.CompileExpression("concat(0x01, 0x02)")
}
```

Run `tinygo build -target=wasm -o /tmp/easyfl.wasm ./wasm/`.
Record the size. This is the **before** number for the full,
unsplit easyfl. The Phase D measurement (after split) tells us the
actual win.

One commit. May fail to compile under TinyGo — catalogue everything.

### Phase B — Tracing revisit (deletes only, no moves yet)

Lands first because it's the simplest, smallest, and reduces clutter
the structural split has to navigate.

- Delete `par.Trace(...)` non-panic calls from `library_embed.go`,
  `compiler.go`, anywhere else in easyfl.
- Replace `par.TracePanic(...)` / `par.Require(...)` / `par.RequireNoError`
  call sites with `easyfl_util.Assertf` / direct `panic(fmt.Sprintf(...))`
  in the embedded bodies. (We're still in `easyfl/embed`-future-territory,
  so `fmt` is fine here.) Or keep the helpers — but strip the
  `Trace()` side-effect.
- Delete `GlobalDataLog`, `GlobalDataTracePrint`, `GlobalDataNoTrace`
  types from `trace.go`. `GlobalData[T]` interface trimmed to
  `{ Data() T; Library() *Library[T] }`.
- Delete Proxima's `TraceOptionAll` / `TraceOptionFailedConstraints`
  plumbing in `ledger/transaction/validate.go`, `parse.go`,
  `ledger/utxodb/state_update.go`, `ledger/utxodb/utxodb.go`,
  `ledger/tests/ledger_test.go`.

This is one easyfl commit + one proxima commit + an easyfl version
bump.

### Phase C — Extract `easyfl/core` sub-package

Mechanical move of the compose-path files (library type, compiler,
decompiler, types, recursion, local_script compose half) into
`easyfl/core/`. Top-level `easyfl` keeps:

- type aliases re-exporting all public types from `core`;
- function re-exports for the small number of public free functions;
- `eval.go`, `serde_*.go`, `trace.go`, `library_json.go`;
- `local_script_eval.go` (split from current `local_script.go`).

`core` includes the helper that today is `library_embed.go`'s
`isNil()` — replaced with `p == nil` after audit of `Trace()`
removal (Phase B deletes most callers of `isNil` already).

Each Proxima file that imports `easyfl` keeps compiling unchanged.
Run `go build ./...` and `go test ./ledger/...` to confirm.

This is one big easyfl commit (the move is mechanical so a reviewer
can audit it) + an easyfl version bump.

### Phase D — Extract `easyfl/embed` sub-package

Move all 30+ embedded function bodies from `library_embed.go` to
`easyfl/embed/*.go`. Replace `library_embed.go`'s top-level
registration map with a call to `embed.RegisterBase(lib)` from
`easyfl.NewBaseLibrary`.

After this commit:
- `core` does not reference any concrete `evalConcat` etc.
- `embed` references them all and is the only place importing `fmt`
  (via `easyfl_util.Assertf` / `panic`) for these bodies.
- `easyfl.NewBaseLibrary` produces the same fully-wired library as
  before.

Run Proxima ledger tests; confirm everything still works.

### Phase E — Phase-A re-measure

Build the wasm probe **importing `easyfl/core`** (not top-level
`easyfl`) and re-run the tinygo build. Update the probe:

```go
package main

import "github.com/lunfardo314/easyfl/core"

func main() {
    lib := core.NewLibrary[any]()
    _, _, _, _ = lib.CompileExpression("concat(0x01, 0x02)")
}
```

Note this probe **cannot CompileExpression "concat"** without the
function being registered; in practice the wasm wallet will iterate
parsed library.json and call `lib.Register` first. The probe should
be a real round-trip:

```go
func main() {
    lib := core.NewLibrary[any]()
    _ = lib.Register(core.FunDescriptor{Sym: "concat", FunCode: 64, NumParams: -1})
    expr, _, _, _ := lib.CompileExpression("concat(0x01, 0x02)")
    text, _ := lib.DecompileBytecode(/* serialized expr */)
    _ = text
}
```

Compare size to Phase-A baseline. The target is "embed + fmt +
json + blake2b" all dropped from the binary; the remainder should
be compiler + decompiler + library registry + tuples + easyfl_util
+ TinyGo runtime.

Document the numbers in this file.

### Phase F — Wallet wiring (handover to wasm_txbuilder.md)

Update `wasm_txbuilder.md` Phase 0 to start from `easyfl/core` rather
than top-level `easyfl`. Document the descriptor-feeding pattern for
loading `library.json` parsed by the wallet's own JSON reader.

## Backward compatibility

- Every `easyfl.<Symbol>` used by Proxima today keeps resolving via
  type alias / function re-export.
- The change in `GlobalData[T]` interface (Phase B) drops two methods
  (`Trace`, `PutTrace`) — that is a **non-trivial** backcompat break
  for any external code implementing the interface. The only known
  implementers are `easyfl.GlobalData*` (which we delete) and Proxima's
  `Transaction`-bound wrapper (which we update in the same commit).
- `slicepool.Disable()` stays where it is (per "don't touch
  slicepool"). The earlier feedback to make nil-pool transparent is
  deferred; the existing `enabled`-gate is benign.
- `library.json` location is unchanged.

## Open items

- Whether the `core` package should expose its own `FromBytecode`-only
  variant of `LocalScript` for wallets that never compile from text
  (read-only inspection of compiled lock scripts).
- Whether wallet-side library descriptor loading should be table-
  driven (a slice of descriptors) or function-by-function. Decide
  during Phase F.

## Verification

1. `tinygo build -target=wasm -o /tmp/easyfl.wasm ./wasm/` succeeds
   in Phase A (baseline) and Phase E (after split).
2. Phase E binary is materially smaller than Phase A — expectation
   is removal of `fmt`, `encoding/json`, `blake2b`, and all embedded
   bodies. If the difference is < 100 KB, something is keeping
   reachability we didn't predict; investigate before declaring
   success.
3. `go test ./...` on easyfl stays green through each phase.
4. `go test ./ledger/...` on Proxima stays green through each phase.
5. Round-trip in wasm: parse a small library.json (handed in as a Go
   constant), register descriptors, compile a non-trivial expression,
   decompile, compare text matches.
