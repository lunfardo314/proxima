# EasyFL Architecture Reference for Proxima Developers

> **QUEUED → `ledger/def/easyfl.md`** — Proxima-specific EasyFL internals: builtins, the embed0 resolver, debugging findings. Not a language guide.
> Rewritten there, then archived. See `claude/kb_reorg.md`.

Practical findings from debugging and developing with the EasyFL scripting engine.
This is NOT a user guide — it documents internal mechanisms that matter when writing
or debugging Proxima constraints.

## Library Compilation Pipeline

### How constraint functions get compiled

All EasyFL functions (locks, chain constraints, etc.) are compiled in a **single batch**
during library initialization. The pipeline in `addExtendedBatch()` has 4 phases:

1. **Phase 1 (Stub)**: Register function stubs with nil `embeddedFun` for all new functions.
   This allows forward references — any function can reference any other in the same batch.

2. **Phase 2 (Compile)**: Compile all function sources to bytecode. At this stage, function
   codes are resolved by name but the executable closures don't exist yet.

3. **Phase 3 (Cycle check)**: DFS traversal of the call graph extracted from bytecode.
   Detects direct, mutual, and indirect recursion.

4. **Phase 4 (Bind)**: Topological sort (Kahn's algorithm), then for each function in
   dependency order: build expression tree from bytecode, create executable closure
   (`makeEmbeddedFunForExpression`), assign to function descriptor.

### Key invariant

In Phase 4, when `ExpressionFromBytecode` builds the expression tree for function F,
every function that F calls must already have its `embeddedFun` set. The topological
sort guarantees this. If violated, the expression tree captures a nil `EmbeddedFunction`,
causing a nil pointer dereference at runtime.

### How Proxima loads constraints

In `def_upgrade0.go`, `upgrade0()` does:

```go
// Stage 1: YAML sources (embedded functions processed immediately, extended → pending batch)
lib.IntroduceUpdateYAMLMulti(resolver, yamlSources...)

// Stage 2: EasyFL source files (all appended to same pending batch)
lib.IntroduceUpdateManyMulti(
    addressED25519ConstraintSource,  // sigLock, unlockedByReference, etc.
    timelockSource,
    amountsSource,
    ...
    _miscCalculationsSource,         // storageDeposit, selfRequireEnoughStorageDeposit
)

// Stage 3: Process entire batch
lib.CommitUpdate()
```

All functions from all sources land in a single `pendingBatch`. The order of sources
in `IntroduceUpdateManyMulti` does NOT matter — the topological sort in `CommitUpdate`
determines the correct compilation order. Cross-source dependencies are fully supported.

## Runtime Evaluation

### Two levels of expression trees

When a constraint is evaluated at runtime, there are two levels:

1. **Fresh tree**: `MustEvalFromBytecodeWithSlicePool` calls `ExpressionFromBytecode(bytecode)`
   to build a temporary expression tree from the constraint's raw bytecode. This tree is
   disposed after evaluation.

2. **Precompiled trees**: Each user-defined function's closure (`makeEmbeddedFunForExpression`)
   captures its expression tree from Phase 4. These live as long as the library.

When the fresh tree encounters a user-defined function call (e.g., `sigLock`), it looks up
the function's `embeddedFun` from the library descriptor. The closure then evaluates using
its precompiled tree.

### Lazy evaluation

ALL EasyFL functions lazy-evaluate their arguments, including `and()`, `or()`, `if()`,
and `require()`. Arguments are only evaluated when explicitly accessed via `par.Arg(n)`.

- `and(A, B, C)`: evaluates A; if nil/empty, returns nil without evaluating B or C
- `or(A, B)`: evaluates A; if non-nil, returns A without evaluating B
- `if(cond, yes, no)`: evaluates cond, then either yes or no (never both)
- `require(cond, msg)`: evaluates cond; only evaluates msg on failure

This matters for `sigLock`: on consumed outputs, `and(selfIsProducedOutput, ...)` short-circuits
so the produced-only constraints (like storage deposit) are never evaluated.

### User-defined function calls

When a user-defined function `foo(arg1, arg2)` is called:

```go
// In makeEmbeddedFunForExpression closure:
varScope[0] = newCall(par.args[0].EvalFunc, par.args[0].Args, par.evalContext)
varScope[1] = newCall(par.args[1].EvalFunc, par.args[1].Args, par.evalContext)
// Arguments are NOT evaluated yet — just wrapped as lazy calls
retp := evalExpression(glb, spool, expr, varScope)
// $0, $1 in the body evaluate the lazy calls on demand
```

## Expression and Call Object Pooling

EasyFL uses `sync.Pool` for `Expression` and `call` objects:

- `newExpression` / `disposeExpression`: pools `Expression` structs. `disposeExpression`
  zeroes the struct before returning to pool.
- `newCall` / `disposeCall`: pools `call` structs. Similarly zeroed.
- `newVarScope` / `disposeVarScope`: pools `[]*call` slices for function argument scopes.

The precompiled expression trees from Phase 4 are never disposed — they're permanent.
Only the fresh trees from runtime `ExpressionFromBytecode` calls are disposed after use.

## Error Messages

EasyFL `!!!underscored_names` in source code are displayed with spaces at runtime:
- Source: `!!!locks_must_be_at_lockConstraintIndex`
- Runtime error: `"locks must be at lockConstraintIndex"`

Use `util.RequireErrorWith(t, err, "locks must be at lockConstraintIndex")` in tests.

## Constraint Evaluation Context

Each constraint is evaluated with an `EvalContext` that provides:

- `path`: byte path to the current constraint in the transaction tree (e.g., `tx.out[1].constraint[1]`)
- `tree`: the transaction's tuple tree
- `SelfOutputBytes()`: returns bytes of the output containing this constraint
  (uses `path[:len(path)-1]` to navigate to the parent output)
- `selfIsProducedOutput` / `selfIsConsumedOutput`: determined by the path prefix

The constraint bytecode is the raw bytes stored in the output tuple. The bytecode is
parsed into an expression tree each time the constraint is evaluated (with pooling for
the intermediate objects).

## Topological Sort: Why Not sort.Slice

The topological sort in `addExtendedBatch` Phase 4 uses Kahn's algorithm, not `sort.Slice`.

`sort.Slice` requires a **strict weak ordering** which demands transitivity of
incomparability. A dependency graph's partial order violates this: an unrelated function E
can be incomparable with both a dependency D and its dependent F, while D < F holds.
With ~200 functions in a batch and only sparse dependency edges, `sort.Slice` can place
a function before its own dependency, leaving a nil `EmbeddedFunction` in the expression tree.

Kahn's algorithm (BFS from zero-in-degree nodes) correctly handles partial orders
and guarantees that every dependency is processed before its dependents.
