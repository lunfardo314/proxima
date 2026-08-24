# EasyFL in Proxima

**This is not a guide to the language.** EasyFL itself is documented on the
public docs site (`txdocs/easyfl.md`). This file covers what is Proxima-specific:
how the constraint library is assembled, where Go-embedded functions come from,
and the mechanisms that have caused real bugs.

## How the library is assembled

`def_upgrade0.go` builds the whole constraint library in three stages:

1. **JSON sources** — `easyfl.IntroduceUpdateJSONMulti(lib, resolver, …)` over
   the files in this directory: constants, embedded-function declarations,
   general and helper functions. Embedded functions are processed immediately;
   extended (EasyFL-bodied) ones go into a pending batch.
2. **EasyFL source files** — `lib.IntroduceUpdateManyMulti(…)` appends every
   `.easyfl` file to the *same* pending batch.
3. **`lib.CommitUpdate()`** — processes the batch as a whole.

Because everything lands in one batch, **the order of sources does not matter**.
Cross-source dependencies are fine. A topological sort at commit time determines
the compilation order.

### The invariant that sort protects

When the expression tree for a function `F` is built, every function `F` calls
must already have its embedded function resolved. Violate it and the tree
captures a nil embedded function, which is a nil dereference at evaluation time —
far from the cause.

The sort is **Kahn's algorithm, not `sort.Slice`**, and that is deliberate.
`sort.Slice` requires a strict weak ordering, which demands that incomparability
be transitive. A dependency graph does not satisfy that: an unrelated function
`E` can be incomparable with both `D` and `F` while `D < F` holds. With roughly
two hundred functions and sparse dependency edges, `sort.Slice` will sometimes
place a function before its own dependency. Do not "simplify" it back.

## Go-embedded functions

Constraints are written in EasyFL wherever they can be. Some cannot be, and those
are implemented in Go: declared in `def_embed0.json` with an `embeddedAs` name,
and wired to the implementation through the resolver map in `ledger/def_embed.go`.
Resolvers are searched newest-upgrade-first, falling back to base EasyFL.

Reach for Go only when EasyFL genuinely cannot do the job:

* aggregation across arbitrary positions in many outputs — `token(...)`,
  `tokenAmount(...)`, `redeemScript`;
* arithmetic needing Go-level overflow handling;
* anything touching the per-transaction context cache;
* cryptographic primitives.

The crypto primitives — `blake2b` and `validSignatureED25519` — live in
`ledger/crypto_builtins.go`. They used to be base-EasyFL builtins and were moved
here once nothing else in easyfl needed them.

Everything else belongs in the constraint's own EasyFL body, the way `chain()`
enforces ChainID preservation. The constraint layer is authoritative; the
transaction builder follows it, and duplicating a rule as a Go assertion in the
builder is not a safety net but a second thing to drift.

## Evaluation

A constraint's bytecode is stored raw in the output tuple and parsed into an
expression tree **on each evaluation**, with `sync.Pool` reuse for the
intermediate `Expression`, `call` and variable-scope objects. The precompiled
trees built at library-commit time are permanent and never disposed; only trees
built at runtime from bytecode are.

Each constraint is evaluated against an `EvalContext` carrying the path to the
constraint within the transaction tree — which is what makes
`selfIsProducedOutput` and `selfIsConsumedOutput` work, and how
`SelfOutputBytes()` reaches the containing output. Evaluation is lazy: an
argument is evaluated only when its value is actually needed.

## Things that bite

**Renaming a symbol is a hardfork.** Public symbol names are hashed into the
library hash. Renaming one changes the ledger identity and invalidates every
existing snapshot and database. Source comments are not hashed; symbols are.

**`isZero(0x)` is true.** To test for *empty* bytes, use
`equal(len(x), u64/0)` — `isZero` on empty data does not mean what it looks
like.

**`!!!underscored_names` render with spaces at runtime.** A failure written as
`!!!inputs_cannot_contain_duplicates` appears as
`"inputs cannot contain duplicates"`. Match on the spaced form in tests.

**Widening a compressed integer needs care.** Prefer `uint8Bytes(x)` when
comparing with `lessThan`, rather than assuming a width.
