# docs.md — Proxima documentation: plan, status, progress

> **META** — The documentation effort: what is done, what is left, and where each
> kind of document lives. **"What is left" is the pending queue — work it in
> order.** Absorbs the former `docs_site_audit.md`, archived 2026-08-25 as
> `claude/archive/shipped/docs_site_audit.md` — read it for the per-file audit
> findings, all of which are now resolved.

## Goal

Make Proxima's documentation accurate, consistent and approachable, grounded in
the **current `develop` branch** rather than in what a document says about
itself.

## Where documentation lives

Three places, with a rule for each:

1. **The package it documents** (proxima repo) — a developer document lives
   beside its code. `api/api.md`, `api/txapi.md`, `core/README.md`,
   `core/attacher/README.md`, `core/memdag/README.md`,
   `core/core_modules/forward_sync/sync.md`, `global/logging.md`,
   `ledger/limits.md`, `ledger/upgrade.md`, `ledger/def/easyfl.md`,
   `ledger/multistate/{snapshot_format,utxo_indexing}.md`,
   `peering/{README,network_connectivity}.md`, `sequencer/README.md`,
   `tests/README.md`, `txlogger/README.md`. **There is no `docs/` directory** —
   it was emptied on 2026-08-24 and the two API references moved into `api/`.
2. **`README.md`** (repo root) — the user landing page.
3. **The docs site**, `lunfardo314.github.io` — public, user-facing. All
   operational guides live there under `participate/`; the transaction model
   under `txdocs/`; concepts under `overview/`. Published from `main`.

## Working principles

- **User-facing, not developer docs**, for the site and the root README. The
  reader is assumed to know nothing Proxima-specific; introduce terms plainly.
- **Package docs stay developer-facing.** Fix accuracy; do not rewrite into
  user-facing style.
- **Simple language.** No coder slang, no crypto slang, not verbose.
- **One document at a time.** The user picks the topic and the file.
- **Verify every concrete claim against `develop` before writing it.** Code
  comments usually tell the truth, but not always; flag a stale one rather than
  editing code unasked.

## Status

**Phase 1 — repo docs: COMPLETE.** Every `docs/` document was reconciled with
`develop` in 2026-06, then in 2026-08 the directory was dissolved: the eight
operational guides moved to the site, the two API references to `api/`, and the
developer documents to the packages they describe. Nine new package documents
were written in the process.

**Phase 2 — docs-site audit and reconciliation: COMPLETE (2026-06-08).** Every
section — `overview/`, `txdocs/`, `ledgerdocs/`, `multistate/`, `participate/` —
was audited against `develop` and corrected on branch `ver8`, since merged to
`main` and published. The audit found `txdocs/` and `ledgerdocs/` substantially
stale after a hardfork-class refactor of the data format and covenant model;
`overview/` was largely sound. Per-file findings are in the archived audit.

**Phase 3 — content additions: COMPLETE (2026-08-25).** Five planned additions,
all shipped and live:

| # | Addition | Landed |
|---|----------|--------|
| 1 | `txdocs/redeemer_scripts.md` — `redeemScript` / `callRedeemer` | 2026-08-25 |
| 2 | `txdocs/native_tokens.md` — foundries, `token`, `tokenAmount` | 2026-08-25 |
| 3 | Holder ID in the single-signature model (`txdocs/tx.md`) | 2026-08-25 |
| 4 | `participate/` section — all 8 operational guides moved from the repo | 2026-06-09 |
| 5 | Single-signature model reconsidered and rewritten (`txdocs/tx.md`) | 2026-08-25 |

Also written this round: `participate/mine.md` (new) and a rewritten
`participate/delegate.md`.

## What is left

In order. Both new documents are written; items 4 to 6 close the effort out.

### 1. The `overview/` restructure and the onboarding spine

Tracked as Phase 6 of `kb_reorg.md`, not here — it is the last phase of that
plan and consumes the two remaining `QUEUED` documents, `claude/inflation.md`
and `claude/tick_duration.md`.

**The gate is closed: the spine was settled with the user on 2026-08-25** and
is recorded in section 1a of `kb_reorg.md`. Four ordered pages in `overview/`
(what Proxima is / tokens and supply / participate / how transactions work),
five existing pages kept as reference behind them, `incentives.md` split by
audience and retired. Writing proceeds one page per session against that
structure; the structure itself is not reopened without the user.

### 2. Developer doc — protection gates and throttling, in one place — DONE 2026-08-26

Written as **`core/resilience.md`**, linked from `core/README.md`. `core/` was
chosen because the transaction processing path is what `core` is about; the
ledger and sequencer gates are covered there too rather than being split off.

On the user's instruction mid-write, the document is **architecture-led**, not a
gate list: Part I is the threat model, the five defence principles, the
degradation ladder and the survivability/recovery model (what self-heals, what
needs an operator, why restart is a hazard, the deadlock-avoidance carve-outs,
and why the network survives when a node does not). Part II is the gate
inventory that supports it.

Every entry of the original checklist was verified against `develop` before it
was written down, and the list was indeed incomplete. What the checklist got
wrong or missed:

- The dedup filter is **not a bloom filter** — it is an exact map of transaction
  IDs with a 60-slot TTL, because each entry carries the pulled/not-pulled state
  and because a false positive would silently lose a transaction. Fixed in
  `core/README.md`, in the metric's `Help` string, in `CLAUDE.md`'s metrics
  table, and in `ingate.go`, which now records the reason.
- The **API upload limit is 2 MiB**, not 65,536 — the 64 KB figure is
  `MaxTransactionSize` at parse. `ledger/limits.md` was wrong and was fixed.
- The **ahead-drop rule** described in the checklist no longer exists in that
  form. What is there now is a 6-slot future timestamp bound plus a
  `SourceTypeSequencer` exemption from the attach gate.
- **Missed entirely by the checklist**: the sequencer's AIMD (TCP-like) tag-along
  budget controller, which is the main system-wide throttle; the memory watchdog
  and its graceful shutdown at 100% stress; branch health as a *convention* with
  a configurable relief window; the coverage-contribution lower bound; the
  attacher build deadline and vertex-TTL self-abort; streaming connection caps
  and slow-consumer drops.
- **Three gaps recorded**: serving pull requests is unrated; `/api/v1/eval` has
  no request-body cap; and `tx_drop` / `sync_drop` / `seq_drop` are internal
  counters that never reach Prometheus, so three of the four drop reasons are
  invisible in Grafana.


### 3. Developer doc — architecture orientation, with all references — DONE 2026-08-26

Written as root **`ARCHITECTURE.md`**, beside `README.md`, and linked from it.
Three landing pages now have one reader each: `README.md` (is this interesting),
`ARCHITECTURE.md` (a developer about to change something), `CLAUDE.md` (working
rules for Claude).

Contents: the four-layer shape of the system with the `ledger/` vs `core/`
protocol boundary made explicit; a package map covering every top-level package
and subpackage; the two lifecycles (a transaction, a node); the three BadgerDBs;
where the rules live and what makes a change a hardfork; why `proxi` is outside
the node. Then the reference index — hard constraints, every developer document
with a line on when to read it, the docs-site sections, the `claude/` working
set and the three archive buckets, and the external dependencies. It closes with
**reading paths** (start from what you are about to do) and **traps**.

Found and fixed while writing: `README.md` pointed at
`https://lunfardo314.github.io/#/overview/intro`, a page that ceased to exist in
the docs-site spine restructure — a dead link on the repository landing page.

Recorded honestly rather than papered over: `core/attacher/README.md` still
opens "TODO needs review", so the index marks it as notes rather than authority
and points at `dag_semantics.md` instead.

Still open, for the consolidation pass (item 5): `CLAUDE.md`'s developer-doc
list omits `ledger/limits.md`, `ledger/def/easyfl.md`, `core/README.md`,
`sequencer/README.md`, `core/core_modules/forward_sync/sync.md` and
`tests/README.md`; and its Project Overview still says "~52K lines" against an
actual ~80K non-test.


### 4. `claude/TODO.md` — DELETED 2026-08-26

The file was reviewed entry by entry against `develop` first. Of fifteen
entries, eight were complete, one rested on a premise that was no longer true
(the "intermittent FATAL — ledger coverage should not decrease along
endorsement" investigation: `check.go` no longer calls `Fatalf` at all, and the
transient-stale-state guard it proposed already exists), and four of the
remaining six needed correcting rather than keeping — including a dust-rule
entry that was half-done without saying so, and a latency threshold whose
arithmetic was computed against an 8 ms tick when the tick is now 80 ms.

The user's call on seeing the result: **delete it.** A backlog that needs a
full re-verification pass before any entry can be trusted is not a backlog. Open
work belongs in the live document that owns the topic, or in the code, not in a
list that rots between sessions.

One piece was kept, because the code cannot record it: the conditional-lock
audit, which says which produce-side checks were deliberately *not* refactored
and why → `archive/shipped/lock_delegation_audit.md`.

Nothing else was preserved. The remaining open items were: framework-side
enforcement of `selfEnforceZeroAmountsInNonChainedOutput`; restore-time snapshot
selection rules; the dagviz connection TTL default and an at-capacity close
reason; cheaper branches; and a handful of ledger-refactor ideas (Merkle proof
in `Readable`, native-token constraints on the amounts vector, `evidenceHash`,
`validateWithRedeemed`, library compilation caching, inclusion-proof opcode, the
open lock). They are recorded here, once, and not carried forward as a list.

### 5. Final assessment, consolidation and cleanup

Across **both developer and user-facing documentation**, once the pages above
exist: decide what is really necessary. Cut redundancy, overlap and
non-critical noise; consolidate what says the same thing twice; delete what no
longer earns its place. This is deliberately the last step — it needs the full
set in view to judge what is surplus, and it is the step where the effort stops
growing documentation and starts reducing it.

### 6. One document for documentation maintenance guidelines

The rules that keep all of the above true after this effort ends: where each
kind of document lives and why, the status-header convention, the requirement
to verify against `develop` rather than trust a document's own account of
itself, when a document is archived instead of edited, and how the repo and the
docs site stay in step.

The lesson worth writing down first: during this reorganization roughly **one
document in eight described itself wrongly**, almost always by calling a
shipped feature unimplemented. Guidelines that do not account for that will not
hold.

## Loose ends

**`core/attacher/README.md`** still opens with the line `TODO needs review`. It
is the one package document never reconciled against `develop`.
`core/memdag/README.md` reads as current but was never formally checked.

**Two stale code comments**, both verified still present on 2026-08-25. Neither
is a documentation bug; both were found while checking documentation claims, and
both are one-line fixes that need user approval:

- `ledger/txbuildercore/tx_layout.go:19` calls `TxSequencerDataBytes` "4-byte
  sequencer info"; `SequencerDataLen` is **2** (`tx_data.go:21`).
- `ledger/def/chain.easyfl` says the chain constraint sits at index 2 — in a
  comment on line 179, in two more on lines 204 and 207, and in the error
  literal `!!!chain_constraint_must_be_at_index_2` on line 180. The authoritative
  value is `ConstraintIndexChain = 3` (`ledger/txbuildercore/output_layout.go`),
  which is what generates the EasyFL `chainConstraintIndex` symbol. The comments
  predate the insertion of the index-values slot at 1 and can be fixed freely;
  **the error literal cannot** — `!!!` text compiles into the bytecode, so
  renaming it changes the library hash and is a hardfork. Stale comments of the
  same vintage sit in `ledger/tests/{ledger_test.go,claude_index_bounds_test.go,claude_tag_along_test.go}`.

A third such flag, the `ledger/upgrade_utxo.go` header comment mislabelling the
chain slots, was checked on 2026-08-25 and is **already fixed** — the header now
describes the 6-element layout correctly.

## Progress log

Condensed; the two source trackers carried the full per-file detail, and the
archived audit still does.

- **2026-06-03** — Both trackers created. Inventoried `docs/`, the root README
  and the package READMEs; audited all five docs-site sections against
  `develop` in parallel.
- **2026-06-03** — Phase 1 sweep: rewrote `proxi.md`, `delegate.md`,
  `testnet.md`, `api.md`, `txapi.md`, `snapshot_format.md`, `upgrade.md`
  against `develop`; de-verbosed the root README (Highlights 26→12 bullets);
  relocated `logging.md`, `snapshot_format.md` and `upgrade.md` into their
  owning packages. Three user-approved code fixes: the `c/<24-byte hex>`
  comment in `send.go`, the `REVOKED`→`on hold` label in
  `delegate/status.go`, and the docs index in CLAUDE.md.
- **2026-06-03 → 06-08** — Phase 2 editing on site branch `ver8`: `txdocs/`
  (11-element tuple, TxID layout, seq-flag bit, 3-stage validation model,
  figures regenerated as SVG), `ledgerdocs/` (0-arg locks, 7-arg `chain`,
  corrected UTXO layout, YAML→JSON), `overview/incentives.md`, and two stub
  pages written. `library_base.md` and `genesis.id.md` deleted with user
  approval as stale dumps.
- **2026-06-08** — Phase 2 complete. `ver8` merged to `main`; `ver7`/`ver8`
  deleted; unreferenced images pruned.
- **2026-06-09** — Phase 3 item 4: the eight operational guides moved from the
  repo to `participate/`, all repo links repointed to the site.
- **2026-08-24** — `docs/` dissolved; the two API references moved to `api/`.
  Nine package documents written or relocated during the `kb_reorg.md` sweep.
- **2026-08-25** — Phase 3 items 1, 2, 3 and 5 written and merged to the site's
  `main`, together with `participate/mine.md` and a rewritten
  `participate/delegate.md`. Phase 3 complete.
- **2026-08-25** — The two trackers merged into this one; `docs_site_audit.md`
  archived as shipped. Open items re-verified against `develop` rather than
  carried over on trust: one of the three flagged code comments had already
  been fixed, two survive.
- **2026-08-25** — Five items scheduled into the pending queue at user
  direction: the protection-gates-and-throttling document, the architecture
  orientation with all references, a review of `claude/TODO.md` (which ended in
  its deletion), a final consolidation pass over developer and user docs, and
  a document setting the maintenance guidelines. The effort now has a defined
  end, and the last two items are what it ends with.
