# Knowledge-base reorganization — plan

> **META** — Plan and progress for this reorganization of the knowledge base.

Status: **Phases 0-3 complete.** `claude/` is down from 107 files to 35 —
15 that stay plus 20 queued for a rewrite into the docs site or a package — with
71 archived into three indexed buckets. Execution is doc-by-doc across many
sessions, tracked in the *Progress* table at the end of this file. The
*Inventory* section, last in the file, is the classification every move is
driven from.

## Problem

As of 2026-08-24, before this effort, `claude/` held 107 `.md` files, 1.7 MB,
flat, in one heap. It mixed four kinds of document that have nothing in common
except that they were written during a Claude session:

- **authoritative semantic models** that constrain how the core may change
  (`dag_semantics.md`, `sync_semantics.md`),
- **shipped-feature specs** that are now just a slower way of reading the
  code (`metadata-refactor.md`, `output_parsing.md`, `chainid32to24.md`, …),
- **incident and investigation notes** tied to one testnet event on one date
  (`crash2.md`, `consensus_halt_2026-04-23.md`, `forward_sync_oom.md`, …),
- **proposals** that were never implemented, or were implemented and reverted
  (`credit_tokens.md`, `forced_delegation.md`).

Symptoms, as measured in Phase 0: **42 of the 107 files are referenced from
nowhere** in the repo — not from `CLAUDE.md`, not from another doc, not from a
code comment. `CLAUDE.md`'s `claude/` index lists 4 of the 107. Half the
directory has not been touched since before May.

The dating is itself a trap: 12 files carry a 2026-08-19 commit date that means
nothing — `47a31ba7`, a bulk IP scrub. `stress_sequencer_shutdown.md` looks
five days old and is 17 days old; `bottleneck.md` looks five days old and is
from March. Recency in `git log` is not evidence that a document is current.

Two consequences worth separating. For a **human reader**, there is no path in:
nothing says which five documents to read before touching the sequencer. For
**Claude**, the directory is a retrieval hazard — a superseded spec greps the
same as a current one, and stale specs have already produced wrong analyses.

## Decisions

1. **No repo `docs/` folder.** `docs/api.md` and `docs/txapi.md` move into the
   `api` package and the folder is deleted. Developer documentation lives next
   to the code it documents, which is already the convention
   (`global/logging.md`, `ledger/upgrade.md`, `core/attacher/README.md`).
   The two API references are to be reviewed for accuracy later, separately
   from this effort.
2. **Archive is `claude/archive/`** — in-repo, public, greppable, history
   preserved via `git mv`.
3. **Execution is incremental**: phase by phase, document by document, across
   many sessions, with the *Progress* table as the single source of truth for
   where the effort stands. Each step opens by stating what it will change,
   for discussion and confirmation, before anything is touched.
4. **A compact user-facing onboarding set is a first-class goal**, not a
   by-product of the migration. See section 1a. It is written **last**: the
   spine is easier to cut once all the material it must lead into is in place
   and accurate.

## Target shape

Four destinations, chosen by *audience* rather than by topic.

### 1. Docs site (`lunfardo314.github.io`) — public, user-facing

The site today has `overview/`, `txdocs/`, `ledgerdocs/`, `multistate/`,
`participate/`, `blog/`. Phase 2 of `docs_site_audit.md` reconciled all of it
with `develop`; Phase 3 (planned additions) is still open and this effort
should absorb it rather than run beside it.

Material in `claude/` that belongs to an *end user or token holder* and is not
on the site yet:

| Source | Destination | Note |
|--------|-------------|------|
| `native_token.md` | new `txdocs/native_tokens.md` | Already Phase-3 item 2. |
| `local_script.md` | new `txdocs/redeemer_scripts.md` | Already Phase-3 item 1. |
| `delegation_scalability.md`, `delegation_freeze_distribution.md`, `delegation_add_tokens.md` | `participate/delegate.md` | Only the operational part: epochs, freeze depth, top-up. The conceptual half of delegation belongs to `overview/` and waits. |
| `mining_tx_streaming.md`, `mining-bias.md` | new `participate/mine.md` | There is no mining guide at all today. |

Everything destined for **`overview/`** is deferred to the last phase with the
onboarding set, because that section is the spine's raw material and will be
restructured by it — editing it first is work done twice:

| Source | Destination |
|--------|-------------|
| `fairlaunch.md`, `launch_rationale.md` | new `overview/fair_launch.md` — philosophy, the mine chain, genesis ramp. A rewrite, not a move. |
| `inflation.md` | fold into `overview/incentives.md`, which exists and is thin |
| `delegation_scalability.md`, `delegation_freeze_distribution.md` | the conceptual half → `overview/delegation.md` |
| `tick_duration.md` | a paragraph in `overview/consensus.md`; the rest is measurement and stays in the repo |

Phase-3 items 3 and 5 (single-signature model / holder ID) stay as written in
`docs_site_audit.md` — they are site-only edits with no `claude/` source.

### 1a. The onboarding set — a compact, gradual path in

The site's failure mode today is not inaccuracy (Phase 2 fixed that) but
**shape**: five parallel sections, each a reference, and no ordered path for
someone arriving with no Proxima context. The migration in section 1 adds
pages; without a path it makes that worse.

So the effort owes a small, ordered, **compact** set — the read-in-order spine
of the site — with everything else demoted to reference hung off it. This is
the **last** phase: writing the spine means deciding what to leave out, which
is a judgement best made when the material it leads into is already migrated
and correct. Working target, to be settled as its own step before any page is
written:

1. **What Proxima is** — cooperative consensus, biggest ledger coverage, no
   blocks, no mempool. One page, no covenant detail.
2. **Tokens, inflation and the fair launch** — where supply comes from, what
   a token holder can do with it.
3. **Participate** — the three concrete roles in ascending commitment:
   delegate → run an access node → run a sequencer / mine. Each an entry point
   to the existing `participate/` guides, not a duplicate of them.
4. **How transactions work** — the UTXO/covenant model, entered only once the
   reader has a reason to care. Front door to `txdocs/`.

Constraints on this set: each page readable on its own in a few minutes;
concepts introduced before they are used; no forward references into
reference material; every deeper page reachable but none required.

The existing `overview/` section is the raw material and is likely to be
restructured rather than extended — which is why all `overview/` work sits in
this phase. Note the overlap to resolve when the spine is settled: spine page 2
(tokens, inflation, fair launch) and the planned `overview/fair_launch.md` +
`overview/incentives.md` may well be one page, not three.

This set is what the whole documentation effort is *for*. Everything before it
is preparation: the spine is cut from finished material.

### 2. Package-local developer docs

No `docs/` folder. Each developer document lives in the package it documents,
topic-named, alongside the existing `global/logging.md` /
`ledger/multistate/snapshot_format.md` / `ledger/upgrade.md` /
`core/attacher/README.md` / `core/memdag/README.md`.

Immediate move (decision 1), content untouched, accuracy review deferred:

| From | To |
|------|-----|
| `docs/api.md` | `api/api.md` |
| `docs/txapi.md` | `api/txapi.md` |

Then delete `docs/`. The only inbound references are the two rows in
`CLAUDE.md`'s index; all other repo links were already repointed to the site
during the `participate/` migration.

Rewritten from `claude/` sources, each against `develop`, one at a time:

| New doc | Sources |
|---------|---------|
| `core/README.md` — DAG / memDAG / attacher model, the readable way in to `dag_semantics.md` | `dag_semantics.md` (which stays in `claude/` as the hard constraint) |
| `core/core_modules/forward_sync/sync.md` — how a node catches up, the readable way in to `sync_semantics.md` | `sync.md` (which is archived after); `sync_semantics.md` stays in `claude/` as the hard constraint and is only linked, never absorbed |
| `sequencer/README.md` — what a sequencer does and how it chooses | `sequencer.md`, `sequencer_conflict_resolution.md`, `branch_cost.md` |
| `ledger/def/easyfl.md` — Proxima-specific builtins and the embed0 resolver; the language itself is on the site | `easyfl.md` |
| `ledger/limits.md` — size and count limits | `limits.md` |
| `tests/README.md` — standing up a local network | `local_3node_testnet.md`, `local_testnet_runbook.md`, `local_testnet_edge_cases.md`, `one_node_bootstrap.md`, `hands_on_proxi_script.md` — five overlapping runbooks collapse into one |

Relocations with a clear owner: `peering.md` + `network_connectivity.md` →
`peering/`, `txlogger.md` → `txlogger/`, `utxo-indexing.md` →
`ledger/multistate/`.

### 3. `claude/` — what stays

`claude/` shrinks to the working set: documents that are *load-bearing for the
next change*, plus the meta-trackers. Target is roughly 15–20 files, indexed
in full in `CLAUDE.md`.

Keep:

- **Hard constraints**: `dag_semantics.md`, `sync_semantics.md`. Both bind how
  the core may change; neither is ever archived or absorbed. Between them they
  are cited from 16 Go files, which is the practical test of a constraint doc.
- **Live design under active work**: `fairlaunch.md`, `launch_rationale.md`,
  `delegation_scalability.md`, `sequencer_conflict_resolution.md`,
  `branch_fork_convergence.md`, `monitor.md`, `mining_tx_streaming.md`,
  `delegation_add_tokens.md`.
- **Open research, explicitly undecided**: `credit_tokens.md`,
  `dex_orders.md`, `forced_delegation.md`.
- **Meta**: `TODO.md`, `docs.md`, `docs_site_audit.md`, this file.

Every surviving file gets a required header — status (`live` / `research` /
`constraint`), one-line summary, and the code it constrains — so that status
is readable without opening the body. `CLAUDE.md`'s index table already has
the right columns; it just needs to cover all of them.

### 4. `claude/archive/` — second tier

Everything else. Three sub-buckets, by why it is no longer current:

- `archive/incidents/` — one event, one date, resolved. `crash{,2,3}.md`,
  `conflict.md`, `consensus_halt_2026-04-23.md`, `bottleneck.md`,
  `memory_leak.md`, `attachment_time.md`, `committed_branch_baseline_wedge.md`,
  `coverage_delta_enforcement_fix.md`, `delegation_params_position_bug.md`,
  `fix-detach-reattach-race.md`, `forward_sync_oom.md`,
  `fork_detection_recovery.md`, `forward_sync_lineage_nonstitch.md`,
  `known_baseline_attacher.md`, `sync_findings.md`, `stress_sequencer_shutdown.md`,
  `trie_iteration.md`, `pastcone_consistency.md`. (~18)
- `archive/shipped/` — spec for a feature now in `develop`; the code is the
  truth. `metadata-refactor.md`, `stem_data_refactor.md`, `output_parsing.md`,
  `output_kind_index.md`, `chainid32to24.md`, `native_token_tag_32vs20.md`,
  `has_tx_refactor.md`, `txflow2.md`, `library_upgrade.md`,
  `proxi_txbuildercore.md`, `wasm_txbuilder.md`, `wasm_txbuilder_helpers.md`,
  `wasm_easyfl.md`, `wallet_eval_api.md`, `get_outputs.md`, `chain_explorer.md`,
  `delegate_lock.md`, `tag_along.md`, `seq_v080_pbc_removal.md`,
  `async-sequencer-plan.md`, `seq-improvements.md`, `deferred_commit.md`,
  `txid_ttl_tiered.md`, `frozen_coverage.md`, `delegation_epoch_params.md`,
  `delegation_allowance.md`, `send_with_deadline_lock.md`,
  `return_to_sender.md`, `snapshot_optimize.md`, `bootstrap_transactions.md`,
  `chain_constraint{,2}.md`, `endorsement.md`, `nolock-traversal.md`,
  `attachment_cost.md`. (~35)
- `archive/superseded/` — proposals overtaken or reverted, and the four
  rate-control drafts that were never reconciled with each other:
  `adaptive_rate_control.md`, `ratecontrol.md`, `ratecontrol2.md`,
  `rate_control_non_seq.md`, `seq_key.md`, `key_management.md`,
  `big_tasks.md`, `unitrie_double_booking_proposal.md`, `dag_visualizer.md`,
  `target_info.md`, `txstore_audit.md`, `hands_on_plan.md`,
  `scenario_delegation_freeze.md`, `tx_test.md`, `sync_startup.md`,
  `network_rtt_mapping.md`. (~16)

Each bucket gets a one-screen `README.md` — a table of file, date, one line of
what it concluded, and whether the conclusion still holds. That index is the
point of the archive: it is what makes the bucket searchable without reading
it, and what stops a superseded doc from being mistaken for a current one.

A `claude/` source that has been fully absorbed into a site page is archived,
not deleted, with the published page linked from the archive index — the note
is the working record behind the page.

## Phases

Six phases, each independently reviewable, each leaving the repo consistent.
Phases are worked **doc by doc**; a session picks up the first unfinished row
in the *Progress* table and stops when that row is done.

**Working protocol.** Every step begins by stating, in the session, exactly
what it will change — files touched, moves made, text rewritten — and waits for
confirmation. Nothing is moved, written or committed before that.

**Phase 0 — inventory and classify.** Produce a full 107-row table: file,
size, last-touched, referenced-from, proposed destination, one-line verdict.
Verify each "shipped" claim against `develop` before trusting it — a spec that
looks shipped but was reverted must land in `superseded`, not `shipped`.
Deliverable: the table appended to this file. **User reviews and corrects the
classification before anything moves.**

**Phase 1 — `docs/` and the archive.** Two mechanical moves, no content
edited: `docs/{api,txapi}.md` → `api/`, folder deleted; then `git mv` the
archive buckets and write their three `README.md` indexes. Fix inbound links
and the `CLAUDE.md` index rows.

**Phase 2 — headers and index.** Add the status header to every surviving
`claude/` file; rewrite the `claude/` index table in `CLAUDE.md` to cover all
of them.

**Phase 3 — docs site migration, reference sections.** New branch off `main`,
per-file commit workflow as in Phase 2 of `docs_site_audit.md`. Work the first
table in section 1 plus the open Phase-3 items from that audit, one page per
session. `txdocs/` and `participate/` only — `overview/` is deferred to Phase
6. Repo-side: archive the absorbed `claude/` sources, repoint links to site
URLs.

**Phase 4 — package developer docs.** Write the six rewritten docs in section
2 and perform the four relocations, one at a time.

**Phase 5 — reconcile the trackers.** `docs.md` and `docs_site_audit.md`
overlap heavily and both predate this effort; merge into one. Fold
`claude/TODO.md` items that are really doc work into it.

**Phase 6 — `overview/` and the onboarding set.** Settle the spine of section
1a, then write it one page per session, together with the deferred `overview/`
pages — the same section, one decision. Last, so that it is cut from material
that is already migrated and correct.

## Ordering and cost

Phases 0–2 are mechanical, touch no prose, and deliver most of the legibility
gain — they can land in one or two sessions. Phases 3–4 and 6 are genuine
writing at one document per session, and are where the effort actually costs
time. Phase 5 is cleanup.

Phase 1 is the only one that is awkward to undo by hand later, and it is a
pure `git mv`, so history survives and a wrong call is one more `git mv`.

## Progress

Single source of truth for where the effort stands. A session updates this
table before it ends. Each row is one step: it opens with a stated,
confirmed plan of what it changes. Legend: ⬜ not started · 🔄 in progress ·
✅ done.

| # | Phase | Unit of work | Status | Session / notes |
|---|-------|--------------|--------|-----------------|
| 0 | classify | 107-row classification table, verified vs `develop` | ✅ | 2026-08-24 — see *Inventory*. 5 misfilings caught; **awaiting sign-off** |
| 1 | move | `docs/{api,txapi}.md` → `api/`, delete `docs/`, fix `CLAUDE.md` | ✅ | 2026-08-24 |
| 1 | move | `git mv` → `claude/archive/{incidents,shipped,superseded}/` | ✅ | 2026-08-24 — 71 files: 18 / 36 / 17 |
| 1 | move | repoint refs in code comments + inter-doc links; `go build ./...` | ✅ | 2026-08-24 — 79 lines in 53 code files, plus 21 relative links between docs |
| 1 | merge | cherry-pick conflict checking → `dag_semantics.md`, then delete `pastcone_consistency.md` | ✅ | 2026-08-24 — new §2.7; 2 code comments repointed |
| 1 | index | `archive/incidents/README.md` (18 rows) | ✅ | 2026-08-24 — 3 unclosed items surfaced |
| 1 | index | `archive/shipped/README.md` (35 rows) | ✅ | 2026-08-24 — `output_kind_index.md` moved to superseded (DEFERRED, never built) |
| 1 | index | `archive/superseded/README.md` (11 rows) | ✅ | 2026-08-24 — 7 more rebucketed to shipped after verification |
| 2 | index | status headers on surviving `claude/` files | ✅ | 2026-08-24 — all 35; `network_connectivity.md` body status corrected |
| 2 | index | rewrite `claude/` index table in `CLAUDE.md` | ✅ | 2026-08-24 — 15 keep rows + archive buckets; `sync_semantics.md` added to the core paragraph |
| 3 | site | `txdocs/native_tokens.md` | ✅ | 2026-08-24 — written to current `develop`; `tx.md` cross-link added |
| 3 | site | `txdocs/redeemer_scripts.md` | ✅ | 2026-08-24 — site `c2913a7`; `validation.md` cross-link added |
| 3 | site | `participate/delegate.md` ← scalability/freeze/top-up | ✅ | 2026-08-24 — plus 3 stale claims corrected (per-sequencer epochs, `-e` flag, askstop needing wallet funds) |
| 3 | site | `participate/mine.md` | ✅ | 2026-08-24 — site `db97d92`, branch `mining-guide`, unpushed. Sources archive after merge |
| 3 | site | single-signature / holder ID | ✅ | 2026-08-24 — `tx.md` section rewritten; audit Phase-3 items 3+5 closed |
| 4 | pkg docs | `core/README.md` | ⬜ | |
| 4 | pkg docs | `core/core_modules/forward_sync/sync.md` | ⬜ | |
| 4 | pkg docs | `sequencer/README.md` | ⬜ | |
| 4 | pkg docs | `ledger/def/easyfl.md` | ⬜ | |
| 4 | pkg docs | `ledger/limits.md` | ⬜ | |
| 4 | pkg docs | `tests/README.md` ← five runbooks | ✅ | 2026-08-24 — commands re-verified against `proxi`; `init node`/`init wallet`/`node transfer`/`--ignore-freeze-bound` were all dead |
| 4 | pkg docs | relocate peering / txlogger / utxo-indexing | ✅ | 2026-08-24 — `peering/README.md`, `peering/network_connectivity.md`, `txlogger/README.md`, `ledger/multistate/utxo_indexing.md`; 13 files repointed |
| 5 | trackers | merge `docs.md` + `docs_site_audit.md` | ⬜ | |
| 6 | overview | settle the spine + `overview/` restructure with the user | ⬜ | gate before writing any page |
| 6 | overview | page 1 — what Proxima is | ⬜ | |
| 6 | overview | page 2 — tokens, inflation, fair launch | ⬜ | absorbs `overview/fair_launch.md` + `incentives.md` ← fairlaunch, launch_rationale, inflation |
| 6 | overview | page 3 — participate, three roles | ⬜ | |
| 6 | overview | page 4 — how transactions work | ⬜ | |
| 6 | overview | `overview/delegation.md` ← conceptual half | ⬜ | |
| 6 | overview | `overview/consensus.md` ← tick duration | ⬜ | |

## Inventory (Phase 0)

107 tracked files plus this plan. Columns: **size**; **date** = last commit
that changed content, ignoring the `47a31ba7` IP-scrub commit of 2026-08-19,
which touched 12 files without saying anything; **refs** = inbound references
from anywhere in the repo other than this plan, split code / doc; **→** =
proposed destination; **verdict**.

Destinations: `keep` · `site` (rewritten onto the docs site, then archived) ·
`pkg:<path>` (rewritten as a package doc, then archived) · `inc` / `ship` /
`sup` (`archive/incidents` / `shipped` / `superseded`).

**⚠ marks a verdict that contradicts the document's own status header**, found
by checking the claim against `develop`. There are five, and all five would
have been filed wrongly from the title alone.

| File | Size | Date | Refs | → | Verdict |
|------|------|------|------|---|---------|
| `TODO.md` | 14K | 2026-08-17 | doc 3 | keep | Cross-session follow-ups. Meta. |
| `adaptive_rate_control.md` | 34K | 2026-04-01 | doc 1 | sup | Largest of four rate-control drafts, never reconciled with the other three. |
| `async-sequencer-plan.md` | 9K | 2026-03-27 | — | ship | `sequencer/strategy_async.go` exists. No status header; provisional. |
| `attachment_cost.md` | 18K | 2026-07-01 | — | ship? | Attachment cost/budget. No status header, no code ref — **unverified**. |
| `attachment_time.md` | 22K | 2026-04-27 | — | inc | Attachment-latency investigation, one measurement window. |
| `big_tasks.md` | 1K | 2026-02-07 | — | sup | Stub task list from February. |
| `bootstrap_transactions.md` | 21K | 2026-07-31 | code 1 | ship | Per-proposal bootstrap mode shipped; `global/global.go` cites it. |
| `bottleneck.md` | 10K | 2026-03-15 | — | inc | Throughput bottleneck hunt, resolved. |
| `branch_cost.md` | 23K | 2026-05-25 | doc 1 | pkg:sequencer | Branch-cost analysis; source for `sequencer/README.md`. |
| `branch_fork_convergence.md` | 6K | 2026-08-11 | CLAUDE.md | keep | Proposal, not implemented. Measurement stands. |
| `chain_constraint.md` | 7K | 2026-02-07 | code 1, doc 1 | ship | Chain constraint is in `develop`; test cites it. |
| `chain_constraint2.md` | 1K | 2026-02-24 | code 1 | ship | One-page addendum to the above. |
| `chain_explorer.md` | 19K | 2026-07-14 | code 1, doc 1 | ship | `api/chain_explorer/` exists. |
| `chainid32to24.md` | 8K | 2026-06-01 | — | ship ⚠ | Header says "analysis only (not implemented)". `ledger/base/id.go:23` has `ChainIDLength = 24`. **Header is stale; it shipped.** |
| `committed_branch_baseline_wedge.md` | 2K | 2026-07-02 | — | inc | One wedge, diagnosed. |
| `conflict.md` | 20K | 2026-04-02 | doc 1 | inc | Conflict-handling investigation, superseded by `sequencer_conflict_resolution.md`. |
| `consensus_halt_2026-04-23.md` | 11K | 2026-04-23 | — | inc | Dated in its own filename. |
| `coverage_delta_enforcement_fix.md` | 8K | 2026-06-11 | — | inc | Single fix, landed. |
| `crash.md` | 4K | 2026-03-18 | doc 1 | inc | |
| `crash2.md` | 34K | 2026-04-12 | — | inc | |
| `crash3.md` | 14K | 2026-04-13 | — | inc | |
| `credit_tokens.md` | 12K | 2026-08-02 | CLAUDE.md | keep | Research, explicitly undecided and unimplemented. |
| `dag_semantics.md` | 23K | 2026-06-17 | code 1, doc 2, CLAUDE.md | keep | **Hard constraint.** Never archived, never absorbed. |
| `dag_visualizer.md` | 4K | 2026-03-08 | — | ship ⚠ | Superseded by nothing — it shipped: `api/dagviz/`, `api/streaming/dag_vertex_server.go`, `proxi db txstore dag_explorer`. Rebucketed 2026-08-24. |
| `deferred_commit.md` | 2K | 2026-05-06 | — | ship? | Two pages on DB update batching. Only "ref" is a permissions entry in `.claude/settings.local.json`, not a citation — **unverified**. |
| `delegate_lock.md` | 3K | 2026-03-07 | doc 1 | ship | `delegateLock` is live and much evolved past this note. |
| `delegation_add_tokens.md` | 14K | 2026-08-16 | code 4 | ship ⚠ | Header says "spec, not implemented". Shipped `f9eaa187` 2026-08-16 — *the day after it was written*. `proxi/node_cmd/delegate/topup.go` + `--add`. **Was on my keep list; wrong.** |
| `delegation_allowance.md` | 14K | 2026-08-24 | code 1, CLAUDE.md | ship | Shipped `4a393c1d`. CLAUDE.md index row moves with it. |
| `delegation_epoch_params.md` | 31K | 2026-08-24 | code 6, doc 3 | ship | Shipped. Six code comments to repoint. |
| `delegation_freeze_distribution.md` | 32K | 2026-07-06 | code 4, doc 1 | keep | Load-vector model still open; cited from `delegationpool` and `task/proposal.go`. Also feeds `overview/delegation.md`. |
| `delegation_params_position_bug.md` | 10K | 2026-05-25 | — | inc | One bug, fixed. |
| `delegation_scalability.md` | 29K | 2026-08-18 | code 1, doc 2 | keep | §8/§9 implemented, model live. Drives the fair-launch sizing. |
| `dex_orders.md` | 18K | 2026-05-16 | code 5, doc 1 | ship ⚠ | No status header. `ledger/lock_dex_orders.go`, `ledger/def/lock_dex_orders.easyfl`, `ledger/tests/dex_orders_test.go`, `examples/dex/` with two tests. **Was on my "open research" list; it shipped.** |
| `docs.md` | 11K | 2026-06-03 | doc 1, CLAUDE.md | keep | Meta; merged into one tracker in Phase 5. |
| `docs_site_audit.md` | 20K | 2026-06-09 | — | keep | Meta; same merge. |
| `easyfl.md` | 7K | 2026-05-07 | code 1, doc 2, CLAUDE.md | pkg:ledger/def | Proxima-specific builtins → `ledger/def/easyfl.md`. |
| `endorsement.md` | 4K | 2026-02-07 | doc 1 | ship | Endorsement rules are in the ledger. |
| `fairlaunch.md` | 27K | 2026-08-18 | doc 2 | keep + site | Live design **and** the source for `overview/fair_launch.md` (Phase 6). Keep until then. |
| `fix-detach-reattach-race.md` | 12K | 2026-04-25 | — | inc | One race, fixed. |
| `forced_delegation.md` | 23K | 2026-06-02 | — | keep | "Draft / spec only. No implementation." Verified: no code. |
| `fork_detection_recovery.md` | 15K | 2026-07-03 | code 4 | inc | Incident, but **four code comments cite it** — repoint required. |
| `forward_sync_lineage_nonstitch.md` | 23K | 2026-06-25 | doc 1 | inc | Redesign shipped; the note is the working record. |
| `forward_sync_oom.md` | 10K | 2026-08-04 | doc 1 | inc | |
| `frozen_coverage.md` | 14K | 2026-05-28 | code 2 | ship | Shipped, then superseded again by the frozen-coverage *bound*. Repoint 2. |
| `get_outputs.md` | 12K | 2026-05-06 | code 3 | ship | API endpoint live. Repoint 3. |
| `hands_on_plan.md` | 7K | 2026-01-18 | doc 1 | sup | Oldest file in the directory. |
| `hands_on_proxi_script.md` | 8K | 2026-06-10 | doc 1 | ship | One of five overlapping local-network runbooks. |
| `has_tx_refactor.md` | 2K | 2026-03-11 | — | ship? | Trie transaction records. **Unverified.** |
| `inflation.md` | 9K | 2026-08-09 | doc 1 | site | → `overview/incentives.md` (Phase 6), then archive. |
| `key_management.md` | 2K | 2026-02-08 | — | sup | Two pages, overtaken by the keystore as built. |
| `known_baseline_attacher.md` | 7K | 2026-06-26 | — | inc | |
| `launch_rationale.md` | 32K | 2026-08-17 | code 10, doc 4 | keep | **Cited from ten ledger files** — genesis, mine lock, chain, tx parse. The most load-bearing non-constraint doc here. |
| `library_upgrade.md` | 25K | 2026-06-03 | doc 2 | ship | Self-declared "✅ COMPLETED". |
| `limits.md` | 3K | 2026-02-07 | doc 1 | pkg:ledger | → `ledger/limits.md`. |
| `local_3node_testnet.md` | 7K | 2026-07-03 | — | ship | Runbook 2 of 5. |
| `local_script.md` | 14K | 2026-05-10 | code 3 | site | → `txdocs/redeemer_scripts.md` (Phase 3), then `ship`. Repoint 3. |
| `local_testnet_edge_cases.md` | 3K | 2026-07-03 | doc 1 | ship | Runbook 3 of 5. |
| `local_testnet_runbook.md` | 6K | 2026-06-19 | doc 2 | ship | Runbook 4 of 5. |
| `memory_leak.md` | 0K | 2026-03-02 | — | delete | 402 bytes, 18 lines, cited nowhere. Deleted 2026-08-24. |
| `metadata-refactor.md` | 29K | 2026-05-06 | code 1, doc 1 | ship | TxMetadata removal shipped. Repoint 1. |
| `mining-bias.md` | 7K | 2026-08-15 | code 1, doc 3 | site | → `participate/mine.md` (Phase 3), then archive. |
| `mining_tx_streaming.md` | 16K | 2026-08-15 | code 2, doc 2 | site + ship ⚠ | Header: "Status: **IMPLEMENTED**". **Was on my keep list as live design; it is shipped.** Feeds `participate/mine.md`, then archives. |
| `monitor.md` | 21K | 2026-08-17 | code 1, doc 1 | keep | "spec 0, provisional, for approval" — genuinely open. |
| `native_token.md` | 17K | 2026-08-24 | code 6, doc 4 | site | → `txdocs/native_tokens.md` (Phase 3), then `ship`. Repoint 6. |
| `native_token_tag_32vs20.md` | 9K | 2026-05-28 | — | sup ⚠ | Header: "**SHELVED (keep 32-byte tags)**". Not shipped — belongs in `superseded`, and my draft had it under `shipped`. |
| `network_connectivity.md` | 19K | 2026-06-20 | code 4, doc 1 | moved | → `peering/network_connectivity.md` (2026-08-24) | Live: cited from `peering/connectivity.go`, `node`, `api`, `global`. |
| `network_rtt_mapping.md` | 9K | 2026-06-20 | code 1, doc 1 | ship ⚠ | Layers 1–3 shipped (`api/server/netviz.go`); only the offline simulator was never built. Rebucketed 2026-08-24. |
| `nolock-traversal.md` | 22K | 2026-04-13 | — | ship | Lock-free past-cone traversal is in `develop` and is named in CLAUDE.md's race-detector rule. |
| `one_node_bootstrap.md` | 16K | 2026-05-23 | doc 2 | ship | Runbook 5 of 5. |
| `output_kind_index.md` | 23K | 2026-05-31 | code 1 | sup ⚠ | Header: "**DEFERRED** — not being built"; the in-trie index was dropped for an async external index. Filed `shipped` in the draft; corrected 2026-08-24. |
| `output_parsing.md` | 8K | 2026-05-06 | — | ship | "Phase 1 shipped"; the rest overtaken by the wasm refactor. |
| `pastcone_consistency.md` | 28K | 2026-07-17 | code 2 | delete | Obsolete. Cherry-pick the conflict-checking logic into `dag_semantics.md` first, then delete. The 2 code comments repoint to `dag_semantics.md`. |
| `peering.md` | 19K | 2026-04-21 | — | moved | → `peering/README.md` (2026-08-24) | |
| `proxi_txbuildercore.md` | 12K | 2026-05-20 | code 1, doc 1 | ship | proxi sweep complete. |
| `rate_control_non_seq.md` | 5K | 2026-03-23 | doc 2 | sup | Draft 2 of 4. |
| `ratecontrol.md` | 9K | 2026-03-19 | doc 4 | sup | Draft 3 of 4. |
| `ratecontrol2.md` | 9K | 2026-04-07 | — | sup | Draft 4 of 4. |
| `return_to_sender.md` | 11K | 2026-06-10 | code 5 | ship | Shipped `44de4b3f`. Repoint 5. |
| `scenario_delegation_freeze.md` | 3K | 2026-06-19 | — | sup | Test scenario, overtaken by the fixed freeze grid. |
| `send_with_deadline_lock.md` | 9K | 2026-05-12 | code 5 | ship | Lock is live. Repoint 5. |
| `seq-improvements.md` | 28K | 2026-05-06 | code 1, doc 1 | ship | "Phases 1 and 2 shipped"; later phases overtaken. |
| `seq_key.md` | 23K | 2026-02-08 | doc 1 | ship ⚠ | Key file shipped (`util/keystore`, `proxi/glb/keyfile.go`). Rebucketed 2026-08-24. |
| `seq_v080_pbc_removal.md` | 6K | 2026-04-30 | doc 1 | ship | PBC removal shipped; note records one table deliberately *not* implemented — preserve that line in the index. |
| `sequencer.md` | 23K | 2026-03-18 | doc 4, CLAUDE.md | pkg:sequencer | The TSF design. Still actively extended by `sequencer_conflict_resolution.md`, so it is a *source*, not a discard. |
| `sequencer_conflict_resolution.md` | 20K | 2026-08-17 | CLAUDE.md | keep | Deferral implemented 2026-08-17, "not yet validated under live load". |
| `snapshot_optimize.md` | 7K | 2026-03-23 | doc 2 | ship | Phase 1 implemented. |
| `stem_data_refactor.md` | 6K | 2026-06-01 | — | ship | "DONE — shipped on develop08, `1aade5a0`". |
| `stress_sequencer_shutdown.md` | 37K | 2026-08-07 | — | inc | Largest incident note. Fix `d73b4142`; one gap recorded — carry it to the index. |
| `sync.md` | 8K | 2026-08-04 | doc 3 | pkg:core/…/forward_sync | Source for the package doc. |
| `sync_findings.md` | 4K | 2026-03-20 | — | inc | |
| `sync_semantics.md` | 37K | 2026-07-03 | code 15, doc 2 | keep | **Hard constraint** (your correction). Cited from 15 Go files — more than any other doc here. Never archived, never absorbed; the package doc only links it. |
| `sync_startup.md` | 5K | 2026-07-03 | — | ship ⚠ | Shipped as `CheckAndRestoreOnStartup` + `snapshot_restore`. Rebucketed 2026-08-24. |
| `tag_along.md` | 3K | 2026-08-24 | doc 1 | ship | Tag-along is live and documented on the site. |
| `target_info.md` | 6K | 2026-03-06 | — | ship ⚠ | Shipped as `proxi/node_cmd/delegate/target_info.go`. Rebucketed 2026-08-24. |
| `tick_duration.md` | 14K | 2026-08-09 | doc 1 | site | One paragraph → `overview/consensus.md` (Phase 6); the measurement stays, archived. |
| `trie_iteration.md` | 12K | 2026-04-25 | code 1 | inc | "analysis and fix proposal", fix landed. Repoint 1. |
| `tx_test.md` | 22K | 2026-03-07 | — | ship ⚠ | "All topics completed"; the tests are in `ledger/tests/`. Rebucketed 2026-08-24. |
| `txflow2.md` | 14K | 2026-03-29 | — | ship? | Transaction-flow refactor. **Unverified.** |
| `txid_ttl_tiered.md` | 16K | 2026-06-28 | code 11 | ship | "IMPLEMENTED on develop". **Eleven code comments to repoint** — the largest single repoint. |
| `txlogger.md` | 13K | 2026-01-26 | — | moved | → `txlogger/README.md` (2026-08-24) | |
| `txstore_audit.md` | 17K | 2026-04-29 | — | ship ⚠ | Shipped as `proxi/db_cmd/txstore/audit.go`. Rebucketed 2026-08-24. |
| `unitrie_double_booking_proposal.md` | 6K | 2026-03-14 | — | sup | Proposal against `unitrie`, not taken up. |
| `utxo-indexing.md` | 33K | 2026-05-05 | code 5, doc 1, CLAUDE.md | moved | → `ledger/multistate/utxo_indexing.md` (2026-08-24) | CLAUDE.md already points at it as the design rationale for the UTXO tuple. |
| `wallet_eval_api.md` | 13K | 2026-05-20 | code 4 | ship | `/eval` + `/ledger_constants` live. Repoint 4. |
| `wasm_easyfl.md` | 8K | 2026-05-20 | code 1, doc 1 | ship | |
| `wasm_txbuilder.md` | 30K | 2026-06-02 | code 4, doc 2 | ship | |
| `wasm_txbuilder_helpers.md` | 23K | 2026-05-20 | code 1, doc 1 | ship | |
| `kb_reorg.md` | 17K | untracked | — | keep | This plan. |

### What Phase 0 changed in the plan

**Thirteen misfilings in the end, every one caught by checking `develop`
rather than the document.** Five were found in Phase 0 itself; writing the
archive indexes found eight more, because a row that has to name the artefact
proving a claim cannot be written from a title. Seven of those eight had been
filed `superseded` and were in fact shipped — `dag_visualizer.md`,
`network_rtt_mapping.md`, `seq_key.md`, `sync_startup.md`, `target_info.md`,
`txstore_audit.md`, `tx_test.md` — and one, `output_kind_index.md`, was filed
`shipped` while its own body said the design was deferred and never built. The
lesson generalizes: **classification by title is unreliable at roughly one file
in eight.**

The five found in Phase 0:
Three documents call themselves unimplemented and are not
(`chainid32to24.md`, `delegation_add_tokens.md`, and `dex_orders.md`, which
has no header at all); two I had filed as live design or research are
finished work (`mining_tx_streaming.md` says IMPLEMENTED in its own header;
`native_token_tag_32vs20.md` says SHELVED, so it is superseded, not shipped).
A document's status line is evidence, not truth — it is written once and never
revisited.

**The archive move is not purely mechanical.** 140 references to `claude/*.md`
live in **code comments**. 32 point at documents that stay put; the other
**108, across 67 Go and EasyFL files**, break the moment the `git mv` runs. The worst
are `txid_ttl_tiered.md` (11 files), `delegation_epoch_params.md` and
`native_token.md` (6 each), `return_to_sender.md`, `send_with_deadline_lock.md`
and `dex_orders.md` (5 each). This needs its own tracked step, run as one sweep
immediately after the `git mv`, with `go build ./...` after. Phase 1 gains a
row for it.

**Two files are deleted rather than archived** (decided 2026-08-24):
`memory_leak.md`, 402 bytes and cited nowhere; and `pastcone_consistency.md`,
obsolete — but its conflict-checking logic is worth keeping, so it is
cherry-picked into `dag_semantics.md` before the file goes, and its two code
comments repoint there. That edit touches a hard-constraint document, so the
extracted text is approved before it lands. Four files are marked `?` as unverified
(`attachment_cost.md`, `deferred_commit.md`, `has_tx_refactor.md`,
`txflow2.md`): all four are old, unreferenced, and headerless, so the cost of
being wrong is one `git mv`.

**Counts** (108 rows = 107 files + this plan): keep 15 · delete 2 · site 6 ·
pkg 14 · incidents 18 · shipped 42 · superseded 11. So `claude/` ends at 15
files: 71 archived directly, 20 rewritten into the site or a package first and
archived after, 2 deleted.
