# `proxi db txstore audit <slot>` — design spec

## Goal

A standalone CLI tool that walks the past cone of all branches in a starting
slot, all the way back as far as the local txstore allows, and reports on
completeness, validation correctness and validation throughput. Optionally,
it copies the live (non-orphan) past cone into a fresh txstore DB so the user
can prune orphans.

Three independent purposes, served by the same traversal:

1. **Completeness audit** — confirm the local txstore can reconstruct the
   ledger past back to genesis (or report the missing dependencies).
2. **Performance benchmark** — measure full-context validation cost per
   transaction / per UTXO over a real, contiguous segment of history.
3. **Garbage collection** — produce a clean output txstore that contains only
   transactions reachable from the chosen slot's branches; everything in the
   source txstore that wasn't visited is, by definition, orphaned.

## CLI

```
proxi db txstore audit <slot from> [<slot back to, default slot 0>] [-V|--validate] [-o|--output <new-db>] [-m|--meta]
``` 

| Flag                    | Meaning                                                                                                                                                                                                                                |
|-------------------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `<slot from>`           | Starting slot. Required positional arg. The tool reads all transactions in this slot, picks the branch transactions, and traverses backward from there.                                                                                |
| `<slot nack to>`        | Oldes slot to traverse. Optional positional arg. The tool reads all transactions back to this slot. Default is slot 0, genesis                                                                                                         |
| `-V`, `--validate`      | Run full-context validation (`SetFullContext` + `ValidateFullContext`) on every visited transaction. Skip — and report — any transaction whose consumed UTXOs are not all available locally. **Shorthand is upper-case `-V`** because the proxi root reserves lower-case `-v` for `--verbose`.                                       |
| `-o`, `--output <path>` | Write each visited transaction into a new Badger txstore at `<path>` (same on-disk layout as a normal `proximadb.txstore` — just cleaned of orphans). Refuses to start if the path already exists.                                     |
| `-m`, `--meta`          | Only meaningful with `--output`. If set, the per-transaction metadata is copied verbatim from the source. If unset (default), the output DB stores each tx with **empty metadata** (`txmetadata.TransactionMetadata{}.Bytes()` prefix). |

Absence of flags means txstore is just traversed and dependencies are checked, missing dependencies are reported, no more effect. 

Run conditions, like the rest of `proxi db txstore`, require the live node to
be **stopped** (Badger needs exclusive access). The tool opens the source
txstore read-only and the output DB read-write. 

The DB should be accessed via the txstore abstraction, in order to be able to replaced with say rockdb in the future.

**Multistate DB is opened only when `--validate` is set.** Without `-v`,
the tool never touches the state DB and never initialises the ledger
library — checking dependencies and writing to the output DB doesn't need
either. Phase 2 in that mode uses a minimal extractor that walks the raw
tuple tree directly (`tuples.TreeFromBytesReadOnly` + path lookups) so
that we can read input txids / endorsements / explicit baseline without
calling `transaction.Parse()` (which loads `ledger.L(slot)` for the
TxVersion check).

## Algorithm

The traversal uses a frontier set `C` (unprocessed) and a transient
`visited` set (processed, kept only as long as some future `C` entry might
reference its outputs). `visited` is pruned aggressively, so working-set
memory stays bounded by current DAG thickness — independent of how far
back we walk. Going to genesis with hundreds of millions of transactions
in a wide ledger is expected to fit in a few hundred MB.

### Phase 1 — find branches in the starting slot

Iterate the source txstore with prefix `Slot2Bytes(<slot from>)`. Keep
keys for which `txid.IsBranchTransaction()` is true. If none are found,
scan up to `auditPhase1FallbackSearch` slots downward for the first slot
containing any branch and prompt the user to use it instead.

### Phase 2 — frontier loop

State:

```
C       := frontier of *Transaction (not yet processed)
visited := frontier of *Transaction (recently processed; kept for input lookup)
branchesInC := map[slot]int   // running count of branch txs in C, per slot
floor   := <slot back to>     // 0 by default
```

Seed `C` with the parsed branches from Phase 1, plus the per-slot branch
counter for `<slot from>`.

While `C` is non-empty and `max(slot ∈ C) ≥ floor`:

1. Pick `T ∈ C` from the bucket at `max(slot ∈ C)` (any tx in that bucket).
2. **Discover deps.** For each input txid, endorsement, and explicit
   baseline of `T`:
   * if `dep.Slot() < floor`: skip (out of audit window).
   * if `dep ∈ C` or `dep ∈ visited`: skip.
   * else fetch tx bytes from source via
     `(*SimpleTxBytesStore).GetTxBytesWithMetadata`. If absent, **report
     missing on stdout** with the citing `T.ID` and continue. If present,
     parse:
     * with `transaction.Parse` if `--validate` (the parsed object is
       reused for the validation step),
     * with `transaction.ParseLibraryAgnostic` otherwise (no
       multistate/library dependency).
     Add to `C` and bump `branchesInC[dep.Slot]` if `dep` is a branch.
3. **Validate** (only if `--validate`). Check first that every input
   producer is in `C ∪ visited`; if any is missing (below floor / not in
   DB / parse-failed), count `valSkipped++`, log on stdout, skip
   validation. Otherwise time `SetFullContextWithFetch` +
   `ValidateFullContext` together, with a loader that resolves OutputIDs
   by looking up the producer in `C ∪ visited` and returning
   `producer.MustOutputDataAt(idx)`. Record the wall-clock duration and
   `NumInputs + NumProducedOutputs` per tx.
4. **Move T to visited.** Remove from `C`, insert into `visited`,
   increment `visitedTotal[category]` (category derived from `T.ID()`
   bits — branch / seq non-branch / non-seq).
5. **Output write** (only if `--output`). Append to a 1 000-record batch
   and flush via `PersistTxBytesBatch`. `--meta` controls metadata
   preservation (see flag table).
6. **Branch-slot completion.** If `T` is a branch, decrement
   `branchesInC[T.Slot]`; when it drops to zero, fire `onBranchSlotComplete`
   (next subsection).

When the loop exits, flush any pending output batch.

### Per-slot completion: in-DB scan, prune, progress

`onBranchSlotComplete(S)` runs the bookkeeping that makes the algorithm
streaming and bounded:

1. **Source-DB scan, key-only.** Iterate
   `Iterator(Slot2Bytes(S)).IterateKeys` and bucket each txid by category
   from its bits — never load the value bytes. Add the result to
   `seenInDBTotal`.
2. **Prune `visited`.** Recompute `maxC = max(slot in C)`. Delete every
   entry `X` from `visited` with `X.slot > maxC`: walking backward, no
   future `C` entry can reference such `X` (their slot ≤ maxC < X.slot,
   and deps are strictly older). If `C` is empty, drop `visited` entirely.
3. **Compressed progress line every `auditProgressInterval` (= 100)
   completed branch-slots.** One line, terse:
   ```
   slots A..B (Δ=100): visited N (br/seq/ns x/y/z), in-DB M, orphans M-N, |C|=… |V|=… | val 0.0301 ms/tx 0.0062 ms/UTXO
   ```
   The `val …` segment is added only with `--validate`. The window stats
   are deltas vs. the previous emit, so each line characterises the most
   recent 100 slots — independently usable for spot-checks.

Why couple slot-completion specifically to *branches* (and not "any tx at
the current max slot"): branches are the canonical "I have advanced past
slot S" event, since the chain of branches is what the audit is following
in the first place. After the initial fan-in (slot from with K parallel
branches), the past cone collapses to one branch per slot — so each
branch-slot-complete moment is a real reset point in the traversal.

### No separate orphan phase

Orphan counts are derived after the loop exits, by category, from the
accumulated totals:

```
orphans = seenInDBTotal − visitedTotal
```

The full source DB is **not** scanned end-to-end. We only iterate keys
within slots we actually traverse, paid one slot at a time at completion.
This keeps the audit usable on stores with hundreds of millions of keys.

### Phase 5 — final report

Single multi-section report on stdout. Example:

```
Past cone of slot 391000 (3 branches):
  visited transactions   : 412 879
  earliest reached slot  : 0   (genesis included)
  latest reached slot    : 391000
  missing dependencies   : 7   (listed below)
  parse errors           : 0

Orphans in source (slots ≥ 0, not reachable from slot 391000 branches):
  branch                 : 41
  sequencer non-branch   : 1 873
  non-sequencer          : 9 412
  total                  : 11 326   (2.7% of source)

Output:
  written to             : ./audit.txstore
  metadata               : preserved   (--meta)
  records written        : 412 879
  bytes written          : 287.3 MB

Validation (--validate):
  attempted              : 412 879
  succeeded              : 412 870
  failed                 : 2     (listed below)
  skipped (missing deps) : 7
  total time             : 12.46 s
  mean per tx            : 0.0302 ms
  p95 per tx             : 0.0710 ms
  max per tx             : 0.4815 ms
  mean per consumed+produced UTXO : 0.0062 ms
  throughput             : 33 137 tx/s, 160 256 UTXO/s

Missing dependencies (7):
  [349873|12]02ab… referenced by [349874|0br]…
  ... (one line each, deduplicated by missing-tx)

Validation failures (2):
  [349912|45]7f… : input commitment mismatch
  [350001|17]a3… : amount conservation violated
```

## Avoiding double-loading

Each tx that ends up in the past cone is parsed at most once. The traversal
parses on first encounter (the result is held in `C` until processed), the
optional validation step reuses that same `*Transaction`, and producer-tx
output lookups during validation hit either `C` or `visited` directly — no
re-parsing. Because `visited` is pruned aggressively past the
max-slot-in-`C` watermark, this stays bounded.

There is no LRU cache. The frontier itself is the cache: we keep parsed
producers alive exactly as long as the rest of the still-unprocessed
frontier might consume them, and not a moment longer.

## Edge cases / decisions

* **Multiple branches in the starting slot.** Expected, normal — the past
  cones merge backward in a few slots. We seed `C` with all of them and
  rely on `C`/`visited` membership to dedupe. The fan-in is reflected in
  the initial `branchesInC[<slot from>]` count.
* **Genesis.** Genesis is a branch transaction at slot 0. The frontier
  loop reaches it via baseline / chain-input links and stops naturally
  because it has no inputs or endorsements.
* **Output DB already exists.** Refuse to start. No `--force`. The point
  of this command is to produce a clean DB; overwriting is foot-gunny.
* **Missing dependencies, one tx referenced by many descendants.** Report
  the missing txid once on stdout when first seen; record up to
  `auditMaxMissingSamples` (5) referrers in the final report.
* **Validation failure.** Counts as failure, not as missing-dep. Listed
  separately. Tool exits 0 either way. Rationale: this is an audit tool
  — we want the full picture, not first-failure abort.
* **Validation skipped.** Happens when an input producer is below `floor`,
  missing from the source DB, or failed to parse. Logged with a `VAL SKIP`
  prefix on stdout while running and counted in the final report.
* **Memory.** Bounded by `|C ∪ visited|` ≈ DAG thickness near the current
  slot — typically a few thousand parsed txs at most. Independent of how
  far back we walk: a full-genesis audit on a 10⁹-tx ledger fits in the
  same working set as a 10⁵-tx audit.
* **`--validate` without `--output`** is the audit/perf use case.
  **`--output` without `--validate`** is the GC use case (faster — it
  skips validation entirely and uses `ParseLibraryAgnostic`, so it
  doesn't even open the multistate DB).

## Implementation pointers

* New file: `proxi/db_cmd/txstore/audit.go`. Register in
  `proxi/db_cmd/txstore/txstore.go` `Init()`.
* DB open / close: `glb.InitLedgerFromDB()`, `glb.InitTxStoreDB()`,
  `glb.CloseDatabases()` — same pattern as `crosscheck.go`.
* **Always go through the `txstore` package** — never poke the underlying
  Badger handle directly. The audit tool reads through
  `*txstore.SimpleTxBytesStore` (`GetTxBytesWithMetadata`, `Iterator`) and
  writes via `(*SimpleTxBytesStore).PersistTxBytesBatch`. The only place
  Badger leaks through is the `badger_adaptor.MustCreateOrOpenBadgerDB(...)`
  call used to open the output directory; that handle is immediately wrapped
  in `txstore.NewSimpleTxBytesStore(...)` and the Badger handle is not used
  again. This way the tool keeps working if/when the txstore is re-backed by
  RocksDB or another KV store.
* Output DB Badger options: match the live node's settings — block cache
  64 MB, index cache 32 MB, `NumCompactors = 2`.
* Iterating slot: `store.Iterator(base.Slot2Bytes(slot)).IterateKeys(...)`,
  filter on `txid.IsBranchTransaction()`. The `Iterator` method on
  `SimpleTxBytesStore` is already exposed (used by the DAG explorer).
* Phase-1 fallback (no branches in `<slot from>`): scan up to
  `auditPhase1FallbackSearch` (= 1024) slots downward, slot by slot, until
  one with a branch is found; prompt the user before adopting it.
* Tx parsing: `transaction.ParseLibraryAgnostic(txBytes)` when
  `--validate` is off (no multistate / no `ledger.L(slot)` access);
  `transaction.Parse(txBytes)` when `--validate` is on, since
  `ValidateFullContext` runs slot-specific EasyFL constraints.
* Looking up an output for the validation loader: lookup in `C ∪ visited`
  directly via `(*frontier).Get`. No external cache — the frontier *is*
  the cache.
* Branch detection: `txid.IsBranchTransaction()` (txid bits, no parse
  required) — used both for filtering branches in slot-prefix iteration
  and for slot-completion detection.
* In-DB key-only iteration: `Iterator(prefix).IterateKeys` — txid bits
  give the category; we never load tx bytes during the per-slot count.
* Stats struct kept locally in `audit.go` (`auditState`); final print via
  `util/lines.Lines` for consistency with the rest of the proxi output.
  Periodic progress lines are plain `glb.Infof` one-liners.

## Out of scope (for v1)

* Re-validating signatures during Phase 2.
* Following the multistate trie to discover txids that aren't in the past
  cone (that's `crosscheck`'s job, intentionally orthogonal).
* Parallel validation. Validation is single-threaded in v1 to make the
  reported timings unambiguous benchmarks. Parallelism would make them
  reflect goroutine scheduling, which isn't what we want from this command.
* `--from <other-txstore-db>` — letting the audit read from a different DB
  than the configured node DB. Easy to add if needed.

## Resolved decisions (2026-04-29)

* **Output DB layout** = "just another txstore, only cleaned up". Same
  on-disk Badger format as a regular `proximadb.txstore`, only tx entries,
  no extra keys. Already what the spec implies.
* **Orphan handling** = ignore for traversal, count by category for the
  report (Phase 4 above).
* **`--meta` / `-m` flag** added: default is to write transactions with
  empty metadata (matches the assumption that audited txs aren't going to
  be re-attached at the meta-level — they're being archived / GC'd). Pass
  `--meta` to copy source metadata verbatim.
