# docs.md — Proxima documentation: plan, status, progress

Tracking document for the effort to bring Proxima's documentation in line
with the current codebase, and ultimately to migrate most of it to the
public docs site.

## Goal

Make Proxima's documentation accurate, consistent, and approachable for
**users** (not developers), grounded in the **current `develop` branch**.

## Scope and sources

Documentation lives in several places:

1. **`docs/` directory** (proxima repo) — primary working area for now.
2. **`README.md`** (repo root) — landing page / overview.
3. **Package `README.md` files** — scattered, currently developer-oriented.
4. **`lunfardo314.github.io` (the "docs site")** — public docs, currently
   **outdated**. Worked on later. Most `docs/` content will ultimately move
   here.

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
| `docs/api.md` | 🔶 | REST/WebSocket API endpoint reference. | Large (38 KB); verify against `api/`. |
| `docs/snapshot_format.md` | 🔶 | Multi-state snapshot file format. | |
| `docs/upgrade.md` | 🔶 | Ledger library upgrade mechanism. | |
| `docs/delegate.md` | ✅ | Delegation concepts and commands. | Rewritten 2026-06-03 vs develop. Fixed epoch (600 slots/~1.7h, per-sequencer), max frozen epochs (default 20, range 8-32), inflation cut/advance/affordability economics, added estimate + target_info, `--cut` flag, statuses. |
| `docs/proxi.md` | ✅ | `proxi` CLI wallet/tool usage. | Rewritten 2026-06-03 vs develop. init wallet→config wallet, transfer→send, key in .key file, dashed IDs, `a/`/`c/`/`$/` forms, faucet+spammer removed, getting-started scope. |
| `docs/logging.md` | 🔶 | Logging and tracing configuration. | |
| `docs/testnet.md` | 🔶 | Testnet topology and operations. | Linked from README; oldest. |

> Note: CLAUDE.md's `docs/` index still marks `run_access.md` /
> `run_sequencer.md` as OUTDATED. That label is stale — they are up to date.
> First action: reconcile CLAUDE.md's `docs/` index with this table.

## Status: README and package readmes

Package READMEs stay **developer-facing**. The effort only fixes their
accuracy against the current code — no rewrite into user-facing style.
The root `README.md` is the exception: it is the user landing page.

| File | Status | Notes |
|------|--------|-------|
| `README.md` (root) | 🔶 | User-facing overview / landing page. |
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
- 2026-06-03 — Code fix (user-approved): reconciled on-hold label —
  `proxi/node_cmd/delegate/status.go:70` now prints `on hold` (was `REVOKED`),
  matching `proxi/glb/display_chains.go:38`. No other user-facing `REVOKED`
  strings remain; `go build ./proxi/...` clean.
