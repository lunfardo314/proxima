# EasyFL — WASM Compatibility (audit, plan, and final state)

Sibling document: [wasm_txbuilder.md](wasm_txbuilder.md) covers the
Proxima-side use of this work (the `ledger/txbuildercore` wallet API).

Started as `easyfl/claude/tinygo_wasm.md`; moved here on 2026-05-18 to
keep the whole multi-repo refactor planned from Proxima.

**Status (2026-05-19): Phases A + B + C all shipped.**

---

## What shipped

**Phase A (easyfl `2e16ad8` + proxima `61ffe1ce`):**
- `easyfl/wasm/main.go` probe entrypoint.
- Three TinyGo-blocker fixes (testutil sub-package extraction, two
  `int`-overflow fixes for 32-bit wasm targets).

**Phase B (easyfl `886b832` + proxima `6ea1015e`):**
- `GlobalData[T]` interface trimmed to `{ Data, Library }`.
- `GlobalDataLog` / `GlobalDataTracePrint` types deleted.
- `CallParams.Trace` deleted; `TracePanic` / `Require` /
  `RequireNoError` kept but trace side-effect removed.
- 32 `par.Trace(...)` lines removed from embedded function bodies.
- Proxima's `TraceOption*` plumbing removed.

**Phase C (easyfl `488875c` → renamed to `c2f3713`, proxima `<this commit>`):**
- Extracted `easyfl/engine` sub-package (originally landed as
  `easyfl/compose`; renamed in `c2f3713`): Library type, Expression,
  CallParams, GlobalData / GlobalDataNoTrace, the eval engine, the
  source compiler, the decompiler, LocalScript, library construction
  primitives, LibraryHash. Go's method-on-receiver-type rule means
  compile and eval methods on Library have to live together — so the
  package's honest scope is "the script engine" (both compose and
  eval), hence the `engine` name.
- Extracted `easyfl/embed`: the 30+ base embedded function bodies +
  `Resolver` factory. The 4 library-bound built-ins
  (`EvalParseBytecode` &c.) stay on `engine.Library` so they can reach
  unexported helpers like `matchesPrefixes`.
- Top-level `easyfl` is now a **back-compat facade**: type aliases
  re-export every `engine.*` type, plus `NewBaseLibrary` (wires
  engine + embed + the embedded library.json), JSON serde as
  **free functions** (`easyfl.ToJSON(lib, …)`,
  `easyfl.UpgradeFromJSON(lib, …)`, …), and the
  `EmbeddedFunctions`/`InlineDataBytecode`/`EvalExpression…`
  re-exports.
- Several previously-unexported `engine` helpers were promoted to
  public to keep the test suite compiling: `Extend`, `EmbedShort`,
  `EmbedLong`, `MustFuncDescriptor`, `IntroduceUpdate`,
  `FunctionByName`, `ParseFunctions`, `ParseCallPrefix`,
  `ExtractReferencedFunCodes`, `CheckForCycles`,
  `CountParametersFromSource`, `FunctionSymbols` (new), plus the
  `FunParsed` type.

Proxima follow-up: ~5 JSON-method call sites switched from
`lib.ToJSON(...)` / `lib.UpgradeFromJSON(...)` /
`lib.IntroduceUpdateJSONMulti(...)` to the free-function form.

## Soundness payoff

The compile-time-enforced boundary is the win:

- Importing `easyfl/engine` directly cannot reach the embed package's
  function bodies, the JSON serde, or the embedded `library.json`
  blob — it's structural separation, not just convention.
- The wallet's recommended path is now: `import "github.com/lunfardo314/easyfl/engine"`,
  `engine.NewLibrary[T]()`, construct `engine.LibraryFromJSON` in the
  wallet's own environment (its own JSON parser), call
  `lib.Upgrade(desc)`, then `lib.CompileExpression` /
  `lib.DecompileBytecode`.
- The Proxima ledger keeps importing `easyfl` — every `easyfl.Library`,
  `easyfl.NewBaseLibrary[*EvalContext]()`, etc. resolves via type alias
  or re-exported wrapper. No call-site change there beyond the JSON
  method-to-function flip.

## Size measurements

Reference baseline numbers (TinyGo 0.41.1 wasm target, gzip -9):

| Probe | Path | Raw | Gzipped |
|---|---|---|---|
| TinyGo hello-world | one `println` | 11 KB | — |
| **Phase A baseline** (full): `easyfl.NewBaseLibrary` + Compile | 1.5 MB | 471 KB |
| Wallet path via top-level `easyfl` (NewLibrary + manual Upgrade) | 886 KB | 288 KB |
| **Wallet path via `easyfl/engine` directly** | 845 KB | 278 KB |
| Realistic `easyfl.NewLibraryFromJSON` + Compile + Decompile | 1.5 MB | 478 KB |

The wallet's compose-only path drops to **278 KB gzipped** when it
imports `engine` directly. The remainder is irreducible without
replacing `encoding/hex`/`strconv` with hand-rolled equivalents
(those are the dominant wasm-size contributors below the eval bodies).

## Recommended wallet API

```go
import "github.com/lunfardo314/easyfl/engine"

lib := engine.NewLibrary[any]()              // no embedded bodies referenced
desc := &engine.LibraryFromJSON{
    Functions: []engine.FuncDescriptorJSON{
        {Sym: "concat", NumArgs: -1, EmbeddedAs: "evalConcat", FunCode: 64},
        // …rest of library.json, parsed by wallet's own JSON reader…
    },
}
_ = lib.Upgrade(desc)                         // no embed callback ⇒ funcs registered without function pointers
expr, _, code, _ := lib.CompileExpression("concat(0x01, 0x02)")
source, _ := lib.DecompileBytecode(code)
```

Key points:
- `engine.NewLibrary` is the wallet's entrypoint. It does *not* drag
  the embedded function bodies because nothing reachable references
  `embed.Resolver`.
- Constructing `engine.LibraryFromJSON` directly bypasses
  `encoding/json` entirely. The wallet parses JSON in its own
  environment (JS-side, or its own Go JSON parser).
- `lib.Upgrade` with no embed callback leaves `funDescriptor.embeddedFun`
  nil — fine for compile + decompile, which only need symbol / funCode /
  arity / callPrefix metadata.

## Open levers (not active)

If a future need pushes below the current 278 KB gzipped floor:

- **Replace `encoding/hex`** with hand-rolled hex. Drops `fmt` +
  `reflect` + `reflectlite` transitive pull. ~50 KB raw / ~30 KB
  gzipped.
- **Replace `strconv` itoa-family** in `easyfl_util` with hand-rolled.
  ~17 KB raw / ~10 KB gzipped.
- **Hand-rolled JSON** if the wallet decides Go-side encoding/json is
  too heavy (current plan defers JSON entirely to the wallet's host
  environment).

Each is an independent cleanup, not blocked by anything.

## Final package layout

```
easyfl/                  back-compat facade
├── facade.go            type aliases + NewBaseLibrary + free-function re-exports
├── serde_json.go        ToJSON / UpgradeFromJSON / NewLibraryFromJSON / ... (free funcs)
├── library_json.go      //go:embed library.json
├── library.json         the base library descriptors
│
├── engine/              the script engine (compile + decompile + eval + registry)
│   ├── library.go       Library type, registration, Extend, Clone, …
│   ├── compiler.go      source → bytecode
│   ├── recursion.go     call-graph utilities
│   ├── local_script.go  LocalScript compose + Eval
│   ├── eval.go          eval engine, CallParams, GlobalData, GlobalDataNoTrace
│   ├── library_bound_builtins.go  EvalParseBytecode &c. (method on Library)
│   ├── serde_tools.go   LibraryHash, ValidateCompiled, IntroduceUpdate, Upgrade
│   └── types.go         Expression, funDescriptor, EmbeddedFunction
│
├── embed/               base embedded function bodies (eval-time, drops by DCE in wallet builds)
│   └── embed.go         30+ function bodies + Resolver[T] factory
│
├── easyfl_util/         shared helpers (no fmt-heavy paths)
│   ├── util.go
│   └── testutil/        test-only helpers (Phase A split)
│
├── slicepool/           pool for eval-time interim allocations (unchanged)
├── tuples/              tuple primitives (unchanged)
├── chess/               example covenant; standalone (unchanged)
└── wasm/main.go         TinyGo probe entrypoint
```

## Cross-link

[wasm_txbuilder.md](wasm_txbuilder.md) — the Proxima-side
`ledger/txbuildercore` wallet API. Starts from the API surface
documented in "Recommended wallet API" above.
