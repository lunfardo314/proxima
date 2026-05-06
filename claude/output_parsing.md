# Output parsing refactor

## Motivation

`OutputFromBytes(data, validateOpt...)` currently always does the
*full* parse via `OutputFromBytesMainWithLib`:

- Decodes the outer tuple.
- Reads elements 0/1/2 from the tuple.
- Calls `AmountsFromBytes` (no library needed).
- Calls `LockFromOutputElementsWithLib` (library needed —
  prefix→name dispatch).

`validateOpt` runs *after* this full parse, so it can never opt out
of the lock dispatch. That made sense before the latest refactor when
the Lock was an inseparable piece of the output's identity. After
splitting lock data between elements 1 and 2 (Phase B'), and after
the planned new `get_outputs` client path (which ships raw bytes over
the wire and parses them on the consumer side), the eager lock parse
is dead weight in many cases:

- An API client receiving raw output bytes for display only needs
  amounts + raw bytecodes, not a typed Lock value.
- A trie iterator that just wants to filter by `lock_type` only needs
  the bytecode prefix, not a fully-typed Lock.
- A wallet listing UTXOs by amount needs `Amounts`, not Lock.
- The lock prefix→name dispatch is the only step that needs the
  ledger library; everything else is pure tuple parsing.

We want a small library-free fast path for "I have raw bytes, give me
something I can index into," with opt-in heavier validation/parsing
hooked through `validateOpt` and on-demand methods on `*Output`.

## Target shape

### Base parse — library-free

```go
// OutputFromBytes does a structural-only parse of an output.
// Without validateOpt it does NOT require the ledger library to be
// initialised. It checks:
//   - the outer tuple decodes
//   - element 0 (amounts) is present and decodes as a sub-tuple
//   - element 1 (index-values) is present and decodes as a sub-tuple
//     (empty bytes also accepted — see IndexValuesFromBytes)
//   - element 2 (lock bytecode) is present (NOT decoded)
// validateOpt funcs run after the structural check and can pull in
// heavier parsing (amounts decode, lock dispatch, …) — including
// library-dependent steps.
func OutputFromBytes(data []byte, validateOpt ...func(*Output) error) (*Output, error)
```

Internally:
1. `tuples.TupleFromBytes(data, 256)` — outer decode.
2. Wrap as `*Output`.
3. Verify `NumElements() >= 3` (`amounts | index-values | lock` — anything fewer is malformed by the post-Phase-B' invariant).
4. Verify element 0 parses as a sub-tuple (cheap structural check).
5. Verify element 1 either is empty (no index entries — accepted
   by `IndexValuesFromBytes`) or parses as a sub-tuple. Symmetric
   with the element-0 check; fails fast on corruption.
6. Run each `validateOpt(ret)` in order; first error wins.

No `L(base.MaxSlot)` call. No lock dispatch. No amount-element-by-
element decode (that lives behind `WithAmountsParsed()`).

### Validation hooks

Library of pre-built `func(*Output) error` factories living next to
`OutputFromBytes`:

- `WithAmountsParsed()` — runs `AmountsFromBytes` (validates each
  amount element decodes as uint64). Library-free.
- `WithIndexValuesParsed()` — runs `IndexValuesFromBytes`. Library-
  free.
- `WithLockParsed()` — runs `LockFromOutputElements` against
  `L(base.MaxSlot)`. Library-dependent.
- `WithLockParsedAt(lib *Library)` — same but with explicit library.
- `WithFullValidation(lib *Library)` — composite: amounts + index-
  values + lock.

Callers that need the old "fully-parsed" semantics use
`OutputFromBytes(data, WithFullValidation(L(base.MaxSlot)))` or the
shorter `WithFullValidation(nil)` (defaults to MaxSlot).

### On-demand `*Output` methods

Today's eager-parse methods (`Amounts()`, `IndexValues()`, `Lock()`)
panic on error via `AssertNoError`. For a base parse that didn't run
the lock validation, calling `Lock()` on a malformed output today
already panics — surprising. New shape:

- **Library-free** (no behaviour change, just clarification):
  - `Amounts() Amounts` — parses on every call (cheap), panics on
    malformed bytes.
  - `IndexValues() [][]byte` — same.
- **Library-dependent**:
  - `Lock(lib *Library) Lock` — explicit library.
  - `LockLatest() Lock` — convenience using `L(base.MaxSlot)`. Same
    panic-on-error semantics.
- Panic helpers stay as-is. The outer ring (HTTP handler, RPC
  boundary, CLI entry point) is expected to recover panics
  alongside other errors — so a malformed-bytes panic from
  `o.Amounts()` becomes just one more failure mode the outer ring
  already handles. Internal callers that have already run
  `WithFullValidation` continue to use the panic helpers without
  added churn.
- **Error-returning siblings** are *not* added in this refactor.
  Untrusted-input callers (the new `get_outputs` server, future
  third-party Go clients) call `OutputFromBytes(data, …)` with the
  validation hook they need; if they want defensive error returns
  later, add the `*E` siblings then.

### Functions to retire / rename

- `OutputFromBytesMain` and `OutputFromBytesMainWithLib`: callers
  collapse to `OutputFromBytes(data, WithFullValidation(lib))` plus
  on-demand `o.Amounts()` / `o.LockLatest()`. Then both delete.
- `OutputFromBytesWithLib`: rename to keep the explicit-library
  variant (`OutputFromBytesWithLib(data, lib, validateOpt...)`),
  documented as "use only when you also need a library-bound
  validateOpt".

## Caller migration

Callers fall into three buckets:

1. **Want only structural parse + raw element access** (e.g. API
   server hydrating outputs to ship over the wire): switch to
   `OutputFromBytes(data)` — no opts. No lib init required.
2. **Want amounts + raw lock bytes** (e.g. trie filter by lock
   prefix, balance summing): `OutputFromBytes(data,
   WithAmountsParsed())`. Still no lib.
3. **Want a fully-typed Lock value** (e.g. transaction validation,
   pretty-print): `OutputFromBytes(data, WithFullValidation(lib))`,
   or use `o.LockLatest()` on demand later.

Most ledger-internal call sites stay in bucket 3. The new client-side
output-parsing path (proxi, future `get_outputs` consumers) sits in
bucket 1 or 2.

## Open questions

- After `OutputFromBytesMain*` are gone, the "main parts" return
  shape (`(*Output, Amounts, Lock, error)`) disappears. Any internal
  caller relying on getting all three back in one allocation will
  do two extra `o.Amounts()` / `o.LockLatest()` calls. Re-parse
  cost: `Amounts()` is one tuple decode + per-element uint64
  validate; `Lock()` is one tuple decode + dispatch. If a hot path
  shows up in profiles, consider a lazy cache on `*Output` —
  out of scope for this refactor (and clashes with
  `feedback_cache_and_refcount.md`'s "no caches of mutable state"
  rule, though `*Output` is immutable so it might be fine).

## Phasing

1. **Add the new shape** alongside the old: `OutputFromBytes` becomes
   structural-only; `WithAmountsParsed` / `WithLockParsed` /
   `WithFullValidation` factories land. Old `OutputFromBytesMain*`
   stay as thin wrappers calling `OutputFromBytes(data,
   WithFullValidation(lib))` and unpacking.
2. **Migrate callers** one bucket at a time. Track call sites that
   are still using `OutputFromBytesMain*` or are unnecessarily
   pulling in the lock parse.
3. **Delete `OutputFromBytesMain`/`OutputFromBytesMainWithLib`**.
   Tighten the docstring on `OutputFromBytes` to make the lib-free
   contract explicit.
4. **Optional follow-up**: introduce error-returning siblings on
   `*Output` if a real consumer needs them (defer until then).

This is independent of the `get_outputs` endpoint refactor but
unlocks the lib-free client path that endpoint's design assumed.
