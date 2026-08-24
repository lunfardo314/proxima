# Local scripts in Proxima — design and implementation

> **QUEUED → `txdocs/redeemer_scripts.md`** — `redeemScript` / `callRedeemer`: design and as-built reference. Shipped.
> Rewritten there, then archived. See `claude/kb_reorg.md`.

This is the consolidated design + as-built reference for the
`redeemScript` / `callRedeemer` feature on top of easyfl's local-script
support. Supersedes the earlier two-file split
(`local_script_proxima.md` and `local_script_plan.md`).

## 1. Context

easyfl exposes a serialised "local script" format
(`*easyfl.LocalScript[T]`) — a self-contained bundle of EasyFL
functions identified by content hash. Proxima needs a way to:

- commit, in a transaction, to a local-script binary so the network
  agrees on what code is being authorised; and
- invoke the committed script's functions from inside other
  constraints / locks during the same transaction's validation.

The two halves are decoupled:

- **`redeemScript(<bin>)`** is a tx-level constraint that registers a
  script-bin commitment in the transaction.
- **`callRedeemer(<hash>, <fnIdx>, args...)`** is a builtin that
  dispatches into a previously-committed script's function.

Calling these "redeemers" is the term taken from the original task
description. The mental model: a UTXO's lock can demand evidence that
some script was committed and that one of its functions returned a
truthy answer — much like the chess-validator covenant pattern in
easyfl's docs, but generalised to anything expressible in EasyFL.

## 2. Wire shape

### 2.1 `redeemScript`

- Signature: 1 argument, returns truthy on success.
- The argument is the local-script binary as a literal.
- Lives at path `TxConstraints` (`0x000a`). Pushed onto the
  `TxConstraints` tuple via `(*TxBuilder).PushTxConstraint(bytecode)`.
- Multiple `redeemScript` constraints in one tx are supported; the
  resulting commitment list is the set of unique hashes seen.

### 2.2 `callRedeemer`

- Signature: vararg (`numArgs: -1` in the YAML). At least 2 args.
- `arg[0]` is the script's content hash (32-byte literal).
- `arg[1]` is the function index inside the script (1 byte).
- `arg[2..N]` are forwarded to the script function as-is.
- Returns whatever the dispatched function returns.
- Can appear anywhere ordinary EasyFL bytecode runs — UTXO locks,
  extra constraints, even inside other tx-level constraints — as long
  as the hash has been committed first in the same tx.

## 3. Soundness, auditability, termination

The design rests on three hard properties:

### 3.1 Soundness — commit-then-invoke separation

The commitment list must not grow during UTXO-level evaluation. If a
redeemed script's body could call `redeemScript(...)`, the list could
expand mid-eval and a later `callRedeemer` could dispatch into a
script committed *during* that same eval — breaking the
"commit-then-invoke" separation that makes the rule auditable.

**Enforced by** a Go-level scope check inside `evalRedeemScript`:

```go
if !bytes.HasPrefix(ctx.EvalPath(), PathToTxConstraints) {
    par.TracePanic("redeemScript: must be invoked at TxConstraints ...")
}
```

A redeemed script body that tries to call `redeemScript(...)` runs
under the *outer* constraint's eval path (UTXO lock path or similar),
so the prefix check fires correctly.

This is the load-bearing soundness gate. It is in Go rather than an
EasyFL wrapper because EasyFL has no notion of library-private
functions: any wrapper-vs-primitive split is bypassable by user
bytecode that names the primitive directly.

### 3.2 Auditability — static commitment + static dispatch

A static reader of a transaction's bytecode should be able to
enumerate:

- exactly which scripts the tx commits to, and
- exactly which scripts each `callRedeemer` call site can dispatch to.

**Enforced by** an inline-data literal check on the relevant arg of
each builtin, using easyfl's `(*CallParams[T]).ArgExpression(n)`
accessor:

```go
// in evalRedeemScript:
if !par.ArgExpression(0).IsInlineData() { ... }

// in evalCallRedeemer:
hashExpr := par.ArgExpression(0)
if !hashExpr.IsInlineData() { ... }
if len(hashExpr.InlineData()) != 32 { ... }
```

A formula in either position is rejected at runtime before any work
is done. The check examines the call-site expression tree without
evaluating it; cost is a pointer deref + bit test.

### 3.3 Termination — no runtime self-recursion

EasyFL's "cross-script composition is recursion-free" argument is
structural: a hash literal can only exist after the callee binary is
finalised, so the dependency graph between binaries is a DAG by
construction. EasyFL has no runtime step counter — termination
relies entirely on this static acyclicity.

The inline-data check on `callRedeemer`'s hash arg (§3.2) is what
preserves the structural argument: a script cannot compute its own
hash at runtime and call into itself. Auditability and termination
share the same enforcement seam.

## 4. State

### 4.1 Per-tx commitment list

```go
// On *transaction.Transaction:
redeemedScripts [][32]byte // nil-friendly, lazy alloc
```

Linear scan on read. The vast majority of txs carry zero entries
(zero alloc); feature-using txs carry 1–2.

API on `TxContextAccess`:

```go
IsScriptRedeemed(h [32]byte) bool
AddRedeemedScript(h [32]byte) // idempotent
```

Reset is automatic — each `*Transaction` is parsed once and validated
once; the slice starts nil.

### 4.2 Library-level compiled-script cache

```go
// On *ledger.Library:
type CompiledScriptCache interface {
    Get(hash [32]byte) (*easyfl.LocalScript[*EvalContext], bool)
    Put(hash [32]byte, s *easyfl.LocalScript[*EvalContext])
}

(*Library).CompiledScriptCache() CompiledScriptCache       // lazy default (sync.Map)
(*Library).WithCompiledScriptCache(c) *Library             // for tests / custom impls
```

Default: thread-safe, unbounded `sync.Map` cache, one per
`*ledger.Library` instance (one per upgrade slot). Operators can
swap in an LRU or persistent cache — but with the default the cache
invariant `cache[H] == s ⇒ blake2b(s.Bytes()) == H` holds trivially
because nothing is ever evicted.

A custom evicting cache must guarantee that any hash currently in some
in-flight tx's commitment list is pinned, otherwise `callRedeemer`
will see the hash as redeemed but find no compiled form. The default
unbounded cache sidesteps this entirely.

## 5. Validation ordering

Already correct in Proxima today:

1. `ValidatePartialContext` (signature, structural).
2. `ValidateFullContext`:
   - `validateTxLevelConstraints` runs **first**, populating the
     redeemed-scripts list via `evalRedeemScript`.
   - `validateOutputs` runs the consumed and produced UTXO
     constraints, where `callRedeemer` is allowed to fire.

`validateTxLevelConstraints` walks the `TxConstraints` tuple as
plain bytecode — every element is evaluated as a constraint. (Unlike
the output tuples, there are no special amounts/index-values slots.)

## 6. Public API summary

```go
// Per-tx (ledger/def_embed.go):
type TxContextAccess interface {
    ... // existing
    IsScriptRedeemed(h [32]byte) bool
    AddRedeemedScript(h [32]byte)
}

// Library-level (ledger/lib.go + ledger/local_script_cache.go):
func (lib *Library) CompiledScriptCache() CompiledScriptCache
func (lib *Library) WithCompiledScriptCache(c CompiledScriptCache) *Library

// Builder (ledger/txbuilder/txbuilder.go):
func (txb *TxBuilder) PushTxConstraint(bytecode []byte)
```

Symbol constants exposed for external code that wants to mint
bytecode: `ledger.SymRedeemScript = "redeemScript"`,
`ledger.SymCallRedeemer = "callRedeemer"`, `ledger.HashSize = 32`.

Required upstream easyfl additions used here:

- `(*CallParams[T]).ArgExpression(n byte) *Expression[T]`
- `(*CallParams[T]).GlobalData() GlobalData[T]`

Both are tiny accessors (no behaviour change in easyfl).

## 7. Validation flow walkthrough

A typical tx using the feature:

```
Tx
├── TxConstraints
│   └── redeemScript(0x<bin>)
├── Outputs
│   └── Output[1]
│       ├── amounts        (idx 0)
│       ├── index-values   (idx 1)
│       ├── lock           (idx 2 — sigLock or whatever)
│       └── extra constraint (idx 4): callRedeemer(0x<h>, 0x00, 0x42)
```

Validation:

1. `validateTxLevelConstraints` evaluates `redeemScript(0x<bin>)`.
   - Scope check passes (path = `0x000a:00`).
   - `arg[0]` is inline data — passes literal check.
   - Hash = blake2b(bin); cache miss; decode via
     `LocalScriptFromBytes`; `cache.Put(hash, s)`;
     `tx.AddRedeemedScript(hash)`.
2. `validateOutputs` evaluates each constraint of each produced
   output. The extra constraint at idx 4 is `callRedeemer(<h>, 0)`.
   - `arg[0]` is inline data, 32 bytes — literal check passes.
   - `tx.IsScriptRedeemed(h)` is true — gate passes.
   - Cache hit; `s.Eval(par.GlobalData(), 0, [0x42])` runs the
     dispatched function with the same trace context.
   - Whatever the function returns becomes the constraint result;
     non-empty is truthy.

## 8. What's deliberately NOT done

- **No EasyFL wrapper for redeemScript/callRedeemer.** A wrapper that
  enforces the scope check from the YAML side would be cosmetic — the
  embedded primitive is callable directly under any name in the
  library because EasyFL has no library-private visibility. Putting
  the check in Go is the only authoritative place for it. (If easyfl
  later adds an `internal: true` descriptor flag, we revisit.)

- **No expression-tree walker over arbitrary bytecode.** Earlier
  drafts proposed a Proxima-side walk over every constraint bytecode
  to enforce inline-data on relevant args. Once the runtime
  `ArgExpression` check exists, the walker is redundant — it would
  reject the same things, slightly earlier, at the cost of an extra
  parse pass per constraint.

- **No `mustRegisterConstraint` for redeemScript.** The constraint
  registry exists to (a) hand back a typed Go object on parse and (b)
  print a friendly name in error messages. There's no typed
  `RedeemScriptConstraint` consumer; the bytecode just runs. Names in
  errors come from easyfl's existing `FunctionNameByCallPrefix`
  fall-through path.

- **No cross-tx commitment persistence.** Each tx starts with an
  empty commitment list.

- **No refcounted-LRU cache.** Default unbounded; swap-in API exists
  for operators who need eviction.

- **No pinned-import-set checks.** `lib.LocalScriptFromBytes` is
  used (not `LocalScriptFromBytesWithCheck`). Pinned import sets are
  a developer-time typo-catch; covenant authors who want them can
  compose their own check via easyfl's `CompileLocalScriptWithCheck`
  in their wallet/CLI tooling.

## 9. As-built file map

### easyfl (separate repo)
- `eval.go` — `(*CallParams[T]).ArgExpression(n)` and
  `(*CallParams[T]).GlobalData()` accessors.
- `library_test.go` — `TestArgExpression`, `TestGlobalData`.

Pushed to origin/develop ahead of the proxima bump.

### proxima
- `ledger/local_script_builtins.go` — `evalRedeemScript`,
  `evalCallRedeemer`, plus `SymRedeemScript`, `SymCallRedeemer`,
  `HashSize` constants.
- `ledger/local_script_cache.go` — `CompiledScriptCache` interface,
  default `unboundedScriptCache`, accessor + swap method.
- `ledger/transaction/redeemed_scripts.go` —
  `IsScriptRedeemed` / `AddRedeemedScript` on `*Transaction`.
- `ledger/def_embed.go` — `TxContextAccess` extended;
  `_unboundedEmbedded` map gains the two new symbols.
- `ledger/def/def_embed0.yaml` — new function descriptors for
  `redeemScript` (numArgs 1) and `callRedeemer` (numArgs -1).
- `ledger/lib.go` — `Library` gains `compiledScriptCache` /
  `scriptCacheOnce`.
- `ledger/transaction/parse.go` — `Transaction` gains
  `redeemedScripts [][32]byte`.
- `ledger/transaction/validate.go` — `validateTxLevelConstraints`
  rewritten as a plain bytecode walker (no
  amounts/index-values special cases).
- `ledger/txbuilder/txbuilder.go` — `transactionData.TxConstraints`
  field, `(*TxBuilder).PushTxConstraint`, ToTuple wired (empty list
  still serialises as nil for backward-compat).

### Tests
- `ledger/tests/local_script_test.go` — 15 tests covering
  redeemScript happy / formula-rejected / invalid-bin /
  outside-TxConstraints / idempotent / cross-tx-cache-reuse and
  callRedeemer happy / not-redeemed / hash-formula / hash-wrong-size /
  idx-not-1-byte / idx-out-of-range / wrong-arity / cross-script-
  composition / determinism.

Pre-existing dead test removed: `TestLocalLibrary` in
`ledger/tests/ledger_test.go` referenced the legacy
`CompileLocalLibraryToTuple` API that easyfl no longer exposes.

## 10. Open notes

1. **Function names don't affect bin hashes.** Easyfl's
   `LocalScriptBin` carries arity + bytecode but not source-level
   names — `func id : $0` and `func renamed : $0` produce identical
   bins (and identical hashes). Test authors who need
   distinct hashes must vary the *body*, not the name. The
   `TestRedeemScript_CrossTxCacheReuse` test learnt this the hard way
   when its first attempt at a "unique" source produced a
   pre-cached hash and the assertion silently confused itself.

2. **Cache is per-`*Library`, per upgrade slot.** Different upgrade
   slots get different caches — the same bin bytes produce a
   different `*easyfl.LocalScript[T]` against different libraries,
   because funCode tables differ. Not a concern today but worth
   knowing if cross-library use ever comes up.

3. **YAML lives in upgrade0 (genesis).** The `develop08` branch is
   pre-release for v0.8.0; the genesis YAML isn't frozen. If we
   later decide to defer to a post-genesis upgrade, the change is
   moving the two YAML entries from `def_embed0.yaml` to a new
   `def_embed1.yaml` and registering a corresponding
   `resolveEmbeddedUpgrade1` in `def_embed.go`.

4. **Decompile / pretty-printing.** `redeemScript(...)` decompiles
   via easyfl's normal path because the symbol is in the library.
   The validate-time error-path string `_constraintName` falls back
   to `constraint_call_prefix(...)` for unknown prefixes; for
   `redeemScript` and `callRedeemer` we currently get that fallback
   in the rare error case where a constraint fails. If this becomes
   noisy in production logs, the right fix is to teach
   `_constraintName` to consult easyfl's `FunctionNameByCallPrefix`
   — out of scope here.
