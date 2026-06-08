# docs_site_audit.md — lunfardo314.github.io (branch ver7) vs proxima codebase

Phase 2 of the documentation effort: audit the public docs site for inconsistencies
with the current proxima `develop` codebase. **This stage is an audit — findings only,
no edits to the site yet.**

- Site repo: `/home/lunfardo/go/src/github.com/lunfardo314/lunfardo314.github.io`, branch
  `ver7` (docsify site; markdown content in topic dirs).
- Code baseline: `/home/lunfardo/go/src/github.com/lunfardo314/proxima`, branch `develop`.
- Companion: repo-docs tracker `claude/docs.md` (Phase 1, complete).

## Resume here (state as of 2026-06-03)

Phase 2 editing is in progress on docs-site branch **`ver8`** (based on `ver7`, pushed to
origin). The proxima-side audit tracker (this file) lives on `develop`. Workflow per file:
edit on `ver8` → commit+push `ver8` → update this tracker → commit+push on `develop`.

**Done on ver8:** `overview/delegation.md`, `txdocs/base.md`, `txdocs/tx.md`;
`participate/run_access.md` deleted (participate section deferred — move docs from proxima).

**`txdocs/tx.md` figures regenerated (ver8, commit `c89cb75`):** replaced the three stale
images — raw-tx tuple tree, full-context tuple tree, example printout. New files under
`static/img/`: `tx-raw.svg`, `tx-context.svg`, `tx-printout.png` (old `utxo-tx*.png` /
`tx_printout.png` left in place, no longer referenced). Editable sources in
`static/img/src/` (`*.dot`, `style_printout.py`, `printout_full.txt`). Figures fix the
11-element layout (0-10, no phantom index 11), correct UTXO internals, vertical 0-5 terminal
column, and show the `utxo(id)` (input→consumed UTXO) and `unlocks` (unlock-param→input)
correspondences. The printout was generated from `ledger/tests/printout_example_test.go`
(a documented foundry-mint generator, kept on develop).

`txdocs/validation.md` DONE on ver8 (commit `abf0042`): rewritten for the current 3-stage
model (pre-validation / partial-context / full-context), correct element indices, tx-level
scripts (redeemScript/callRedeemer, native-token `token(...)`), and the path-based
local-context explanation (`selfIsConsumedOutput`/`selfIsProducedOutput`).

**`txdocs/` section COMPLETE on ver8.** `easyfl.md`/`utxo.md`/`intro.md` fixed + `tmp.md`
deleted (commit `339ab83`); orphan `library_base.md` deleted (commit `3d138fd`, user-approved
— it was an unreferenced stale dump; `easyfl.md` covers the language, library lives in code).

**Next, in priority order:**
1. `ledgerdocs/*` covenant rewrites (constraints, chain, chain_lock, general-def, library) +
   regenerate `ledgerdocs/genesis.id.md` from the current JSON library.
2. `overview/incentives.md` (mark APR table illustrative; pace 12 not 5).
3. `multistate/multistate.md` + `participate/participate.md` are unwritten stubs.

Authoritative layout facts (verified): tx tuple = 11 elements 0–10
(`ledger/txbuildercore/tx_layout.go`); UTXO tuple = amounts[0]/index-values[1]/lock[2]/
chain[3]/foundry|sequencer[4]/foundryPolicy[5]/extras[6+] (`output_layout.go`); ChainID 24
bytes; seq flag = bit 0 of timestamp byte 4; SequencerDataLen = 2.

## Site content map

| Section | Files | Code-tied? |
|---------|-------|-----------|
| `overview/` | intro, utxo_ledger, permissionless, consensus, safety_liveness, incentives, delegation | concepts (some specifics) |
| `txdocs/` | intro, base, tx, utxo, validation, easyfl, library_base, tmp | **high** (tx structure, IDs, EasyFL) |
| `ledgerdocs/` | library, constraints, chain, chain_lock, general-def, genesis.id | **high** (covenants, genesis library) |
| `multistate/` | multistate | medium |
| `participate/` | participate, run_access | overlaps repo `docs/run_access.md` |
| `blog/` | nakamoto, digital_gold | low |
| root | README.md | low/medium |

## Known codebase changes to check against (cheat-sheet)

- ChainID is **24 bytes / 48 hex** (was 32/64). Display: address `a/<hex>`, chain `c/<hex>`,
  chain ID `$/<48hex>`; dashed tx IDs `s<slot>-<tick>-<hex>`, output `…#<idx>`. EasyFL
  *source* form is still `a(0x..)`.
- Token names: **PROX** (base token), **mote** (smallest unit).
- Ledger time: 128 ticks/slot, tick max 127, seq bit = bit 0 of timestamp byte 4.
- TxID 32 bytes = 5-byte ts + 1 byte (numOutputs−1) + 26-byte hash. OutputID 33 bytes.
- **Metadata refactor**: `TxMetadata` removed; per-branch aggregates moved onto the **stem
  output** (stemLock + StemData); `RootRecord` is now only `{Root, SequencerID}`.
- **Stem data split**: stemLock + StemData inline tuple at output idx 3; aggregates incl.
  `frozen_coverage`, `numSeqTransactions`, `numSeq`.
- **Sequencer constraint** now carries delegation params (`epochSlots`, `maxFrozenEpochs`);
  standalone `delegationParams` constraint removed.
- Delegation: `inflationCut` (promille), advance, safe revocation 60 slots, epoch ~600
  slots, max frozen epochs default 20 (range 8–32).
- **EasyFL serialization is JSON** now (was YAML); LedgerDefinitions file `.yaml`→`.json`.
  Crypto builtins (`blake2b`, `validSignatureED25519`) moved into proxima. Library hash
  includes `VersionData`.
- `redeemScript`/`callRedeemer` added; `TxOtherData` slot removed.
- Native token: `foundry(supply)`, `token`/`tokenAmount`.
- Pace = 12; PostBranchConsolidation removed (endorsements monotonicity-only).
- UTXO tuple layout: 0 amounts, 1 index-values, 2 lock, 3 chain, 4+ extras.
- `genesis.id.md` is a **YAML dump with old hash `69097cea…`** — stale format + content.

## Audit findings

(populated from the three section audits below)

Overall: **txdocs/ and ledgerdocs/ are substantially stale** (the data-format and
covenant model changed in a hardfork-class refactor). **overview/ is mostly fine** except
`delegation.md`. Several files are unwritten stubs. Token names (PROX/mote) are correct
everywhere (`ledger/base/token.go`). Severity: 🔴 rewrite · 🟠 significant · 🟡 minor.

### txdocs/

- 🔴 `library_base.md` — wholesale stale: it reproduces the EasyFL base library as **YAML**;
  it's now **JSON** (`easyfl/library.json`). Stale hash (doc `8d5795e2…`, actual
  `86efd216d65141773b3a6709d5a78da3195db3d62c42bd1f601b727bfcdcdad2`), funCodes shifted by
  one (`fail` is 15 not 16), and `blake2b`/`validSignatureED25519` are **gone from base**
  (moved to proxima `ledger/crypto_builtins.go` + `def_embed0.json`). Best: replace with a
  pointer to the canonical JSON files, or regenerate.
- ✅ `tx.md` — **DONE on ver8 (2026-06-03).** Fixed element table to **11 elements (0–10)**,
  removed phantom index-11 "Other data", relabeled index 10 = transaction-level constraints;
  produced output i = `T[8,i]`; `consumed(T)` uses `len(T_6)`; unlock-params path `(0,7,i)`;
  rewrote UTXO-element description (NOT all terminal — `c_0` amounts and `c_1` index-values
  are tuples; `c_2` lock; `c_3` chain); `sigLock` is argument-less (holder in index-values).
- 🔴 `validation.md` — every concrete tuple index is the pre-refactor layout: timestamp is
  `T1` (doc T5), sequencer data `T2`, signature `T3`, input commitment `T4` (doc T7), inputs
  `T6` (doc T0), produced outputs `T8`. No scalar "total produced" at `T6`.
- ✅ `base.md` — **DONE on ver8 (2026-06-03).** Fixed TxID byte table (count byte at index 5,
  hash bytes 6–31); seq flag is **bit 0** of byte 4 (was wrongly "bit 7"); genesis now **three**
  outputs (count byte `02`); dashed display forms (`s<slot>-<tick>-<hash>`, `#idx`); added a
  **Chain ID** subsection (24 bytes, `$/<48hex>`); `0x0102` = 258; corrected the Go-bindings
  UTXO layout (amounts[0]/index-values[1]/lock[2]/extras[3+], `WithAmounts`). Replaced fragile
  `String()` printouts with a layout table to avoid fabrication.
- 🟡 `utxo.md` — "genesis initially contains **two** UTXOs" → three.
- 🟡 `easyfl.md` — `lessOrEqualTo`→`lessOrEqualThan`; descriptor fields are JSON `embeddedAs`
  (camelCase), not YAML `embedded_as`.
- 🟡 `intro.md` — broken link `ledgerdocs/easfl.md` → `txdocs/easyfl.md`.
- 🟡 `tmp.md` — scratch stub (dup of tx.md fragment), not in nav; delete.

### ledgerdocs/

- 🔴 Lock model (constraints.md, chain_lock.md) — rewrite needed. Locks are now **0-arg**
  (`sigLock`, `chainLock`, `delegateLock`); identity (holderID/chainID) lives in the
  **index-value tuple at output index 1**, read via `selfIndexValue(0)`. Gone:
  `addressED25519`/alias `a`, `unlockedByReference`(old form), `msgED25519`, `total`,
  single-value `amount` constraint.
- 🔴 UTXO tuple layout (constraints.md L68-72 wrong) — amounts[0], index-values[1],
  **lock[2]**, chain[3], foundry/sequencer[4], foundry-policy[5] (`output_layout.go`).
  Index 0 is an **`amounts` vector** (balance / inflation / frozen coverage), not a scalar.
- 🔴 `chain.md` — entire source listing obsolete. `chain(...)` is now **7-arg** (ChainID,
  pred index, origin slot, cum chain inflation, cum branch bonus, transition counter, branch
  counter; `chain.easyfl`). No 35-byte blob, no "transition mode". Origin = **24 zero bytes**;
  discontinue = empty unlock params. Branch bonus is VRF-based.
- 🔴 `genesis.id.md` — stale point-in-time **YAML** dump, old hash `69097cea…` (current base
  `86efd216…`), obsolete symbols, missing all current ones. Regenerate from the current JSON
  library and reframe as JSON.
- 🟠 `general-def.md` — `parseArgumentBytecode`/`parsePrefixBytecode` → unified
  **`parseBytecode`**; `parseInlineDataArgument` takes prefix **last**; `txID` is now an
  embedded function (not an EasyFL formula); `amount`→`amounts` vector,
  `amountConstraintIndex`→`amountsConstraintIndex`; several listed constants removed/added.
- 🟠 ChainID 24 bytes / `$/<48hex>` throughout (constraints, chain, chain_lock).
- 🟠 `library.md` — "typically written in YAML" → JSON.
- Sequencer constraint now carries delegation params `sequencer(epochSlots, maxFrozenEpochs)`.

#### ledgerdocs survey refinements (2026-06-08, verified vs develop)
Confirmed GONE in `ledger/def/*.easyfl` (grep): `addressED25519`/`a`, `func amounts` (the
constraint is `func amount` in `amounts.easyfl`), `txTotalProducedAmount`, `func total`,
`pathToTotalProducedAmount`, `pathToLocalLibraries`, `pathToSeqAndStemOutputIndices`,
`msgED25519`. Current `sigLock` (in `lock_signature.easyfl`) is 0-arg: holderID read via
`selfIndexValue(0)`, lock pinned at `lockConstraintIndex` (=2); `unlockedByReference` now
compares holderID via index-values.

Per-file specifics + plan:
- `chain.md` — verbatim source listing; **regenerate** from `ledger/def/chain.easyfl`
  (current `chain` is the multi-arg form; header comment still says "35 bytes / transition
  mode" — obsolete). Linked from `constraints.md` (not the sidebar).
- `chain_lock.md` — verbatim source; **regenerate** from `ledger/def/lock_chain.easyfl`
  (doc shows `selfBlockIndex,1` → now lock index 2; `len($0),u64/32` + `slice(..,0,31)` →
  ChainID 24 bytes). Linked from `constraints.md`.
- `constraints.md` — prose rewrite: mandatory-index list (0 amounts vector / 1 index-values /
  2 lock — doc says "0 amount, 1 lock"); unlock-params path `(0,1,i,j)`→`(0,7,i,j)`; delete the
  `txTotalProducedAmount`/`total` and `msgED25519` examples (functions gone) or replace with a
  current witness example; replace `addressED25519`/`a` section with argument-less `sigLock`.
- `general-def.md` — prose rewrite: produced path `(0,2,..)`→`(0,8,..)`; `pathToProducedOutputs
  0x0002`→`0x0008`; `pathToTimestamp 0x0005`→`0x0001`; rewrite `txEssenceBytes`/`txID` (essence
  now = all 11 elements except signature, no total-amount/local-libraries; `txID` is embedded);
  refresh the constants block + "YAML"→"JSON"; `amountConstraintIndex` naming.
- `library.md` — small: "typically written in YAML" → JSON; "genesis.id YAML" framing.
- `genesis.id.md` — 1589-line YAML dump, old hash, obsolete symbols. **DELETE** (user-approved
  2026-06-08, same call as `library_base.md`). Must also drop the references in `library.md`
  (L10) and `general-def.md` (L39, L68) when deleting.

### overview/ + multistate/ + participate/ + blog/ + README

- ✅ `overview/delegation.md` — **DONE on ver8 (2026-06-03).** Rewritten in real terms:
  freeze measured in **epochs** (per-sequencer length + ceiling), not "1–12 hours"; safe
  revocation = **60 slots** (~10 min); **inflation cut** in promille + sequencer profit
  margin; advance kept. Compulsory-freezing section reframed as **planned** (no such feature
  in code — verified: freezing only applies to owner-created delegation outputs; flag for
  user whether to keep-as-planned or remove).
- ✅ `participate/run_access.md` — **DELETED on ver8 (2026-06-03)** per user. The whole
  `participate/` section will be rewritten and its operational docs moved from the proxima
  repo; the stale copy was removed in the meantime.
- 🟡 `overview/incentives.md` — YoY inflation % table is model-derived (mark illustrative;
  `C=30303030` is confirmed in code). Pace example "1005" (5 ticks) understates default pace
  **12**.
- ⬜ `multistate/multistate.md` — unwritten stub (`TBD…`). When written, document
  RootRecord = {Root, SequencerID} + stem-output aggregates.
- ⬜ `participate/participate.md` — near-empty skeleton.
- ✅ `overview/{intro,permissionless,utxo_ledger,consensus,safety_liveness}.md`, `README.md`,
  `blog/*` — no concrete inconsistencies (conceptual; consistent with current model).

### Flagged code issues (not doc problems)

- `ledger/def/chain.easyfl:179-180` comment says "chain constraint must be at index 2" but
  `chainConstraintIndex` = `ConstraintIndexChain` = **3**. Stale code comment.

## Progress log

- 2026-06-03 — Created audit tracker; on `ver7`.
- 2026-06-03 — Ran three parallel section audits (txdocs / ledgerdocs / overview+rest)
  against `develop`. Findings recorded above. Summary: txdocs + ledgerdocs substantially
  stale (data format + covenant model); overview mostly OK except delegation.md;
  run_access.md stale vs repo; several stubs; token names correct. Audit complete — editing
  not yet started.
- 2026-06-03 — Started site editing on new branch **`ver8`** (based on `ver7`, pushed to
  origin). Rewrote `overview/delegation.md`; deleted `participate/run_access.md` (the
  `participate/` section will be rewritten and its operational docs moved from proxima).
  Site edits land on `ver8`; the proxima `claude/` tracker stays on `develop`.
  Plan: the whole `participate/` section is a later rewrite (move docs from proxima repo).
- 2026-06-03 — Rewrote `txdocs/base.md` on ver8 (TxID layout, seq-flag bit, genesis 3
  outputs, dashed IDs, Chain ID subsection, integer typo, UTXO layout).
- 2026-06-03 — Fixed `txdocs/tx.md` on ver8 (11-element tuple, removed phantom Other-data,
  index 10 = constraints, produced-output/unlock-param paths, UTXO-element tuple layout,
  argument-less sigLock). Next: `txdocs/validation.md`.
  Note: `ledger/txbuildercore/tx_layout.go:19` comment says "4-byte sequencer info" but
  `SequencerDataLen = 2` (`tx_data.go:21`) — stale code comment, flagged not fixed.
