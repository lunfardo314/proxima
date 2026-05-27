# Chain explorer

## Context

In Proxima a _chained account_ ("chain") is a UTXO covenant whose `chain` constraint at output slot 3 preserves a stable `chainID` across transitions. It is a first-class citizen of the ledger:

- **sequencer chains** — `chain` + `sequencer(epochSlots, maxFrozenEpochs)` at slot 4 (+ milestone data at slot 5 on milestones).
- **foundry chains** — `chain` + `foundry(supply)` at slot 4 (+ optional `foundryPolicy` at slot 5). The chain ID *is* the native-token tag.
- **delegation chains** — `chain` + delegate lock (sigLock owned by master, with target sequencer ID and frozen-coverage state at the last position).
- **generic chains** — `chain` only, no role-typing constraint.

Today the project runs ~100 chains. Projected scale is thousands to hundreds of thousands. The only inspection tools are CLI: `proxi node allchains`, `proxi node balance`, `proxi node chain <id>`. There is no browser-based view, and the existing JSON API surface (`/api/v1/get_all_chains`, `/api/v1/get_chain_output`, `/api/v1/get_sequencers`, `/api/v1/get_sequencer_target_info`) was designed for spot lookups, not bulk filtered browsing.

This spec defines a browser-served chain explorer mounted into the node API server, modelled on the existing DAG explorer (`api/dag_explorer/`).

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
| `/api/v1/chain_explorer/controller` | GET | All chains controlled by a given holderID / chain ID (cross-link target) |

All four are read-only and operate on the LRB.

## `GET /api/v1/chain_explorer/list`

The primary endpoint. Single JSON response, no pagination.

### Query parameters

| Param | Type | Default | Notes |
|-------|------|---------|-------|
| `max` | int | 200 | Hard cap on rows returned. Server-enforced ceiling: 2000. |
| `kind` | enum | `all` | One of `all`, `sequencer`, `foundry`, `delegation`, `generic`. |
| `controller_id` | hex | — | 32-byte holderID OR chainID. If set, returns only chains whose controlling lock indexes to this value (sigLock holder, chainLock chainID, or delegation master). |
| `delegation_target` | hex | — | 32-byte sequencer chainID. Restricts to delegation chains targeting it. |
| `delegation_master` | hex | — | 32-byte holderID. Restricts to delegation chains mastered by it (alias for `controller_id` + `kind=delegation`, kept for clarity). |
| `active_within_slots` | uint32 | — | Excludes chains whose last-transit slot is older than `lrbSlot - N`. |
| `balance_min`, `balance_max` | uint64 | — | Inclusive bounds on the chain's on-chain PRXI balance. |
| `sort_by` | enum | `balance` | One of `balance`, `chain_id`, `last_active`, `transitions`. |
| `sort_order` | enum | `desc` | `asc` or `desc`. |

Unknown parameters are rejected with HTTP 400 so the UI fails loudly during development.

### Response

```json
{
  "lrbid": "000002590101d9ca...",
  "lrb_slot": 601,
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
      "controller_id": "9d2c6fedeb0f...",
      "controller_display": "chainLock(0x9d2c6fedeb0f...)",
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
      "controller_id": "fb03128a43df11...",
      "controller_display": "sigLock(0xfb03128a43df...)",
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
      "controller_id": "fb03128a43df...",
      "controller_display": "sigLock(0xfb03128a43df...)",
      "delegation": {
        "master_id": "fb03128a43df...",
        "target_id": "9d2c6fedeb0f...",
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

`controller_display` is the decompiled lock source (`sigLock(0x…)`, `chainLock(0x…)`, etc.), produced by `lib.DecompileBytecode` on the slot-2 bytecode. Stable per-lock and usable as a grouping key in the UI.

`kind` discriminator rules (mutually exclusive, checked in this order):
1. slot 4 holds a parseable `sequencer(…)` → `sequencer`
2. slot 4 holds a parseable `foundry(…)` → `foundry`
3. lock at slot 2 parses as a delegate lock → `delegation`
4. otherwise → `generic`

(Note: a sequencer chain that is also a delegation target is still `sequencer` — the kind classifies the *output role*, not its relationships.)

### Server-side implementation sketch

The handler iterates `rdr.IterateChainedOutputs` (already exists), classifies each output with the wallet library helpers (`ParseChainConstraint`, `ParseSequencerConstraint`, `ParseFoundryBytecode`, `ParseDelegationOutput`), applies the predicate from query params, and accumulates into `rows`. Sort + truncate at the end.

For the `controller_id` filter, prefer a trie-side lookup against `TriePartitionControllers` using the same path the existing `get_outputs?index_value=…` endpoint uses; fall back to the iteration filter if the indexed entry isn't a chain output.

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

Response includes `lrbid` and `lrb_slot` so the UI can confirm freshness.

## `GET /api/v1/chain_explorer/controller`

Returns all chains controlled by a given identity. Used as the cross-link target when the user clicks a `controller_id` cell.

### Query parameters

| Param | Type | Notes |
|-------|------|-------|
| `controller_id` | hex | Required. 32-byte holderID (sigLock) OR chainID (chainLock / delegation master). |
| `max` | int | Default 200, ceiling 2000. |

### Response

```json
{
  "lrbid": "...",
  "controller_id": "fb03128a43df...",
  "controller_display": "sigLock(0x…)",
  "total_balance": 521000000000,
  "matched": 23,
  "returned": 23,
  "rows": [ /* same row shape as /list */ ]
}
```

Implemented as a thin wrapper over `/list?controller_id=…`.

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

- click `controller_id` cell → re-runs the table with `controller_id` filter pre-filled
- click `delegation.target_id` → re-runs with `kind=sequencer` + filter for that ID
- in the detail panel, sequencer chains show a "Delegations (N) →" button that re-runs with `kind=delegation` + `delegation_target=…`
- foundry chains show "Token holders (N) →" which… does a `get_outputs?index_value=<holderID||tag>` traversal (out of scope for v1 if too expensive, behind a button so it doesn't fire on every render)

### Auto-refresh

Toggle in the toolbar: off / 5s / 10s / 30s / 60s. Default 10s. On each tick, re-issues the current `/list` request and diff-renders the table. If the LRB hasn't advanced (same `lrbid`), skip the render — avoids flicker.

### Browser support

Latest Firefox / Chrome / Safari. No build step; vanilla JS + a single fetch wrapper. Match the existing dag_explorer.html style (it loads d3.v7 from a CDN but the chain explorer doesn't need d3 — keep it pure-JS).

## Implementation phases

1. **Phase 1 — list endpoint + minimal HTML.** `/list` with all filters and all four kinds, plus a basic table HTML rendering it (no auto-refresh, no detail panel, no cross-links). Validate end-to-end against the running testnet.
2. **Phase 2 — detail endpoint + slide-in panel.** Adds `/chain` and the per-row drill-down. Cross-links wired up.
3. **Phase 3 — controller endpoint + auto-refresh.** Adds `/controller`, the controller pivot, and the auto-refresh toggle.
4. **Phase 4 — type-specific extras.** Foundry token-holder count, sequencer delegations summary, frozen-status visual badges in the table.

Each phase ships in a single commit, behind the same `/api/v1/chain_explorer*` URL surface (the SPA degrades gracefully on missing fields).

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

- Whether the `controller_display` should also resolve nested lock kinds (e.g. delegate lock printed as `delegate(master=…, target=…)` rather than the raw bytecode). Currently the proposal is "decompile to EasyFL source"; the alternative is "structured object so the UI renders it". Default to the EasyFL source for v1 and revisit if it's unreadable in practice.
- Foundry token-holder iteration cost. Worth measuring once Phase 1 ships.
- Whether `/list` should include a small `summary` object (sums by kind, on-sequencer vs delegated vs idle PRXI) so the toolbar can show the same "supply breakdown" `proxi node allchains` prints. Adds one trie pass per request; cheap. Likely yes, in Phase 2.
