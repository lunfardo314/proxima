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
proxi db txstore audit <slot from> [<slot back to, default slot 0>] [--validate] [-o|--output <new-db>] [-m|--meta]
``` 

| Flag                    | Meaning                                                                                                                                                                                                                                |
|-------------------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `<slot from>`           | Starting slot. Required positional arg. The tool reads all transactions in this slot, picks the branch transactions, and traverses backward from there.                                                                                |
| `<slot nack to>`        | Oldes slot to traverse. Optional positional arg. The tool reads all transactions back to this slot. Default is slot 0, genesis                                                                                                         |
| `--validate`            | Run full-context validation (`SetFullContext` + `ValidateFullContext`) on every visited transaction. Skip — and report — any transaction whose consumed UTXOs are not all available locally. No shorthand: `-v` is the global verbose flag. |
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

### Phase 1 — find branches in the starting slot

Iterate the source txstore with prefix `Slot2Bytes(<slot>)`. Among all keys
returned, keep those for which `txid.IsBranchTransaction()` is true. 
If none are found, find the first non-empty earlier slot and ask user if proceed.

### Phase 2 — past-cone traversal (BFS)

Starting from the branch txids, BFS backward through the past cone using:

* every input's transaction ID (`tx.MustInputAt(i).TransactionID()`),
* every endorsement (`tx.MustEndorsementAt(i)`),
* the explicit baseline if present (`tx.ExplicitBaseline()`).

State:

```
visited := map[base.TransactionID]struct{}{}    // membership only — IDs
missing := []base.OutputID / []base.TransactionID  // categorised below
queue   := []base.TransactionID
parsedCache := lru.New(N)                       // optional, see "double-loading"
floor   := <slot back to>                       // 0 by default
```

For each popped `txid`:

1. If `txid.Slot() < floor`: skip silently (out of audit range — not the same
   thing as a missing dependency).
2. Skip if already `visited`.
3. `store.GetTxBytesWithMetadata(&txid)` (via the `txstore.SimpleTxBytesStore`
   handle, not the raw KV — see *Implementation pointers*).
   * If `len == 0`: record `txid` as a missing dependency, continue. **Do not
     fail.** This is the explicit "missing deps are ignored and reported" rule.
4. Mark `visited[txid] = {}`.
5. Extract dependencies. If `--validate` is set, use
   `transaction.Parse(txBytes)` (the parsed `*Transaction` is reused in
   Phase 3). Otherwise, run a minimal local extractor that calls
   `tuples.TreeFromBytesReadOnly(txBytes)` and reads `[TxInputIDs, i]`,
   `[TxEndorsements, i]`, and `[TxExplicitBaseline]` directly, skipping
   the library-dependent version check. If extraction fails: record as a
   corrupt-record error, continue.
6. If `--output`: append `(txid, valueBytes)` to a write batch and flush
   in batches of e.g. 1000 via `PersistTxBytesBatch`. `valueBytes` is:
   * `--meta` set:    `txBytesWithMetadata` (verbatim from source).
   * `--meta` unset:  `(*txmetadata.TransactionMetadata)(nil).Bytes() ||
     txBytes`, where `txBytes` is obtained via
     `txmetadata.SplitTxBytesWithMetadata(txBytesWithMetadata)`. A nil
     metadata serialises to a length-zero prefix, which is the standard
     "no metadata attached" form already understood by the txstore reader.
7. Push every input txID, every endorsement, and the baseline (if any) onto
   the queue.

Termination: queue empty. Natural stopping points:

* `floor == 0` (the default): genesis (no inputs / no endorsements), or any
  tip whose dependencies are all missing.
* `floor > 0`: any link that would cross below `floor` is dropped at the
  guard in step 1.

### Phase 3 — validation (only if `--validate`)

After Phase 2, sort the visited set by `(slot asc, tick asc)` and validate
in that topological-ish order. (Strictly topological isn't required for
correctness — the loader looks deps up by ID — but processing oldest-first
keeps the parsed-tx LRU warm for descendant lookups.)

For each visited txid in sorted order:

1. Reload `txBytes` from the source txstore. Parse with `Parse()`.
2. Build the loader:
   ```go
   loader := tx.InputLoaderByIndex(func(oid base.OutputID) ([]byte, bool) {
       // 1. parsed-tx LRU lookup
       // 2. fall back to txstore.LoadOutput
       // 3. on miss, return false → SetFullContext returns "missing input" err
   })
   ```
3. Time `tx.SetFullContext(loader)` + `tx.ValidateFullContext()` together
   (single wall-clock measurement per tx). If the loader returns "not
   found" for any input, count this tx as **skipped — missing dependency**
   instead of a validation failure.
4. Insert the parsed `*Transaction` into the LRU cache so descendants can
   reuse its outputs without re-parsing.

Stats accumulated in this phase:

* Validations attempted, succeeded, failed, skipped (missing deps).
* Total wall time across validations.
* Per-tx mean / median / p95 / max in nanoseconds.
* Per-UTXO mean — UTXOs counted as `NumInputs() + NumProducedOutputs()`,
  matching the live `proxima_tx_validation_num_utxo` metric so numbers are
  directly comparable to Prometheus data.
* Slot-by-slot rolling average (printed with progress; useful to see whether
  validation cost has changed across history).

### Phase 4 — orphan stats

Always run, regardless of flags. Single sequential pass over the source
store with `Iterator(nil)`:

```
for each k in source:
    txid := base.TransactionIDFromBytes(k)
    if txid.Slot() < floor: continue            // out of audit window
    if _, seen := visited[txid]; seen: continue // in past cone, not orphan

    switch {
    case txid.IsBranchTransaction():       orphan_branches++
    case txid.IsSequencerTransaction():    orphan_seq_nonbranch++
    default:                               orphan_nonseq++
    }
```

The orphans themselves are not enumerated or written anywhere — they are
implicitly defined as everything in the source DB that is not in `visited`
and falls within `[floor, +∞)`. Only the three counts are kept.

Why bother: an orphan-heavy txstore is the signature of a node that's been
chasing forks or has accumulated late-arriving txs that never made it onto
a baseline branch path. The category split (branches / seq / non-seq)
makes that signature interpretable at a glance.

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
  mean per tx            : 30.18 µs
  p95 per tx             : 71 µs
  mean per UTXO          : 6.24 µs
  throughput             : 33 137 tx/s, 160 256 UTXO/s

Missing dependencies (7):
  [349873|12]02ab… referenced by [349874|0br]…
  ... (one line each, deduplicated by missing-tx)

Validation failures (2):
  [349912|45]7f… : input commitment mismatch
  [350001|17]a3… : amount conservation violated
```

While running, the tool prints a one-line progress update every N visited
txs (default 50 000) showing slot reached + cumulative counts.

## Avoiding double-loading

The user requirement is "prevents double loading the transaction" during the
validate path. The interpretation:

* Each visited tx must be parsed at most twice across the whole run — once
  in Phase 2 (to extract links) and once in Phase 3 (to validate). This is
  unavoidable unless we keep all parsed objects in memory across phases,
  which is too expensive on a full-genesis run.
* During Phase 3, when validating tx X we must not re-parse the
  producing-tx of every input. Solution: a parsed-tx **LRU cache** (size
  configurable, default 50 000 entries) populated as we validate. When
  looking up an output for an input, hit the cache first; on miss, load and
  parse once and insert.
* The Phase 2 parse cannot meaningfully feed Phase 3 because Phase 2
  processes newest-first (BFS from branches) but Phase 3 processes
  oldest-first. Holding all Phase-2 parsed Txs alive between phases is the
  alternative; rejected on memory grounds.

If profiling shows Phase 2 is also a bottleneck, we can drop Phase 2's full
`Parse()` in favour of a hand-rolled "extract inputs + endorsements +
baseline" pass that skips the upgrade-index check and the produced-amount
allocation. Not in v1.

## Edge cases / decisions

* **Multiple branches in the starting slot.** Expected, normal — the past
  cones merge backward in a few slots. We BFS from all of them simultaneously
  and rely on `visited` to dedupe.
* **Genesis.** Genesis is a branch transaction at slot 0. Phase 2 reaches it
  via baseline / chain-input links and stops there because it has no inputs
  or endorsements.
* **Output DB already exists.** Refuse to start. No `--force`. The point of
  this command is to produce a clean DB; overwriting is foot-gunny.
* **Missing dependencies, one tx referenced by many descendants.** Report
  the missing txid once, with a small list of citing txids (cap at e.g. 5
  to keep the report bounded).
* **Validation failure.** Counts as failure, not as missing-dep. Listed
  separately. Tool exits 0 either way unless flag-driven (see `--strict`
  below if we ever add it). Rationale: this is an audit tool — we want the
  full picture, not first-failure abort.
* **Memory.** Worst case is a full-genesis run on a heavily populated
  ledger: the `visited` set is ~32 bytes per entry × N transactions. At
  10⁷ txs that's ~320 MB of in-memory map, acceptable for a one-off CLI.
  The LRU is bounded.
* **`--validate` without `--output`** is the audit/perf use case.
  **`--output` without `--validate`** is the GC use case (faster, doesn't
  pay the validation cost).

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
* Phase-1 fallback (no branches in `<slot from>`): walk the source store
  with `Iterator(nil)` until the first key whose slot < `<slot from>` and
  whose `txid.IsBranchTransaction()` is true; print it and prompt the user
  before continuing. Single sequential scan, bounded by the gap.
* Tx parsing: `transaction.Parse(txBytes)` for Phase 2 (no signature check
  needed); the same `Parse` plus `SetFullContext` + `ValidateFullContext`
  for Phase 3.
* Looking up an output for the loader: `txstore.LoadOutput(store, oid)` —
  but wrap it with the parsed-tx LRU so we don't re-parse the same producer
  on every consumer.
* Branch detection: `txid.IsBranchTransaction()` (already used in
  `dag_explorer/dag_explorer.go`).
* Stats struct kept locally in `audit.go`; final print via
  `util/lines.Lines` for consistency with the rest of the proxi output.

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
