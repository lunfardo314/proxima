# Chain explorer

## Context

In Proxima a _chained account_ ("chain") is a UTXO covenant whose `chain` constraint at constraint index 3 preserves a stable `chainID` across transitions. It is a first-class citizen of the ledger:

- **sequencer chains** — `chain` + `sequencer(epochSlots, maxFrozenEpochs)` at constraint index 4 (+ milestone data at constraint index 5 on milestones).
- **foundry chains** — `chain` + `foundry(supply)` at constraint index 4 (+ optional `foundryPolicy` at constraint index 5). The chain ID *is* the native-token tag.
- **delegation chains** — `chain` + delegate lock (sigLock owned by master, with target sequencer ID and frozen-coverage state at the last position).
- **generic chains** — `chain` only, no role-typing constraint.

Today the project runs up to 100 chains. Projected scale is at thousands to at least hundreds of thousands. The only inspection tools are CLI: `proxi node allchains`, `proxi node balance`, `proxi node chain <id>`. There is no browser-based view, and the existing JSON API surface (`/api/v1/get_all_chains`, `/api/v1/get_chain_output`, `/api/v1/get_sequencers`, `/api/v1/get_sequencer_target_info`) was designed for spot lookups, not bulk filtered browsing.

This spec defines a browser-served chain explorer mounted into the node API server, modeled on the existing DAG explorer (`api/dag_explorer/`).

## Goals

- Single-page browser UI dedicated to chained accounts.
- Server-rendered list, no client-side bulk fetch of the whole chain set.
- Rich filtering and grouping, scoped to the latest reliable branch (LRB).
- Per-row drill-down to type-specific details and natural cross-links (master → delegation, target → sequencer, foundry → minted-token holders).
- Periodic auto-refresh on a sane cadence; cap on rows per render.

## Non-goals

- Historical chain state per slot. Always operates on the LRB. (A `?slot=N` query parameter MAY be added later but is out of scope for v1.)
- Transaction-graph / past-cone visualisation. That is the responsibility of the existing `/api/v1/dag_explorer/*` views.
- Wallet operations (mint, delegate, transfer, etc.) — read-only browsing.
- Mobile-first layout. Aimed at developer/operator workstations.

## Mounting and packaging

Lives in a new package `api/chain_explorer/` mirroring `api/dag_explorer/`:

```
api/chain_explorer/
  chain_explorer.go    # handlers + JSON types
  chain_explorer.html  # embedded SPA (HTML + inline CSS + inline JS)
```

Registered from `api/server/server.go` alongside the DAG explorer:

```go
// Chain explorer (HTML page + JSON APIs)
chain_explorer.Register(srv.addHandler, srv)
```

The handler set receives the live node's `multistate.SugaredStateReader` (via the `withLRB` callback used by every other state-query handler), the wallet `txbuildercore.Library` for bytecode parsing, and the `Constants` for slot ↔ wall-clock translation.

### Paths

| Path | Method | Purpose |
|------|--------|---------|
| `/api/v1/chain_explorer` | GET | Serves the embedded HTML SPA |
| `/api/v1/chain_explorer/list` | GET | Filtered chain list (table source) |
| `/api/v1/chain_explorer/chain` | GET | Per-chain detail (drill-down) |

All three are read-only and operate on the LRB. Pivots like "all chains controlled by holder X" are expressed as `/list?index_value=…` rather than a dedicated endpoint.

## `GET /api/v1/chain_explorer/list`

The primary endpoint. Single JSON response, no pagination.

### Query parameters

| Param | Type | Default | Notes |
|-------|------|---------|-------|
| `max` | int | 200 | Hard cap on rows returned. Server-enforced ceiling: 2000. |
| `kind` | enum | `all` | One of `all`, `sequencer`, `foundry`, `delegation`, `generic`. |
| `index_value` | hex | — | 32-byte value. Returns only chains whose `index_values` tuple at constraint index 1 contains this entry (sigLock holder, chainLock chainID, or delegation master / target). Matches the semantics of the existing `get_outputs?index_value=…` endpoint. |
| `delegation_target` | hex | — | 32-byte sequencer chainID. Convenience filter for `kind=delegation` chains whose `index_values[1]` equals this value. |
| `delegation_master` | hex | — | 32-byte holderID. Convenience filter for `kind=delegation` chains whose `index_values[0]` equals this value. |
| `active_within_slots` | uint32 | — | Excludes chains whose last-transit slot is older than `lrbSlot - N`. |
| `balance_min`, `balance_max` | uint64 | — | Inclusive bounds on the chain's on-chain PRXI balance. |
| `sort_by` | enum | `balance` | One of `balance`, `chain_id`, `last_active`, `transitions`. |
| `sort_order` | enum | `desc` | `asc` or `desc`. |

Unknown parameters are rejected with HTTP 400 so the UI fails loudly during development.

### Response

```json
{
  "lrbid": "000002590101d9ca...",
  "wall_clock_unix": 1779862439,
  "total_supply": 1000019078004787,
  "matched": 1432,
  "returned": 200,
  "truncated": true,
  "rows": [
    {
      "chain_id": "9d2c6fedeb0f...",
      "output_id": "000002500202...",
      "kind": "sequencer",
      "balance": 999519070270137,
      "frozen": 20015429600,
      "origin_slot": 0,
      "transition_counter": 4827,
      "branch_counter": 601,
      "last_active_slot": 601,
      "index_values": ["9d2c6fedeb0f..."],
      "sequencer": {
        "name": "boot",
        "epoch_slots": 8192,
        "max_frozen_epochs": 8,
        "profit_margin_promille": 100
      }
    },
    {
      "chain_id": "4ad4b20a47f4...",
      "output_id": "0000019cfa02eb83...",
      "kind": "foundry",
      "balance": 41000000,
      "origin_slot": 412,
      "transition_counter": 0,
      "index_values": ["fb03128a43df11..."],
      "foundry": {
        "supply": 0,
        "policy": "none"
      }
    },
    {
      "chain_id": "1a2b3c4d...",
      "output_id": "...",
      "kind": "delegation",
      "balance": 50000000000,
      "origin_slot": 250,
      "transition_counter": 17,
      "index_values": ["fb03128a43df...", "9d2c6fedeb0f..."],
      "delegation": {
        "target_name": "boot",
        "required_inflation_cut_promille": 900,
        "max_frozen_epochs": 4,
        "status": "active",
        "frozen_until_epoch": 0
      }
    }
  ]
}
```

`matched` is the count *before* the `max` truncation, so the UI can warn "showing 200 of 1432; refine your filters."

`index_values` is the raw output index-values tuple at constraint index 1 — the same entries the trie indexes under `TriePartitionControllers`. Returned as an array of hex strings, one per entry. Per-kind conventions:

- **sequencer** — `[chainID]` (the chain self-locks via chainLock).
- **foundry** / **generic** — `[holderID]` (sigLock-controlled).
- **delegation** — `[masterHolderID, targetSequencerChainID]`.

The SPA renders entries in canonical short forms (`a/<hex>` for a sigLock holder, `c/<hex>` for a chainLock chainID — matching `ControllerIDFromSource`) and uses the raw bytes as the grouping/pivot key. No `_display` field is sent; display formatting is the UI's job. Filter parameters that need to address a specific entry (master, target) are explicit query params; for opaque "anything indexed under value X" the existing `get_outputs?index_value=…` semantics apply.

`kind` discriminator rules (mutually exclusive, checked in this order):
1. constraint index 4 holds a parseable `sequencer(…)` → `sequencer`
2. constraint index 4 holds a parseable `foundry(…)` → `foundry`
3. lock at constraint index 2 parses as a delegate lock → `delegation`
4. otherwise → `generic`

(Note: a sequencer chain that is also a delegation target is still `sequencer` — the kind classifies the *output role*, not its relationships.)

### Server-side implementation sketch

The handler iterates `rdr.IterateChainedOutputs` (already exists), classifies each output with the wallet library helpers (`ParseChainConstraint`, `ParseSequencerConstraint`, `ParseFoundryBytecode`, `ParseDelegationOutput`), applies the predicate from query params, and accumulates into `rows`. Sort + truncate at the end.

For the `index_value` filter, prefer a trie-side lookup against `TriePartitionControllers` using the same path the existing `get_outputs?index_value=…` endpoint uses; fall back to the iteration filter if the indexed entry isn't a chain output.

Single in-memory pass per request; no caching. Iteration cost is O(chain count); at 100k chains this is ~tens of ms on the LRB trie. If profiling shows this is too slow, the next step is a per-LRB chain-summary cache indexed by `(branchID → []row)`, invalidated on branch advance — out of scope for v1.

### Lock-down

The handler MUST refuse to render if the LRB pointer is `nil` (node still syncing). Returns 503 with a one-line "no LRB available" message.

## `GET /api/v1/chain_explorer/chain`

Per-chain detail view. Fed by clicking a row.

### Query parameters

| Param | Type | Notes |
|-------|------|-------|
| `chain_id` | hex | Required. 32-byte chainID. |

### Response

Same shape as one entry in `/list` `rows` plus:

- `output_data_hex` — raw output bytes (so the page can do its own constraint decoding without another roundtrip).
- For `sequencer` kind: `delegations_count`, `delegated_total` (computed by re-iterating delegation chains with `delegation_target = this chainID`).
- For `foundry` kind: `tokens_outstanding` (sum of `tokenAmount(this tag, _)` across the LRB; trie walk under `TriePartitionControllers` against the `holderID || tag` compound index — see `claude/native_token.md`).
- For `delegation` kind: full `delegateLockState` decode (current status, last frozen epoch, safe revocation window if any).

Response includes `lrbid` so the UI can confirm freshness (the slot is the first 4 bytes of the txid).

## HTML SPA (`chain_explorer.html`)

Single embedded HTML file, no framework — same approach as `dag_explorer.html`. Inline CSS in the existing dark-blue palette (`#16213e` / `#0f3460` / `#6dd5ed`).

### Layout

```
+--------------------------------------------------------------+
| Sidebar (340px)                | Main pane (flex)             |
|--------------------------------+------------------------------|
| Filters:                       | LRB s601 (12 s ago)          |
|  Kind:    [all v]              |  matched 1432, showing 200   |
|  Controller: [_______]         |  [Refresh]  Auto: 10s [v]    |
|  Target:     [_______]         |  ────────────────────────────|
|  Master:     [_______]         |   #  chainID  bal  kind  …   |
|  Active in last N slots: [50]  |   1  9d2c…    1B   seq   …   |
|  Balance ≥ [__] ≤ [__]         |   2  4ad4…   41M   foundry…  |
|  Sort: [balance v] [desc v]    |   3  ...                     |
|  Max rows: [200]               |                              |
|  [Apply]                       |                              |
|--------------------------------|                              |
| Legend / colour key            |                              |
+--------------------------------+------------------------------+
```

### Table columns (default)

| # | chainID (truncated, monospace) | balance | kind (badge) | controller (truncated, click → drill) | output ID (link to DAG explorer past cone) | transitions | last active (slots ago) | type-specific |

Type-specific column is rendered conditionally:

- sequencer: `name (count/branches)`
- foundry: `supply` (`policy` icon if non-default)
- delegation: `→ target_name (cut %)` with frozen badge if status != `active`

Hovering any cell that holds an ID copies-on-click and shows the full hex in a tooltip. Clicking the row opens the detail panel (slides in from the right) populated from `/chain_explorer/chain?chain_id=…`.

### Cross-links

- click any `index_values` entry → re-runs the table with `index_value=<that entry>` (pivots to "all chains where this value is indexed").
- in the detail panel, sequencer chains show a "Delegations (N) →" button that re-runs with `kind=delegation` + `delegation_target=<this chain_id>`.
- foundry chains show "Token holders (N) →" which does a `get_outputs?index_value=<holderID||tag>` traversal (out of scope for v1 if too expensive — behind a button so it doesn't fire on every render).

### Auto-refresh

Toggle in the toolbar: off / 5s / 10s / 30s / 60s. Default 10s. On each tick, re-issues the current `/list` request and diff-renders the table. If the LRB hasn't advanced (same `lrbid`), skip the render — avoids flicker.

### Browser support

Latest Firefox / Chrome / Safari. No build step; vanilla JS + a single fetch wrapper. Match the existing dag_explorer.html style (it loads d3.v7 from a CDN but the chain explorer doesn't need d3 — keep it pure-JS).

## Implementation notes

**The spec is intentionally open.** Implement step-by-step, ship the smallest viable slice first, then pause for user feedback on direction — both spec and code. Don't try to land everything in one go.

Suggested first slice (Phase 1):

- `/list` endpoint with the bare-minimum filters: `max`, `kind`, `index_value`. Skip `delegation_target`/`delegation_master`/`active_within_slots`/`balance_*`/`sort_*` until requested.
- Minimal `chain_explorer.html` rendering a static table from one `/list` fetch. No detail panel, no auto-refresh, no cross-link click handlers.
- End-to-end validation against a standalone node seeded with one of each kind.

Subsequent slices (apply only when the user asks):

- richer filters (`delegation_target`, `delegation_master`, `active_within_slots`, balance bounds, sort)
- `/chain` detail endpoint + slide-in panel
- cross-link click handlers
- auto-refresh with LRB-pointer-based diff suppression
- type-specific extras (foundry token-holder count, sequencer delegations summary, frozen-status badges)
- optional `summary` block on `/list` (kind sums + on-sequencer / delegated / idle PRXI)

Each slice ships in a single commit behind the same `/api/v1/chain_explorer*` URL surface (the SPA degrades gracefully on missing fields).

## Testing

- Go side: per-handler unit tests at `api/chain_explorer/*_test.go`. Feed a synthetic LRB built from `utxodb` fixtures with one of each kind, exercise every filter, assert the `matched`/`returned` counts.
- Browser side: not automated for v1. Manual testing against the standalone node (`proxi config node --standalone`) populated with `proxi node foundry create` / `proxi node dlg amount` / `proxi node mkchain` to seed each kind.

## Out of scope (deferred)

- Historical view (`?slot=N`).
- Time-series of a single chain's balance / transitions.
- Bulk export (CSV).
- Authentication. The endpoint is read-only and shares the existing API server's auth posture (none).
- Per-row past-cone preview — link out to the DAG explorer instead.

## Open questions

- Foundry token-holder iteration cost. Worth measuring once the first slice is in.
- Whether `/list` should include a small `summary` object (sums by kind, on-sequencer vs delegated vs idle PRXI) so the toolbar can show the same "supply breakdown" `proxi node allchains` prints. Adds one trie pass per request; cheap. Likely yes, in a later slice.
