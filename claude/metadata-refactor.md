# Refactor: move global ledger values into the stem constraint; remove persistent TxMetadata

## Status (2026-05-01)

Phases A → E are shipped. Eight commits on `develop08`:

| Commit     | Phase | What landed                                                                |
|------------|-------|----------------------------------------------------------------------------|
| `28ce40a1` | A1    | `healthyCoverageDelta` ledger function + Go wiring; sweep ~20 call sites    |
| `f04f1725` | A2    | `StemLock` carries 7 new aggregates; arity 2→9; genesis stem populated      |
| `f9cd47ce` | A3    | On-chain supply / coverage recurrences + healthiness in `lock_stem.easyfl` |
| `46d92647` | B     | Sequencer task plumbs past-cone-aware aggregates into the produced stem    |
| `929da629` | C     | `RootRecord` trimmed to {Root,SequencerID}; aggregates projected onto BD    |
| `eef76c04` | D     | `enforceStemValues` (baselineRoot panic, others warn) + Phase B baselineRoot |
| `ee2f19f6` | E     | Persistent TxMetadata removed; wire prefix gone; ~30 call sites swept       |

Open follow-ups (next session):
- **Phase A4** focused negative tests for the stem constraint (deferred; happy paths covered by `tests/`).
- **Phase D escalation** — the attacher's non-baselineRoot mismatches currently log; once proposer/attacher past-cone reconciliation lands, escalate to panic per §9.6.
- **Phase F** tooling/UI:
  - `proxi db info` / `db roots` already read via `BranchData` (works) — verify printout shows the new aggregates with the right labels.
  - `dag_explorer` per-vertex `CoverageDelta` / `Supply` fields are temporarily nil for non-branch vertices (TODO comment in `api/dag_explorer/dag_explorer.go`); for branch txs, wire them from the stem.
  - `api/streaming/dag_vertex_server.go` `VertexWithDependenciesExtended` likewise passes nil for these — fold in the stem read.
  - Update `CLAUDE.md` "Active Task" list with v0.8.x post-refactor pointers.
- **Cleanup leftovers** worth doing as small commits:
  - Remove the now-vestigial `TransactionMetadata` parameter from `txstore.PersistTxBytesWithMetadata` and rename to `PersistTxBytes`. The wrapper interface in `global` will need the rename too.
  - Drop the `_ = baselineSupply` shim in `sequencer/task/proposer.go`.
  - Re-evaluate whether the cache `branchDataWithLedgerCoverage` still earns its keep now that `Branches.LedgerCoverage` is a one-liner.
  - The `combineTxBytesWithMetadata` helper in `core/core_modules/txstore_writer/txstore_writer.go` is now a pass-through — inline and delete.
  - The `TxMetadata json.RawMessage` field on `api.TxBytes` / `api.TransactionJSONAble` is always nil; can be removed in a wire-format break.
- **Recurrence sanity assert is currently logging-only** for non-baseline mismatches in `enforceStemValues` — re-classify per §9.6 once proposer/attacher view drift is reconciled.



## 1. Goal

Several globally-deterministic values are currently kept in `multistate.RootRecord` (off-chain, in
the BadgerDB partition `rootRecordDBPartition`) and additionally propagated piggy-back-style via
`TransactionMetadata`. They are not part of the trie commitment, therefore they cannot be proven
cryptographically — which blocks bridging and any other use case that needs Merkle proofs of
ledger global state.

We move those values **into the stem output's `stemLock` constraint** so that they become part of
the UTXO state and inherit the trie's Merkle commitment. As a side effect, the persistent
`TransactionMetadata` becomes redundant and is removed.

A second motivation: today coverage is computed two ways depending on distance to the snapshot —
direct halving recurrence from the previous branch, or a 64-slot accumulating traversal in
`Branches.LedgerCoverage`. The two have produced small discrepancies near snapshot edges in the
past. After the refactor, the **local recurrence (enforced inside the constraint) is the only
way** coverage is computed in the system.

Values to move into `stemLock`:

| Value           | Current location                                | After refactor                             |
|-----------------|-------------------------------------------------|--------------------------------------------|
| Total supply    | `RootRecord.Supply`                             | `stemLock` arg                             |
| Coverage delta  | `RootRecord.CoverageDelta`                      | `stemLock` arg                             |
| Total coverage  | derived off-chain (`Branches.LedgerCoverage`)   | `stemLock` arg (stored explicitly)         |
| Slot inflation  | `RootRecord.SlotInflation`                      | `stemLock` arg                             |
| Frozen coverage | `RootRecord.FrozenCoverage`                     | `stemLock` arg (kept as "trustless stats") |
| Num transactions | `RootRecord.NumTransactions`                   | `stemLock` arg (kept as "trustless stats")  |
| Baseline root   | (only inside `RootRecord` of the predecessor)   | `stemLock` arg (predecessor's state root)  |

Survivors in `RootRecord`:

- `Root` — current state's trie root (cannot live inside its own state → cyclical)
- `SequencerID` - used for quick look-up of the chained output in the state

The state root is stored on the **successor** stem, not on its own (i.e. stem `N+1` carries
`baselineRoot = root_N`). This breaks the cyclic-commitment problem and gives a Merkle-provable
chain of state roots.

## 2. Existing topology (recap)

Locations confirmed by analysis (file:line):

- `core/txmetadata/txmetadata.go:19-31` — `TransactionMetadata` definition (StateRoot, CoverageDelta,
  FrozenCoverage, LedgerCoverage, SlotInflation, Supply + non-persistent `SourceTypeNonPersistent`,
  `TxBytesReceived`).
- `core/txmetadata/txmetadata.go:103-213` — flag-based serialization, length-prefix split.
- `txstore/txstore.go:78-116` — `PersistTxBytesWithMetadata` writes `metadataBytes || txBytes`
  per txid; this is the only persistence site.
- `ledger/multistate/state.go:54-72` — `RootRecord` struct.
- `ledger/multistate/state.go:582-639` — `RootRecordParams` + `Updatable.Update()` writes the root
  record alongside the trie commit, atomically.
- `ledger/multistate/roots.go:92-141` — `RootRecord.Bytes()`/`RootRecordFromBytes()` (7-element
  tuple format).
- `ledger/lock_stem.go:20-25` — current `StemLock` struct: only `PredecessorOutputID` + `VRFProof`.
- `ledger/def/lock_stem.easyfl` — current EasyFL definition (2 args, length-checks first arg as 33
  bytes / OutputID; on consumed branch enforces stem chaining + VRF; on produced branch enforces
  index, zero-amount, and `_enforceBranchCoverageBounds`).
- `core/attacher/wrapup.go:17-88` — populates `finals.TransactionMetadata`, builds
  `RootRecordParams`, hands them to `branches.AddPendingBranch`.
- `core/attacher/attacher.go:650-665` — off-chain halving formula
  `total_coverage = LedgerCoverage(baseline) >> (slot_diff) [+ delta]`.
- `core/core_modules/branches/branches.go:178-235` — `LedgerCoverage()` traverses up to ~64 slots
  back, accumulating `CoverageDelta >> slotsBack`. Cache of `RootRecord`s per branch.
- `core/attacher/check.go:99-116` — `checkConsistencyWithMetadata()` — currently only **warns**
  on mismatch.
- EasyFL helpers available for the new constraint: `consumedConstraintByIndex($idx,$blk)`,
  `consumedOutputByIndex($idx)`, `inputIDByIndex`, `parseInlineDataArgument`,
  `lshift64`/`rshift64`, `add`, `sub`, `mul`, `lessThan`, `equal`, `txSlot`, `txStemOutputIndex`,
  `selfIsConsumedOutput`, `selfIsProducedOutput`, `slotFromOutputID` (or equivalent).
- `global.FractionHealthyBranch` — currently a Go-only fraction (numerator/denominator) used by
  `IsHealthyCoverageDelta`. After the refactor, the same numerator/denominator must be exposed as
  ledger constants for use inside the stemLock constraint, and Go code must read them from the
  ledger (single source of truth).

## 3. New `stemLock` shape

All integer values are encoded in EasyFL **compressed `z64/` form** (variable width, up to 8
bytes). `mustValidUint64` (or equivalent) replaces `mustSize($i, 8)` because the on-wire width
varies. Declared widths in the table below describe the maximum / decoded size.

| arg | name              | type/bytes  | semantics                                                     |
|-----|-------------------|-------------|---------------------------------------------------------------|
| $0  | `predOutputID`    | 33          | as today                                                      |
| $1  | `vrfProof`        | var         | as today                                                      |
| $2  | `totalSupply`     | z64 (≤8)    | Σ tokens on this branch's state                               |
| $3  | `totalCoverage`   | z64 (≤8)    | accumulated halving sum at this slot                          |
| $4  | `coverageDelta`   | z64 (≤8)    | sum of consumed amounts on this branch's past cone (incl. frozen) |
| $5  | `frozenCoverage`  | z64 (≤8)    | frozen part of `coverageDelta` (trustless stats)              |
| $6  | `slotInflation`   | z64 (≤8)    | Σ inflation amounts in this branch's past cone                |
| $7  | `numTransactions` | z32 (≤4)    | new tx count in the branch's past cone                        |
| $8  | `baselineRoot`    | 24          | trie root of the **predecessor** branch (24-byte blake2b-192) |

Notes:

- `baselineRoot` uses 24 bytes (consistent with commit `9d9ce576` switching trie commitment to
  `HashSize192`). Confirm width with `ledger.CommitmentModel.NewVectorCommitment().Bytes()`.
- Field widths for compressed ints are not asserted with `mustSize`; instead use a uint-decoding
  helper that bounds-checks ≤ 8 bytes.
- Genesis stem (see §9.3): `totalCoverage = totalSupply`, all deltas (`coverageDelta`,
  `frozenCoverage`, `slotInflation`, `numTransactions`) = 0, `baselineRoot` = 24 zero bytes.

## 4. EasyFL constraint logic

In addition to the current checks, the produced branch of `stemLock` must enforce the recurrence
plus a **healthy-coverage check** (no unhealthy branches can be sealed by stemLock).

Healthy fraction is parameterized by two new ledger constants — defaults numerator=7, denominator=12:

```
func constHealthyCoverageNumerator   : u64/7
func constHealthyCoverageDenominator : u64/12
```

The healthiness predicate itself is exposed as an EasyFL function so it can be called both from
the stemLock constraint and from Go (via the precompiled-function path):

```
// returns true iff the branch is healthy:
//   coverageDelta * denominator > supply * numerator
//   (i.e. coverageDelta > (numerator/denominator) * supply, matching the
//   existing 7/12 threshold without integer-division rounding loss)
func healthyCoverageDelta :
    lessThan(mul($0, constHealthyCoverageNumerator),
             mul($1, constHealthyCoverageDenominator))
```

The Go-side `global.FractionHealthyBranch` and `global.IsHealthyCoverageDelta` are repointed to
read from these ledger definitions (single source of truth — every reference in the rest of the
codebase routes through the ledger; `IsHealthyCoverageDelta` becomes a thin wrapper that calls
the precompiled `healthyCoverageDelta`). The constants are configurable via ledger upgrade like
other ledger params.

Constraint sketch (produced branch):

```
// helpers — read fields from this stem (produced) and predecessor stem (consumed input at selfOutputIndex)
totalSupply       = parseInlineDataArgument(producedStemLockOfSelfTx, 2)
totalCoverage     = parseInlineDataArgument(producedStemLockOfSelfTx, 3)
coverageDelta     = parseInlineDataArgument(producedStemLockOfSelfTx, 4)
frozenCoverage    = parseInlineDataArgument(producedStemLockOfSelfTx, 5)
slotInflation     = parseInlineDataArgument(producedStemLockOfSelfTx, 6)

_predTotalSupply   = parseInlineDataArgument(consumedConstraintByIndex(selfOutputIndex,1), 2, #stemLock)
_predTotalCoverage = parseInlineDataArgument(consumedConstraintByIndex(selfOutputIndex,1), 3, #stemLock)

K = sub(txSlot, slotFromOutputID(inputIDByIndex(selfOutputIndex)))

// supply recurrence
require(equalUint(totalSupply, add(_predTotalSupply, slotInflation)),
        !!!supply_inconsistent_with_predecessor)

// total-coverage recurrence (the only formula in the system)
require(equalUint(totalCoverage, add(rshift64(_predTotalCoverage, K), coverageDelta)),
        !!!total_coverage_inconsistent_with_predecessor)

// trustless-stats sanity: strict <
require(lessThan(frozenCoverage, coverageDelta),
        !!!frozen_coverage_must_be_strictly_less_than_coverage_delta)

// branch must be healthy (delegates to the EasyFL function declared above):
//   coverageDelta * denominator > supply * numerator
require(healthyCoverageDelta(totalSupply, coverageDelta),
        !!!branch_unhealthy)
```

Bootstrap-chain exemption: the new healthiness check **inherits the existing exemption** from
`_enforceBranchCoverageBounds` — the bootstrap chain is exempt, same as today. See §9.7.

What EasyFL **cannot** verify (must remain in Go):

1. `slotInflation`, `coverageDelta`, `frozenCoverage`, `numTransactions` truly equal the sums over
   the past cone — these depend on transactions outside the consumed inputs.
2. `baselineRoot` equals the actual trie root of the predecessor branch — the trie root is
   provided by the validator's baseline state; not an EasyFL-visible value. Confirmed §9.4 to
   keep this as Go-only enforcement: state root is a consensus value among many nodes, so it is
   trust-less by independent recomputation.

Both are enforced in Go inside the attacher post-wrap-up (see §6). The ledger as a system
enforces consensus on the values: any node whose recomputation disagrees with the on-stem value
is out of consensus.

## 5. RootRecord trim and `BranchData` projection

**Persistent `RootRecord`** shrinks to:

```go
type RootRecord struct {
    Root            common.VCommitment
    SequencerID     base.ChainID
}
```

`NumTransactions` is removed from `RootRecord` — it lives only on the stem now.

**`BranchData` (in-memory convenience type)** keeps the full set of values for code-readability
and call-site compatibility, but the values come from parsing the stem output (already fetched
the same way as today via `FetchBranchDataByRoot`):

```go
type BranchData struct {
    RootRecord                      // Root, SequencerID (from DB)
    Stem            *ledger.OutputWithID
    SequencerOutput *ledger.OutputWithID

    // projected from Stem.Output.StemLock() — populated at construction time
    TotalSupply     uint64
    TotalCoverage   uint64
    CoverageDelta   uint64
    FrozenCoverage  uint64
    SlotInflation   uint64
    NumTransactions uint32
    BaselineRoot    common.VCommitment
}
```

Projection happens inside `FetchBranchDataByRoot` so every consumer that today accesses
`br.CoverageDelta`/`br.Supply`/etc. keeps compiling with minimal churn — the values are now
sourced from the stem, not from the (deleted) `RootRecord` columns.

`RootRecordParams` shrinks correspondingly. `Update()` still writes the (now lighter) root
record atomically with the trie.

Compatibility shim layer NOT introduced — this is a hard ledger-version break. Confirmed §9.5:
new testnet will be cut after all v0.8.x breaking changes have landed (more are coming); no DB
or wire compat needed.

## 6. New attacher / commit flow

Before the refactor:

1. wrap-up computes deltas → `finals.TransactionMetadata` (advisory) → `RootRecordParams` (DB).
2. Branch is committed; root record persisted with all fields.
3. `checkConsistencyWithMetadata()` warns if peer-supplied metadata disagrees.

After:

1. **Sequencer build path** (`sequencer/txbuilder_seq/...`): when constructing a branch tx, the
   sequencer computes `totalSupply, totalCoverage, coverageDelta, frozenCoverage, slotInflation,
   numTransactions, baselineRoot` from its own past-cone projection plus the predecessor stem
   values (totalCoverage, totalSupply) read from the stem output already in hand via `stemInput`.
2. **Attacher path**: validator independently computes the same set from its past cone. After
   wrap-up, it compares the computed values against the values declared in the produced stem.
   - `slotInflation`, `coverageDelta`, `frozenCoverage`, `numTransactions`, `baselineRoot`,
     `totalCoverage`, `totalSupply` mismatch → **panic** (see §9.6: any mismatch means a major
     bug or out-of-consensus condition; deterministic computation must agree).
   - The recurrences (`totalSupply = predSupply + slotInflation`,
     `totalCoverage = (predTotalCoverage >> K) + coverageDelta`) are already enforced at EasyFL
     stage 3, so the panic at the Go level catches inconsistency between Go-computed and
     stem-declared base aggregates.
3. The five stem-derived values are no longer written to `RootRecord`. `Update()` only persists
   `Root, SequencerID`.
4. `Branches.LedgerCoverage(branchID)` becomes a one-liner: read the branch's stem output,
   parse `stemLock`, return `totalCoverage`. The 64-slot halving traversal in `branches.go:178`
   is deleted. The cache of derived values in `branches.go` is removed (per
   `feedback_cache_and_refcount.md` and confirmed §10: local recurrence is the single source).

## 7. Persistent TxMetadata removal

After the move, every persistent field of `TransactionMetadata` is either (a) on-chain in the
stem (for branch txs), or (b) recomputable. Therefore:

1. **Remove the `TransactionMetadata` struct type entirely.** The two ephemeral fields
   (`SourceType`, `TxBytesReceived`) are passed as plain Go function parameters on the receive
   path, or folded into existing context structs (e.g. attacher/workflow input descriptors).
   No serialization, no JSON mirror, no dedicated type — the package
   `core/txmetadata` is deleted.
2. `txstore` stops persisting metadata. `PersistTxBytesWithMetadata` becomes
   `PersistTxBytes(txBytes, txid?)`. The `mdBytes ||` prefix in `txstore.go:94-99` is removed.
   Callers of `GetTxBytesWithMetadata` are renamed/migrated to plain `GetTxBytes`.
3. `txmetadata.SplitTxBytesWithMetadata` / `ParseTxMetadata` and the entire
   `core/txmetadata` package are removed.
4. P2P wire format (`peering/txbytes.go`): drop the metadata prefix on send/receive. Source type
   stays as a *runtime* field on the receive path, set from the connection origin.
5. API path: drop metadata serialization on `submit_tx`, plus the optional metadata block in
   responses (audit any JSON shape changes).
6. Receivers that today log/inspect provided metadata (`attacher_milestone.go`,
   `core/workflow/txinput.go`, `pull_tx_server`, `dag_explorer`) lose the optional inputs and
   recompute as needed.

Backward compatibility: **none**. Hard format break, lands inside the v0.8.x breaking-change wave.

## 8. Implementation phases

Order chosen so the tree compiles after each phase.

### Phase A — EasyFL & ledger types (the source of truth)

A1. Add ledger constants `constHealthyCoverageNumerator` (=7), `constHealthyCoverageDenominator`
    (=12) and the EasyFL function `healthyCoverageDelta(supply, covDelta)` (§4). Wire
    `global.FractionHealthyBranch` and `global.IsHealthyCoverageDelta` (and every other
    reference) to read from / call into these ledger definitions (precompiled call for the
    function — single source of truth).
A2. Extend `StemLock` Go struct with new fields. Update `Source()` template, `Bytes()`,
    `StemLockFromBytesWithLib`. Re-run inline test (`registerInlineTest`). Use compressed
    integer encoding (z64/z32) consistent with EasyFL conventions.
A3. Update `ledger/def/lock_stem.easyfl`: add args $2..$8, decoders, supply formula, halving
    formula, frozenCoverage strict-< check, healthiness check. Add helper funcs
    `_predTotalSupply`, `_predTotalCoverage`, etc.
A4. EasyFL inline tests + `ledger/tests/` updates: any test that builds a stem must produce
    valid args. Add a focused `claude_stem_constraint_test.go` covering:
    - successor with correct fields → accept
    - wrong supply formula → reject
    - wrong halving formula → reject
    - `frozenCoverage >= coverageDelta` → reject
    - unhealthy branch → reject
    - genesis stem path
A5. Adjust `txbuilder` helpers in `ledger/txbuilder/` that build branch transactions to accept
    the new fields.

### Phase B — Sequencer build path

B1. `sequencer/task/...` and `sequencer/strategy_async.go`: compute the seven new stem fields
    when proposing a branch milestone. Pull predecessor's `totalSupply`, `totalCoverage` directly
    from the stem-input being consumed; pull `baselineRoot` from `Branches.Get(prev).Root`.
B2. `sequencer/txbuilder_seq/...`: thread the values into the new `StemLock`.
B3. Sanity assert in build path: the sequencer's projection of `totalCoverage` matches the
    recurrence-derived value before signing, to catch projection drift early.

### Phase C — Multistate / Branches

C1. Trim `RootRecord` and `RootRecordParams` to `Root` + `SequencerID` only. Update
    `Bytes()`/`FromBytes()` to a 2-element tuple. Bump tuple element count constant. Hard break.
C2. Project all stem-derived fields into `BranchData` inside `FetchBranchDataByRoot`. Code that
    currently reads `br.CoverageDelta`, `br.Supply`, etc. continues to work.
C3. Rewrite `branches.LedgerCoverage`/`Supply` to read from stem output (drop the halving loop
    and the cache). `LedgerCoverage(branchID)` becomes `Get(branchID).TotalCoverage`.
C4. Migrate every consumer that touched removed `RootRecord` fields. Search hit list:
    - `roots.go:144-159` (Lines / LinesVerbose)
    - `roots.go:267-274` (FetchLatestRootRecords sort by CoverageDelta)
    - `roots.go:455-470` (FindRootsFromLatestHealthySlot uses CoverageDelta+Supply)
    - `roots.go:496-554` (FindLatestReliableBranch sorts by CoverageDelta)
    - `roots.go:613` (`IsHealthy` uses CoverageDelta+Supply)
    - `branches.go` accessors (Supply, LedgerCoverage)
    - `proxi db info` / `db roots` printout
    - any API exposing these values

### Phase D — Attacher

D1. Replace `checkConsistencyWithMetadata` (advisory) with `enforceStemValues` (panic on
    mismatch) for branch transactions. Compare attacher-computed values vs. the values inside
    the produced stem. Per §9.6, mismatch is a major bug / out-of-consensus → panic.
D2. For non-branch sequencer txs: nothing to enforce on stem (no stem produced); ledger coverage
    monotonicity checks (`check.go:31-85`) already use computed values — no change needed beyond
    swapping the source of `Branches.LedgerCoverage` to stem-read.
D3. Drop population of persistent metadata fields in `wrapup.go:23-29` (removed entirely along
    with the `TransactionMetadata` type — see Phase E).

### Phase E — TxMetadata teardown

E1. Delete the `TransactionMetadata` type and the entire `core/txmetadata` package. The two
    ephemeral fields (`SourceType`, `TxBytesReceived`) become plain Go parameters on the receive
    path, or are folded into existing context structs (e.g. attacher/workflow input descriptors).
E2. With the package gone, `Bytes()`, `flags()`, `TransactionMetadataFromBytes`,
    `SplitTxBytesWithMetadata`, `ParseTxMetadata`, and the JSON mirror disappear with it.
E3. `txstore`: drop metadata prefix; rename APIs accordingly.
E4. `peering/txbytes.go`: drop metadata in/out on the wire.
E5. `api/server/txapi.go` + `proxi/db_cmd/txstore/...`: update CLI/API paths.
E6. Update all ~30 call sites in the former TxMetadata fan-out (`grep -l TxMetadata`) to use the
    plain-param form.
E7. Delete `core/txmetadata/txmetadata_test.go` along with the package.

### Phase F — Tooling, docs, telemetry

F1. `proxi db roots` / `proxi db info`: read from stem instead of root record.
F2. `dag_explorer` / `api/v1/get_branches`: same.
F3. Update CLAUDE.md memory (Active Task list + key learnings) only when phases land.

## 9. Open design questions / decisions taken

1. **Stem field widths** — accept §3 layout. ✅ keep `frozenCoverage` as trustless stats.
   Compressed `z64/`/`z32/` encoding, not fixed-width.
2. **`NumTransactions`** — ✅ moved to the stemLock only; removed from `RootRecord`. Read from
   the stem output (projected into `BranchData` at construction time, §5).
3. **Genesis stem** — ✅ `totalCoverage = totalSupply`, all deltas = 0, `baselineRoot` = zero
   bytes. EasyFL produced-branch path detects genesis (e.g. via `equal(_predOutputID, ...)`
   or `equal(K, 0)`-style discriminator) and skips the recurrence checks while still asserting
   the genesis-specific equalities.
4. **`baselineRoot` enforcement** — ✅ Go-only check. The state root is a consensus value among
   many nodes, so it is trust-less by independent recomputation. No new EasyFL primitive.
5. **Hard break vs soft window** — ✅ no compat. New testnet will be cut after all v0.8.x
   breaking changes have landed; further breaking changes are expected.
6. **`LedgerCoverage` semantics** — ✅ identical recurrence; on-chain value is the only one.
   Mismatch between Go-computed value and on-stem value → **panic** (it indicates either a node
   bug or that the network has lost consensus on a deterministic value).
7. **Bootstrap-chain exemption from healthiness check** — ✅ exempt. The bootstrap chain is
   skipped, same as in the existing `_enforceBranchCoverageBounds`.
8. **Healthiness check on genesis** — ✅ skipped at genesis. Genesis dispatch (§9.3) bypasses
   both the recurrences and the healthiness check.

## 10. Risks

- **Stem size growth**: today ~36 bytes; after refactor ~120–145 bytes (33 + var + z64 ×6 +
  z32 + 24). Modest tx-size increase. Well below 65,531-byte P2P cap.
- **Net DB size**: stem outputs grow (~+90 B each), but `RootRecord` shrinks by ~32 B per branch
  → net change is small and stem growth lives inside the trie (where it earns its keep as
  cryptographic commitment).
- **Genesis bootstrap**: any of the formula checks can panic on genesis if the special case is
  forgotten. Cover with explicit tests in Phase A.
- **Snapshot restore**: snapshots replay state from a stem output, not from a `RootRecord`. The
  restore flow already reads stem; verify nothing in restore depends on the removed
  `RootRecord` fields. The current "≤64 slots after snapshot" coverage edge case is eliminated
  by construction (recurrence is local and uses only the previous branch).
- **Sequencer projection drift**: the sequencer must declare the *same* values the validators
  will compute. Past-cone projection is deterministic given the same input set; the sequencer
  must use the same baseline branch. Mitigated by the Phase B3 sanity assert.
- **Panic on mismatch**: per §9.6, any deterministic-value mismatch panics. This is strict — it
  means a malformed external branch could crash a node. Mitigation idea: keep the panic
  classification only inside attacher wrap-up where the inputs are already validated and
  trust-anchored to the past cone (the predecessor stem values are part of state). Capture as
  follow-up if operationally too aggressive.
- **30-file fan-out for TxMetadata removal** — straightforward but tedious; do it last
  (Phase E) after the on-chain values are proven correct.

## 11. Out of scope (deferred to follow-up tasks)

- Merkle proof exposure on the API (`Expose Merkle proof in the Readable` — TODO.md line 32).
  Likely already implemented in unitrie; verify and connect later.
- Bridging-side proof verifier — uses platform-independent unitrie proofs. Not a concern now.
- Halving-window length tuning (currently 64 slots) — non-issue once on-chain.

---

**Status: refined; all §9 questions answered. Ready to start Phase A.**
