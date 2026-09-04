# Local scripts in Proxima — design and implementation

> SHIPPED

This is the consolidated design + as-built reference for the
`redeemScript` / `callRedeemer` feature on top of easyfl's local-script
support. Supersedes the earlier two-file split
(`local_script_proxima.md` and `local_script_plan.md`).

Revised 2026-09-04: `redeemScript`'s argument is no longer required to be
an inline literal, and dispatch depth is now bounded at runtime. Sections
2.1, 3.2 and 3.3 carry the change.

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
- The argument yields the local-script binary. It is either inline data
  or a formula the library can evaluate on its own — see §3.2.
- Lives at path `TxConstraints` (`0x000a`). Pushed onto the
  `TxConstraints` tuple via `(*TxBuilder).PushTxConstraint(bytecode)`.
- Multiple `redeemScript` constraints in one tx are supported; the
  resulting commitment list is the set of unique hashes seen.

### 2.2 `callRedeemer`

- Signature: vararg (`numArgs: -1` in the descriptor). At least 2 args.
- `arg[0]` is the script's content hash (32-byte inline literal — this
  one is not relaxed, see §3.2).
- `arg[1]` is the function index inside the script (1 byte).
- `arg[2..N]` are forwarded to the script function as-is.
- Returns whatever the dispatched function returns.
- Can appear anywhere ordinary EasyFL bytecode runs — UTXO locks,
  extra constraints, even inside other tx-level constraints — as long
  as the hash has been committed first in the same tx, and the chain of
  nested calls stays within the depth bound of §3.3.

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

### 3.2 Auditability — what a reader can determine from the bytes

A reader of a transaction's bytecode should be able to enumerate:

- exactly which scripts the tx commits to, and
- exactly which scripts each `callRedeemer` call site can dispatch to.

The two halves are enforced differently.

**`callRedeemer` arg 0 must be an inline 32-byte literal.** The check
examines the call-site expression tree without evaluating it, using
easyfl's `(*CallParams[T]).ArgExpression(n)` accessor; cost is a pointer
deref and a bit test. So a reader can still tell from a call site alone
which script it reaches.

**`redeemScript` arg 0 need only be transaction-independent.** Inline
data is taken verbatim. Anything else is evaluated in an *empty
context*: nil data context, empty parameter scope. A formula that
reaches for the transaction dies on a nil dereference, one that
references a parameter dies on the empty scope, and both panics are
caught by the validator like any other constraint failure. What survives
is exactly the set of expressions whose value is a function of the
library alone:

```go
// in evalRedeemScript, via redeemScriptArg:
expr := par.ArgExpression(0)
if expr.IsInlineData() {
    return expr.InlineData()
}
return easyfl.EvalExpressionInPool(lib.NewGlobalDataNoTrace(nil), par.Spool(), expr)
```

The commitment set therefore remains determined by the transaction's
bytes, but reading it requires the library the transaction validates
against.

That is what the relaxation buys. A library upgrade can carry a
frequently used script as an ordinary function, and transactions name it
instead of repeating it. In the chess example the two script binaries are
8,253 bytes of an 8,887-byte move transaction; named, each
`redeemScript` constraint is 3 bytes of bytecode.

Note that this is a change in *behaviour* with no change in the library
hash. The hash covers funCodes, arities, symbols, bytecode, the
`embeddedAs` key and the immutable flag — the name of the Go function
behind an embedded symbol, but not what that function does. Nothing in
the protocol gates it, so a transaction using a formula is invalid on a
node running older code and valid on a newer one. The change has to
reach the whole network before any such transaction exists.

### 3.3 Termination — bounded dispatch depth

EasyFL has no runtime step counter, and its cycle checks work by
symbol, so neither can see through a content hash.

Termination used to rest on a structural argument alone: a script's
bytes are fixed before its hash exists, so a hash literal can never name
the script it sits in, and the dispatch graph between binaries is a DAG
by construction.

That argument is sound, but it holds only while `callRedeemer`'s hash
argument is a literal. If the argument were ever allowed to name a hash
indirectly — through a library symbol, say — a fixed point becomes
constructible funCode-first: reserve the funCode the new symbol will
get, compile a script whose body calls that funCode, hash the script,
then define the symbol to return that hash. The script's bytes never
contain the hash, so nothing has to be inverted. Two scripts can be made
to call each other the same way, with no fixed point at all.

The consequence would not be a rejected transaction. Unbounded dispatch
exhausts the goroutine stack, and that is a fatal runtime error no
`recover` intercepts — the node dies rather than refusing the
transaction.

**Enforced by** a dispatch-depth counter on the `*EvalContext`,
incremented for the duration of each `callRedeemer` frame. The bound is
the number of scripts the transaction has committed:

```go
if ctx.redeemerDepth >= ctx.TxContext().NumRedeemedScripts() {
    par.TracePanic("callRedeemer: dispatch depth %d exceeds ...")
}
ctx.redeemerDepth++
defer func() { ctx.redeemerDepth-- }()
```

Every frame dispatches into one of the committed scripts, so a chain
visiting only distinct scripts can never be deeper than the commitment
list; a deeper one has revisited a script, which means the graph has a
cycle. The bound is exact — it refuses cycles and nothing else — and
there is no constant to tune. Real compositions sit at the limit rather
than under a ceiling: the chess covenant redeems two scripts and
dispatches two deep.

One `*EvalContext` is built per constraint evaluation and a transaction
validates on a single goroutine, so the counter needs no
synchronisation. The nested script shares the caller's context because
`callRedeemer` passes `par.GlobalData()` into `EvalInPool`.

A cycle is still unconstructible today, so this check cannot fire in
production; it is what keeps termination true if the hash argument is
ever relaxed. See §10 note 5 for why its message will not appear in
logs.

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
NumRedeemedScripts() int      // bounds callRedeemer dispatch depth
```

Reset is automatic — each `*Transaction` is parsed once and validated
once; the slice starts nil.

The dispatch depth itself lives on the `*EvalContext`, not on the
transaction: it is scoped to one constraint evaluation chain, and one
context is built per constraint.

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
    NumRedeemedScripts() int
}

// Library-level (ledger/lib.go + ledger/local_script_cache.go):
func (lib *Library) CompiledScriptCache() CompiledScriptCache
func (lib *Library) WithCompiledScriptCache(c CompiledScriptCache) *Library

// Builder (ledger/txbuildercore/txbuilder.go):
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
   - `arg[0]` is inline data, so it is taken verbatim. Had it been a
     formula it would have been evaluated with no transaction and no
     parameters.
   - Hash = blake2b(bin); cache miss; the bin is copied and decoded via
     `LocalScriptFromBytes`; `cache.Put(hash, s)`;
     `tx.AddRedeemedScript(hash)`.
2. `validateOutputs` evaluates each constraint of each produced
   output. The extra constraint at idx 4 is `callRedeemer(<h>, 0)`.
   - `arg[0]` is inline data, 32 bytes — literal check passes.
   - `tx.IsScriptRedeemed(h)` is true — gate passes.
   - Dispatch depth 0 is below the one redeemed script, so the frame is
     admitted and the depth becomes 1.
   - Cache hit; `s.EvalInPool(par.GlobalData(), par.Spool(), 0, [0x42])`
     runs the dispatched function, sharing the caller's context and
     slice pool.
   - Whatever the function returns becomes the constraint result;
     non-empty is truthy.

The copy at step 1 is not incidental. easyfl clones only the wire form
inside `LocalScriptFromBytes` and builds the decoded script's expression
trees over the caller's slice, while the cache outlives both the
transaction and the slice pool a formula would allocate in.

## 8. What's deliberately NOT done

- **No EasyFL wrapper for redeemScript/callRedeemer.** A wrapper that
  enforces the scope check from the descriptor side would be cosmetic — the
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

- **`callRedeemer`'s hash argument is still literal-only**, even though
  termination no longer depends on it. Relaxing it the way
  `redeemScript`'s argument was relaxed would shrink locks too, but it
  would cost the property that a call site names its callee in its own
  bytes. That is a smaller loss than the crash the depth bound now
  prevents, so it is a live option rather than a closed one — it was
  simply not part of the same change.

- **No size cap on a formula's result.** A computed binary is already
  bounded at 64 KiB by `slicepool`, and the local-script wire format
  encodes its body length in a `uint16`, so an oversized result fails
  the header parse. A separate check would reject the same inputs.

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
  `IsScriptRedeemed` / `AddRedeemedScript` / `NumRedeemedScripts` on
  `*Transaction`.
- `ledger/def_embed.go` — `TxContextAccess` extended; `EvalContext`
  carries `redeemerDepth`; the embedded-resolver map gains the two new
  symbols.
- `ledger/def/def_embed0.json` — function descriptors for
  `redeemScript` (numArgs 1) and `callRedeemer` (numArgs -1).
- `ledger/lib.go` — `Library` gains `compiledScriptCache` /
  `scriptCacheOnce`.
- `ledger/transaction/parse.go` — `Transaction` gains
  `redeemedScripts [][32]byte`.
- `ledger/transaction/validate.go` — `validateTxLevelConstraints`
  rewritten as a plain bytecode walker (no
  amounts/index-values special cases).
- `ledger/txbuildercore/txbuilder.go` — `transactionData.TxConstraints`
  field, `(*TxBuilder).PushTxConstraint`, ToTuple wired (empty list
  still serialises as nil for backward-compat).
- `ledger/txbuildercore/helpers_redeemer.go` — wallet-side emitters
  `NewRedeemScriptConstraint`, `NewCallRedeemerConstraint`, and
  `LocalScriptHash`.

### Tests
- `ledger/tests/local_script_test.go` — 23 tests. For redeemScript:
  happy / formula-bin / formula-bin-invalid / formula-touching-tx /
  formula-nested-redeem / formula-nested-callRedeemer /
  formula-param-ref / library-resident-bin / formula-cached-across-tx /
  invalid-bin / outside-TxConstraints / idempotent /
  cross-tx-cache-reuse. For callRedeemer: happy / not-redeemed /
  hash-formula / hash-wrong-size / idx-not-1-byte / idx-out-of-range /
  wrong-arity / cross-script-composition / depth-bound-stops-cycle,
  plus determinism.

  The depth-bound test stages a cycle rather than compiling one — the
  inline-hash rule means EasyFL cannot express one — by registering a
  script in the cache under a hash that is not its content hash. It
  asserts the exact number of dispatch frames for several commitment
  sizes. A regression there does not fail the test; it exhausts the
  stack and kills the test process.

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

3. **Descriptors live in upgrade0 (genesis).** The definitions are
   JSON since the easyfl JSON cutover. If we later decide to defer to a
   post-genesis upgrade, the change is moving the two entries from
   `def_embed0.json` to a new `def_embed1.json` and registering a
   corresponding `resolveEmbeddedUpgrade1` in `def_embed.go`.

4. **Decompile / pretty-printing.** `redeemScript(...)` decompiles
   via easyfl's normal path because the symbol is in the library.
   The validate-time error-path string `_constraintName` falls back
   to `constraint_call_prefix(...)` for unknown prefixes; for
   `redeemScript` and `callRedeemer` we currently get that fallback
   in the rare error case where a constraint fails. If this becomes
   noisy in production logs, the right fix is to teach
   `_constraintName` to consult easyfl's `FunctionNameByCallPrefix`
   — out of scope here.

5. **Three easyfl bugs found here, all fixed upstream** in easyfl
   `5cdef3b` and pinned by this repo on 2026-09-04.

   - **Panics inside a redeemed script were swallowed.**
     `LocalScript.Eval` and `LocalScript.EvalInPool` declared unnamed
     results while assigning to `err` from a deferred `recover`, so the
     assignment never reached the caller and the call returned
     `(nil, nil)`. Every nested failure — "not redeemed", "fnIdx out of
     range", arity mismatch, dispatch depth — degraded to an empty
     value. It was fail-closed in the ledger, since `runTuple` and
     `validateTxLevelConstraints` both treat an empty constraint result
     as a failure, but the message was lost. Naming the results is what
     lets the dispatch-depth error of §3.3 reach a log at all.

     Note this changed which transactions are valid. An in-script panic
     used to yield an empty value that a surrounding `or(...)` could
     absorb, leaving the transaction valid; now it aborts the
     constraint. Like the §3.2 relaxation it is a semantic change with
     no library-hash change.

   - **`LocalScriptFromBytes` aliased the caller's slice.** It cloned
     the wire form into `s.bin` but built the expression trees over the
     bytes it was handed, so a cached script pointed into memory its
     caller still owned. It now parses the clone. The copy in
     `evalRedeemScript` is no longer load-bearing, though it is cheap
     and stays as a statement of what the cache requires.

   - **`slicepool.AllocData` truncated silently.** It converted
     `len(data)` to the `uint16` size argument of `Alloc`, which wrapped
     above 65535 and returned a short buffer. It now refuses the
     allocation. `repeat` was the reachable case — it builds its result
     on the heap first, so a large enough count produced a quietly
     truncated answer. `concat` was never affected: it appends into an
     under-allocated slice, which grows correctly on the Go heap. The
     bitwise builtins still index past a truncated `Alloc` result and
     panic with a runtime message rather than a clear one; harmless, but
     unfixed.

     Do **not** widen the allocator to make oversize values work. The
     64 KiB ceiling is currently the only thing bounding how far
     `repeat` can amplify a few bytes of bytecode, and EasyFL has no
     step counter to replace it.
