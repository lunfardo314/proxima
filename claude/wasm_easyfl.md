# EasyFL — TinyGo / WASM Compatibility (audit + plan)

Sibling document: [wasm_txbuilder.md](wasm_txbuilder.md) covers the
Proxima-side use of this work (the `ledger/txcore` wallet API). This
document covers everything inside `easyfl` itself that needs to change
for that.

Started as `easyfl/claude/tinygo_wasm.md`; moved here on 2026-05-18 to
keep the whole multi-repo refactor planned from Proxima (the only
consumer driving it).

---

## Goal

Compile a **subset** of EasyFL to WebAssembly via TinyGo so it can run
in browsers and other environments without the full Go runtime. Full
functionality (JSON serde, optimized allocators, library hash) remains
available only for the **Proxima backend** build, which keeps using
the standard Go toolchain.

## Scope decisions (input to planning)

| Concern | WASM subset | Proxima backend |
|---|---|---|
| Source compiler / bytecode evaluator | required | required |
| YAML library serde (`gopkg.in/yaml.v3`) | **already gone** | already gone |
| Crypto embedded fns (`blake2b`, `ed25519`) | **already gone** | provided by Proxima |
| `slicepool` optimized allocator | **simplified** to pure `make`/`append` | kept |
| `reflect` (one isolated call) | **removed** | n/a (also removed) |
| `sync.RWMutex` in `tuples/tree.go` (lazy subtree) | **stripped or no-op** | kept |

`blake2b` and `validSignatureED25519` were moved out of base easyfl on
2026-05-18 (commit `946e808` on easyfl `develop`, commit `77b06206`
on proxima `develop08`). The YAML→JSON cutover (the dominant blocker
in the original audit) had already shipped before that.

What the wasm core needs from easyfl:

- Library construction from JSON (read-only after init).
- Source compiler: source → bytecode.
- Source **decompiler**: bytecode → source (for inspection in the
  wallet UI).
- Symbol-prefix lookup (`FunctionCallPrefixByName`).
- The `tuples` sub-package (tuple builder + serialize).
- `easyfl_util` (`Uint64FromBytes`, `Concat`, etc.).

It does **not** need:

- The evaluator (`eval.go`).
- The slicepool (transactions are short-lived; allocation churn is
  fine).
- Embedded function dispatch.

So the wasm core is a "compose+inspect-only" subset, even tighter
than the standalone easyfl wasm subset.

## Audit findings

### Blockers (under TinyGo)

#### B1. `gopkg.in/yaml.v3` — `serde_tools.go:15`

**Resolved.** The YAML→JSON cutover shipped (project memory:
`project_easyfl_json_persistence.md`). `library.yaml` is gone,
`library.json` is the canonical asset, no `yaml.v3` dependency
remains.

#### B2. `reflect` — `library_embed.go:10, 93`

Single isolated use:

```go
func isNil(p interface{}) bool {
    return p == nil || (reflect.ValueOf(p).Kind() == reflect.Ptr && reflect.ValueOf(p).IsNil())
}
```

Trivial to replace. Used only to guard typed-nil `GlobalData[T]`
values. Open.

#### B3. Crypto embedded functions — `library_embed.go`

**Resolved 2026-05-18.** `blake2b` / `validSignatureED25519` moved
out of base easyfl into proxima/`ledger/crypto_builtins.go`.
`crypto/ed25519` and `golang.org/x/crypto/blake2b` imports gone from
`library_embed.go`. (Also: `serde_tools.go` still uses
`blake2b.Sum256` for `LibraryHash()` — that's the next item below.)

### Risks (compile under TinyGo but worth attention)

#### R1. `sync.Pool` / `sync.Mutex` / `sync.RWMutex`

- `eval.go:52-53` — `callPool`, `varScopePool` (per-arity pool of
  `[]*call[T]`)
- `types.go:112-113` — `expressionArrayPool`, `expressionPool`
- `slicepool/slicepool.go` — entire pool implementation
- `tuples/tree.go:20` — `subtreeMutex sync.RWMutex` (lazy subtree
  deserialization)

Under TinyGo / single-threaded WASM these are stubs / no-ops. Code is
correct but pools provide no reuse — net cost is allocation churn
plus a tiny code-size overhead. Per scope decision, slicepool
simplifies to direct allocation; the other `sync.Pool` sites in
`eval.go` and `types.go` should be treated the same.

For `tuples/tree.go`: under wasm the tuple is built and serialized
single-threadedly — no readers can race writers, so the lock is
dead weight. We want either a build-tag variant that strips the
mutex entirely or confirmation that TinyGo's no-op `sync.RWMutex`
is inlined to zero. Code-size concern, not correctness.

#### R2. `fmt` usage

Heavy in `compiler.go`, `eval.go`, `recursion.go`, `trace.go`,
embedded function error reporting via `par.TracePanic(...)`. Works
under TinyGo but adds significant binary size to WASM output
(likely the dominant size contributor once YAML and crypto are
gone).

Mitigation options (deferred): swap `fmt.Errorf` for static error
strings on hot paths; conditionally compile out `Trace` formatting
on WASM builds.

#### R3. Pure-Go crypto under TinyGo (only relevant if we *kept* it)

Not a concern given the scope decision to move crypto out, but for
reference: `golang.org/x/crypto/blake2b` and `crypto/ed25519` should
work under TinyGo 0.31+ but need verification; blake2b has an asm
fast path that TinyGo bypasses. **(Now Proxima's concern, not
easyfl's.)**

### Clean — no concerns

Verified via grep across non-test files:

- No goroutines (`go func`)
- No `os` / `net` / `syscall` / `os/exec` / `os/signal`
- No `unsafe`
- No `cgo`
- No `runtime.*` calls
- `compiler.go` uses `go/token.IsIdentifier` (small, pure, supported)
- `bufio.Scanner` with `MaxScanTokenSize` constant — fine
- `encoding/binary`, `encoding/hex`, `strings`, `strconv`, `math` — fine

## Per-file summary

| File | Imports of concern | Action for WASM subset |
|---|---|---|
| `library.go` | none | keep |
| `compiler.go` | none | keep |
| `eval.go` | `sync.Pool` (callPool, varScopePool); `slicepool` | replace pools with direct alloc |
| `types.go` | `sync.Pool` (expr arrays) | replace pools with direct alloc |
| `library_embed.go` | `reflect` | remove reflect (crypto already gone) |
| `serde_tools.go` | `blake2b` (LibraryHash) | move to sub-package |
| `serde_json.go` | none of concern | move to sub-package |
| `library_json.go` | `//go:embed library.json` (string only) | exclude or keep as string asset |
| `local_script.go` | none | keep |
| `recursion.go` | none | keep |
| `trace.go` | `fmt` only | keep |
| `slicepool/` | `sync.Pool`, `sync.Mutex` | build-tag-replace with direct-alloc shim |
| `tuples/` | `sync.RWMutex` in tree.go | strip or accept no-op under wasm |
| `easyfl_util/` | none | keep |
| `chess/`, `claude/` | demos, inherit parent | not part of WASM build |

## Locked-in design decisions

These decisions were taken before detailed planning began and serve as
the foundation for the refactor:

1. **WASM scope: compile + evaluate.** The WASM build includes both
   the source compiler and the bytecode evaluator. It does **not**
   include library construction at runtime (no `Extend()` flows
   exposed in WASM hosts) — the library is constructed once at
   startup and treated as read-only thereafter.

   *Proxima refinement:* the txcore subset is even tighter — it
   needs the compiler and **decompiler** but **not** the evaluator.

2. **Library loading: deferred.** The WASM subset is designed around
   an abstract loader interface; the concrete on-the-wire format
   (binary snapshot vs. programmatic construction) is picked during
   implementation. Core must not depend on YAML. **(YAML dependency
   already gone.)**

3. **Factoring: sub-package split (Option B), with a localized build
   tag for slicepool.** Crypto embedded functions moved to dedicated
   sub-package (`easyfl/embed/crypto` was the original idea; the
   actual outcome on 2026-05-18 was that they moved *out of easyfl
   entirely* into Proxima's `ledger/crypto_builtins.go`, which is
   the same net effect for the wasm build). JSON serde + LibraryHash
   still need to move to a sub-package (`easyfl/serde`). The root
   `easyfl` package becomes the TinyGo-clean subset. The
   `easyfl/slicepool` sub-package stays in place but ships two
   build-tagged implementations of the same public API (see decision
   4 below). Proxima updates its imports for the serde piece; the
   slicepool import path is unchanged.

4. **Slicepool: two implementations of the same API, selected by
   build tag.** The current `nil == pure allocation` pattern is fine
   — no Go-interface abstraction in core. Public eval signatures
   keep `*slicepool.SlicePool` as they are today. The
   `easyfl/slicepool` sub-package contains two files:
   - `slicepool.go` with `//go:build !tinygo` — current
     segment-based `sync.Pool`-backed implementation, used by
     Proxima.
   - `slicepool_tinygo.go` with `//go:build tinygo` — pure-allocation
     shim exposing the same `*SlicePool` type and method set (`New`,
     `Alloc`, `AllocData`, `Dispose`). No `sync.Pool`, no
     `sync.Mutex`, no segments. Methods just call `make`/`copy`.

   This is the **only build tag in the project** (modulo a possible
   identical split for `tuples/tree.go`'s mutex). Caller code in
   core (eval, compiler, embedded functions) is unchanged. The WASM
   binary contains no pooling machinery.

5. **Tracing / `fmt`: keep as-is for now.** No API divergence in
   this refactor. Binary size will be measured once the WASM build
   is functional; if `fmt` is the dominant cost, trace stripping
   can land as a follow-up.

## Target package layout

```
easyfl/                    (TinyGo-clean core, ~minimal deps)
├── library.go             — Library[T], function registry
├── compiler.go            — source → bytecode
├── eval.go                — bytecode → result (used by backend only;
│                            txcore doesn't import)
├── types.go               — Expression[T], CallParams[T]
├── library_embed.go       — non-crypto embedded fns; reflect removed
├── local_script.go
├── recursion.go
├── trace.go
└── …

easyfl/serde/              — JSON + LibraryHash; depends on easyfl + blake2b
├── json.go                — moved from serde_json.go
└── hash.go                — LibraryHash, ValidateCompiled

easyfl/slicepool/          — two implementations of the same API, build-tagged
├── slicepool.go             — //go:build !tinygo  — optimized segment pool
└── slicepool_tinygo.go      — //go:build tinygo   — pure-alloc shim

easyfl/tuples/             — sync.RWMutex stripped or build-tag-replaced
easyfl/easyfl_util/        — kept as-is
easyfl/chess/, claude/     — demo packages; not in WASM build
```

Sub-packages import the core. The core imports nothing from the
sub-packages, so the WASM build can compile core in isolation.

---

## Implementation plan (Phases A–D)

Strict ordering — each phase must build green before the next starts.
This plan is the canonical execution sequence; the Proxima-side
[wasm_txbuilder.md](wasm_txbuilder.md) phases (0–7) depend on Phase D
of this plan landing.

### Phase A — Probe (measurement-driven)

Add a stub `easyfl/wasm/main.go` guarded by `//go:build tinygo` that
does:

```go
package main

import "github.com/lunfardo314/easyfl"

func main() {
    lib := easyfl.NewBaseLibrary[any]()
    _, _, _, _ = lib.CompileExpression("concat(0x01, 0x02)")
}
```

Run `tinygo build -target=wasm ./wasm/`. Catalogue every actual
compile failure. The predictions above were made before the JSON
cutover and the crypto move; we want ground truth before doing
surgery. One commit.

Likely findings (predicted, not guaranteed):
- `reflect` in `library_embed.go:isNil` (B2).
- `sync.Pool` in `eval.go` / `types.go` (R1) — TinyGo no-ops these so
  probably fine, but binary-size penalty.
- `slicepool/slicepool.go` may need the build-tag split.
- Anything dragged by `serde_tools.go` (JSON loader + library hash).

### Phase B — Knock out blockers in dependency order

In order, each on its own commit so we can revert independently.

**B1. Replace `reflect`.** One-liner in `library_embed.go:isNil`.
Likely just `p == nil` for all known call sites — verify call sites
first. (Or, if a generic-typed nil check is needed, use a tiny
type-assertion-based helper.)

**B2. Apply the slicepool build-tag split** exactly as drafted in
decision #4 above:
- `slicepool/slicepool.go` → `//go:build !tinygo` (current
  implementation, unchanged)
- `slicepool/slicepool_tinygo.go` → `//go:build tinygo` (pure-alloc
  shim, ready to paste)
- `slicepool/slicepool_test.go` → `//go:build !tinygo`
No caller changes; the type and method set are identical.

**B3. Strip the `sync.RWMutex` from `easyfl/tuples/tree.go`** lazy-
subtree path under TinyGo. Two options — pick after Phase A measures
the actual size impact:
- Build-tag split (clean, mirrors slicepool).
- Trust TinyGo's no-op `sync.RWMutex` and skip this step.

**B4. Extract `easyfl/serde` sub-package** (the only cross-repo step).

Post the YAML cutover this is much smaller than the original audit
predicted. The whole code that needs to move is essentially:

- `serde_json.go` → `serde/json.go`
- `LibraryHash` / `ValidateCompiled` from `serde_tools.go` →
  `serde/hash.go`

After this, core easyfl has no JSON loader, no library-hash code. The
base library construction still works because `NewBaseLibrary` lives
in `library.go` and only needs the registrar / compiler bits.

Proxima updates required (do in the same atomic step):
- `ledger/lib_singleton.go` imports the new `LibraryHash` path.
- `ledger/upgrade_utxo.go` imports the new `BaseLibraryHash` path.
- Bump easyfl pseudo-version in `proxima/go.mod`.

Note: `library_json.go` (the `//go:embed library.json` declaration)
stays in core — it's just a string asset.

### Phase C — TinyGo build green + measure

Re-run `tinygo build -target=wasm ./wasm/` from the stub entrypoint.
Confirm it builds. Note binary size. If size is bigger than expected
this is signal for whether Phase 6 of the proxima plan (fmt/Trace
stripping) ever needs to land.

Also: round-trip test — compile + decompile a non-trivial expression
against a small library, in WASM, with results matching the
standard-Go build.

### Phase D — Update this spec

Flip each Phase A/B item's status to **DONE** with commit hash. Add a
"TinyGo build green as of <commit>" line. Cross-link from
`wasm_txbuilder.md` so the Proxima-side plan can start Phase 0 with
the easyfl dependency satisfied.

---

## Remaining open items (to resolve during detailed planning)

- ~~Slicepool TinyGo shim — exact API.~~ **Resolved.** Audit of all
  callsites confirms production code only uses `New()`, `Alloc()`,
  `AllocData()`, `Dispose()`, and the `*SlicePool` type. `Disable()`
  is test-only and called from `library_test.go:18` — that test file
  already imports `blake2b` and exercises YAML, so it is excluded
  from the TinyGo build by the YAML/crypto sub-package split,
  independent of slicepool. The TinyGo shim is exactly:

  ```go
  //go:build tinygo

  package slicepool

  type SlicePool struct{}

  func New() *SlicePool                              { return nil }
  func (p *SlicePool) Alloc(size uint16) []byte      { return make([]byte, size) }
  func (p *SlicePool) AllocData(data ...byte) []byte { ret := make([]byte, len(data)); copy(ret, data); return ret }
  func (p *SlicePool) Dispose()                      {}
  ```

  `slicepool/slicepool_test.go` gets `//go:build !tinygo` since it
  exercises the optimized segment allocator's internals.

- **Where `LibraryHash()` lives.** Used by `serde_tools.go` when
  writing compiled JSON — naturally moves to `easyfl/serde`. Proxima
  uses it independently in `lib_singleton.go` and `upgrade_utxo.go`;
  Phase B4 updates those imports atomically.

- **Embedded function registration API.** Crypto fns no longer live
  in easyfl, so `EmbeddedFunctions[T]` already returns no crypto
  symbols. Proxima's `def_embed.go` resolver chains
  `easyfl.EmbeddedFunctions(lib)` (base) with Proxima's own resolver
  map (which includes `evalBlake2b` / `evalValidSignatureED25519`).
  No further API change needed.

- **`isNil()` replacement.** Confirm all callers of `isNil()` to
  determine whether `p == nil` suffices, or a generic-typed `T`
  constraint is needed. Phase B1.

- **WASM entrypoint.** Lives at `easyfl/wasm/` (Phase A). The
  Proxima-side wasm entrypoint will live at `ledger/txcore/wasm/`
  (Proxima `wasm_txbuilder.md` Phase 5) and import this one as a
  library, not as a binary.

## Verification plan (once refactor lands)

1. `tinygo build -target=wasm -o easyfl.wasm ./wasm/` from the thin
   WASM entrypoint package. Phase A produces the first such build;
   Phase C re-runs it for size measurement.
2. Binary-size budget check.
3. Round-trip test: compile a non-trivial expression from source →
   bytecode → decompile → source-equivalent, in WASM, with results
   matching the standard-Go build.
4. Conformance: run the existing `library_test.go` cases that don't
   depend on YAML/crypto against the WASM build.
