# docs.md — Proxima documentation: plan, status, progress

Tracking document for the effort to bring Proxima's documentation in line
with the current codebase, and ultimately to migrate most of it to the
public docs site.

## Goal

Make Proxima's documentation accurate, consistent, and approachable for
**users** (not developers), grounded in the **current `develop` branch**.

## Scope and sources

Documentation lives in several places:

1. **Package-local documents** (proxima repo) — a developer document lives in
   the package it documents. `api/api.md`, `api/txapi.md`, `global/logging.md`,
   `ledger/upgrade.md` and the package `README.md` files. There is no `docs/`
   directory; it was emptied on 2026-08-24 and the two API references moved
   into `api/`.
2. **`README.md`** (repo root) — landing page / overview.
3. **`lunfardo314.github.io` (the "docs site")** — public, user-facing docs.
   Reconciled with `develop` in the 2026-06 audit; the user-facing operational
   guides live there under `participate/`.

Order of work:
- **Phase 1 (now):** make proxima-repo docs consistent with the current
  codebase.
- **Phase 2 (later):** update the docs site; migrate most repo docs to it.

## Working principles

When reviewing and editing docs, follow these (unless explicitly stated
otherwise for a given doc):

- **User-facing, not developer docs.** The audience has limited knowledge
  of Proxima.
- **Explain introduced concepts in a generic manner.** Don't assume the
  reader already knows Proxima-specific terms; introduce them plainly.
- **Simple language.** No coder slang, no crypto slang, not verbose.
- **Step-by-step.** Edit one document at a time, do not overwhelm user with parallel edits on different topics. User defines the topic and specific doc file

## Process

- A separate agent will do the doc review/editing work.
- The code baseline is the current `develop` branch — verify claims against
  the actual code before writing.
- In most cases code comments give truth, however not always. If comment should be edited, do it or discuss with user.  
  Do not do any edits of the code, always ask user.
- Update the status table below as each doc is reviewed/edited.

## Status: `docs/` directory

Legend: ✅ up to date · 🔶 needs review/edit · ⬜ not started this effort

| Doc | Status | Topic | Notes |
|-----|--------|-------|-------|
| `docs/run_standalone.md` | ✅ up to date | Throwaway single-node network with bootstrap sequencer (frontend/wallet/browser devs). Companion to WASM wallet README. | |
| `docs/node_config.md` | ✅ up to date | Reference for all `proxima.yaml` node config tags. | |
| `docs/wallet_config.md` | ✅ up to date | Reference for `proxi.yaml` wallet profile tags. | |
| `docs/run_access.md` | ✅ up to date | Run an access node and sync with the testnet. | Updated with the other run_* docs; CLAUDE.md's OUTDATED label is stale. |
| `docs/run_sequencer.md` | ✅ up to date | Run a sequencer node. | Same — stale CLAUDE.md label to be corrected. |
| `api/txapi.md` | ✅ new | `/txapi/v1` transaction-building/parsing API. | Created 2026-06-03. Extracted + refreshed the TX API: request syntax + field tables (payload blobs dropped per user). Fixes vs old api.md: get_txbytes has no tx_metadata; get_parsed_transaction has no tx_metadata/sender (+endorsements); get_vertex_dep reworked to compact keys (a/i/seqid/cd/supply/in/…); parse_output_data `human_readable` param. Verified vs `api/server/txapi.go` + `api/api.go` structs. |
| `api/api.md` | ✅ | `/api/v1` + WebSocket node API reference. | Rewritten 2026-06-03 from a full handler inventory. Dropped TX API (→txapi.md) + retired legacy account-query endpoints (get_account_*, get_outputs_for_amount, get_nonchain_balance, get_chain_outputs, get_ledger_id_data, get_delegations_by_sequencer, query_inclusion_score). Added eval (POST), ledger_constants, get_outputs (unified), get_sequencers, get_sequencer_target_info, get_inactive, get_branch_list, get_snapshot*, dag_explorer/* + chain_explorer/* backends, txlog/*. Field-table style; common shapes factored out; WebSocket at /wsapi/v1. |
| `ledger/multistate/snapshot_format.md` | ✅ moved | Multi-state snapshot file format. | Reviewed + rewritten 2026-06-03 vs develop: ver 1→ver 2 (libraries YAML→JSON, `LibraryJSON`), RootRecord now 2 fields (Root+SequencerID, base.ChainID; aggregates moved to stem output→BranchData), trie partitions corrected (0x00 UTXO / 0x01 Controllers ACCN / 0x02 ChainID CHID), file naming = dashed branchID (no literal genesis.snapshot), added `snapshot db` cmd + restore batch flag, ver 2 history row. Relocated from docs/. |
| `ledger/upgrade.md` | ✅ moved | Ledger library upgrade mechanism. | Reviewed 2026-06-03 vs develop: mostly accurate (tracked JSON cutover). Fixed upgrade-UTXO layout (6 elements, not 5 — index 1 is index-values, lock at 2, hashes/slot at 3/4/5), `proxi snapshot create`→`snapshot db`, port 8080→8000, Step-5 note (def_upgrade0 is genesis builder, no def_upgradeN yet), file-ref `HasPendingUpgradeForSlot`. Relocated from docs/; library_upgrade.md links repointed. |
| `docs/delegate.md` | ✅ | Delegation concepts and commands. | Rewritten 2026-06-03 vs develop. Fixed epoch (600 slots/~1.7h, per-sequencer), max frozen epochs (default 20, range 8-32), inflation cut/advance/affordability economics, added estimate + target_info, `--cut` flag, statuses. |
| `docs/proxi.md` | ✅ | `proxi` CLI wallet/tool usage. | Rewritten 2026-06-03 vs develop. init wallet→config wallet, transfer→send, key in .key file, dashed IDs, `a/`/`c/`/`$/` forms, faucet+spammer removed, getting-started scope. |
| `global/logging.md` | ✅ moved | Logging and tracing configuration. | Reviewed 2026-06-03: fully consistent with `global/` code (config keys, `LogTopicf`/`WarnTopicf`/`TopicVerbosityLevel`, all 8 topics + levels). No content change; relocated from `docs/logging.md` into the owning package. |
| `docs/testnet.md` | ✅ | Testnet topology and operations. | Rewritten 2026-06-03 vs develop. transfer→send, a(0x)→a/, dropped version-history + APR figure, kept getfunds/faucet (host key; faucet temporarily offline note), endpoints :8001. |

> The four technical docs are handled differently (user direction 2026-06-03):
> `logging` / `snapshot_format` / `upgrade` are **developer docs** — review for
> consistency, then relocate into their owning package (topic-named .md).
> `api.md` becomes separate **dev-oriented** API docs (placement TBD).

## Status: README and package readmes

Package READMEs stay **developer-facing**. The effort only fixes their
accuracy against the current code — no rewrite into user-facing style.
The root `README.md` is the exception: it is the user landing page.

| File | Status | Notes |
|------|--------|-------|
| `README.md` (root) | ✅ | User-facing overview / landing page. Reviewed + de-verbosed 2026-06-03: condensed 26-bullet Highlights → 12, tightened intro/positioning, fixed grammar ("because because", "It a sort of"), dropped stale "(outdated somehow)" note + stray bullet, added run_standalone link. Docker + all tutorial links verified live. |
| `core/memdag/README.md` | 🔶 | Developer doc — accuracy fix only. |
| `core/attacher/README.md` | 🔶 | Developer doc — starts with "TODO needs review". |
| `ledger/txbuildercore/wasm/README.md` | ✅? | Companion to run_standalone.md; likely current. |
| `tests/nodes/README.md` | ⬜ | Test infra; developer-only. |
| `tests/node-docker-setup/readme.md` | ⬜ | Test infra; developer-only. |

## Status: docs site (`lunfardo314.github.io`)

Phase 2. Inventory pending. Currently outdated.

## Progress log

- 2026-06-03 — Created this tracking doc. Inventoried `docs/`, README, and
  package readmes against CLAUDE.md's index.
- 2026-06-03 — Corrected CLAUDE.md `docs/` index (run_access / run_sequencer
  → up to date; intro sentence reworded).
- 2026-06-03 — Rewrote `docs/proxi.md` against develop (see status note).
  Marked example `holder_id` as illustrative.
- 2026-06-03 — Code fix (user-approved): corrected stale comment in
  `proxi/node_cmd/send.go` — chainLock target is `c/<24-byte hex>`, not
  `c/<32-byte hex>` (`ChainIDLength = 24`). Two occurrences (header block +
  cobra Long help).
- 2026-06-03 — Rewrote `docs/delegate.md` against develop (see status note).
  Decisions: per-sequencer framing for epoch/max-epochs; cut economics
  documented in depth; added `estimate` + `target_info`; doc uses `on hold`.
- 2026-06-03 — Rewrote `docs/testnet.md` against develop (see status note).
  Faucet kept per user direction (re-enabled later, CLI stable); config key
  corrected `addr`→`host` to match `faucet_get.go`. Endpoints kept on :8001
  per user; version-history paragraph + 9-10% APR dropped per user.
- 2026-06-03 — Code fix (user-approved): reconciled on-hold label —
  `proxi/node_cmd/delegate/status.go:70` now prints `on hold` (was `REVOKED`),
  matching `proxi/glb/display_chains.go:38`. No other user-facing `REVOKED`
  strings remain; `go build ./proxi/...` clean.
- 2026-06-03 — Reviewed `logging.md`: fully consistent with `global/` code, no
  content change. Relocated `docs/logging.md` → `global/logging.md` (git mv).
  Updated CLAUDE.md docs index: dropped logging row; marked proxi/delegate/
  testnet as up to date (reconcile done). Decisions for the technical docs:
  topic-named .md in package; upgrade → `ledger/`; move + update refs.
- 2026-06-03 — Added Web tools section to `testnet.md` (chain_explorer,
  dag_explorer, dagviz, peers; paths verified against `api/api.go`).
- 2026-06-03 — Reviewed + de-verbosed root `README.md` per user (make it less
  verbose): Highlights 26→12 bullets, tighter intro, grammar/link fixes,
  added run_standalone. All tutorial links verified.
- 2026-06-03 — Reviewed + rewrote `snapshot_format.md` (heavily stale): ver 2 /
  JSON, 2-field RootRecord, corrected trie partitions, file naming, CLI
  (`snapshot db`, restore flags), ver 2 history. Relocated → `ledger/multistate/`.
  Updated CLAUDE.md index (dropped row) and `claude/library_upgrade.md` link.
- 2026-06-03 — API docs structure decided (several .md by URL prefix). Created
  `docs/txapi.md` for `/txapi/v1` (field-table style, payload blobs dropped per
  user).
- 2026-06-03 — Rewrote `docs/api.md` as the `/api/v1`+WebSocket reference from a
  full handler inventory (agent-assisted, file:line verified). Dropped TX API +
  retired legacy endpoints; added ~12 new ones (eval, ledger_constants,
  get_outputs, sequencers, target_info, inactive, branch_list, snapshot*,
  explorer backends, txlog). CLAUDE.md docs/ index now all up to date; intro
  reworded; txapi.md row added. **Phase 1 (repo docs) essentially complete** —
  only the two core/ package READMEs (memdag, attacher) remain optional.
- 2026-06-03 — Reviewed `upgrade.md` (mostly current): fixed 6-element upgrade-UTXO
  layout, `snapshot create`→`snapshot db`, port, Step-5 note, file-ref. Relocated
  → `ledger/upgrade.md`; repointed all 4 `claude/library_upgrade.md` links.
  OPEN code-comment issue (flagged, not edited): `ledger/upgrade_utxo.go:6-11`
  header comment still labels the chain slots "Constraint 2/3/4" while the parser
  reads 3/4/5 (6-element layout). Likely origin of the doc's off-by-one.
