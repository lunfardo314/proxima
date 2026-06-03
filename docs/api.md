# Node API (`/api/v1`)

This is the node's main HTTP API. Two companion references cover the rest of the surface:

- [`txapi.md`](txapi.md) — the `/txapi/v1` transaction-building and parsing helpers.
- Browser HTML tools (`/dashboard`, `/peers`, `/dagviz`, `/dag_explorer`, `/chain_explorer`)
  are described in [`run_access.md`](run_access.md); their JSON backends are listed under
  [Explorer backends](#explorer-backends) below.

## Conventions

- Endpoints are HTTP `GET` unless marked **POST** (`eval`, `submit_tx`, `txlog/enable`).
- Responses are JSON. Most carry an `error` field — empty/absent on success, set on failure;
  failures are returned with HTTP 200 unless noted. A few endpoints return raw bytes or plain
  text (noted per endpoint).
- Transaction IDs, output IDs, chain IDs and other identifiers are **hex-encoded**.
- **LRB** = latest reliable branch (the consensus ledger state).
- Example base URL in the snippets: `http://localhost:8000`.

## Common response shapes

These appear in several endpoints:

- **`OutputDataWithID`** — `{ "id": "<hex output ID>", "data": "<hex raw output bytes>" }`.
- **`BranchDataJSONAble`** — branch metadata, with these fields: `root` (object
  `{ root, sequencer_id }`), `stem_output_index`, `sequencer_output_index`, `on_chain_amount`,
  `branch_inflation`, `supply`, `total_coverage`, `coverage_delta`, `frozen_coverage`,
  `slot_inflation`, `num_confirmed_transactions`, `num_seq_transactions`, `num_seq`,
  `baseline_root`.

Sections:

- [Ledger](#ledger)
- [State queries](#state-queries)
- [Transactions](#transactions)
- [Branches and chain state](#branches-and-chain-state)
- [Node status](#node-status)
- [Snapshots](#snapshots)
- [Explorer backends](#explorer-backends)
- [Transaction log](#transaction-log)
- [WebSocket](#websocket)

---

## Ledger

### get_ledger_definition

The compiled ledger library for a slot (the rules in force at that slot).

`/api/v1/get_ledger_definition?slot=<n>` — `slot` optional, default latest.

Response: `error`, `upgrade_slot`, `library_json` (full compiled library as JSON text),
`library_hash` (hex), `prev_library_hash` (hex), `prev_upgrade_slot`.

### ledger_constants

The ledger constants a wallet needs for local transaction building (slot/tick math, pace,
epoch limits, token names, etc.).

`/api/v1/ledger_constants?slot=<n>` — `slot` optional, default latest.

Response fields include: `hash`, `description`, `genesis_controller_public_key`,
`genesis_time_unix`, `tick_duration_ns`, `ticks_per_slot`, `initial_supply`,
`base_token_name`, `base_token_name_ticker`, `smallest_amount_name`,
`smallest_amounts_per_base_token`, `slot_inflation_base`, `minimum_inflatable_amount_0`,
`transaction_pace`, `transaction_pace_sequencer`, `max_number_of_endorsements`,
`pre_branch_consolidation_ticks`, `safe_revocation_slots`, `delegation_epoch_slots`,
`max_frozen_epochs`, `delegation_epoch_slots_min`/`_max`,
`delegation_max_frozen_epochs_min`/`_max`, `tag_along_slots`, `tag_along_reclaim_slots`,
`attachment_cost_budget`, `tx_id_state_ttl_slots`,
`healthy_coverage_numerator`/`_denominator`.

### get_ledger_time

The node's current ledger time.

`/api/v1/get_ledger_time`

Response: `error`, `slot`, `tick`, `time` (hex, the 5-byte ledger-time wire form).

### eval

Evaluates one or more **closed** EasyFL formulas (no `$0`/`$1` arguments) server-side. Used
by wallets for values they cannot compute locally.

**POST** `/api/v1/eval` — JSON body:

```json
{ "slot": 0, "sources": ["<closed EasyFL formula>", "..."] }
```

`slot` optional (0/omitted = latest). Response: `error`, `results` — an array of
`{ "value": "<hex result bytes>", "error": "<per-formula error>" }`. Each formula is
evaluated independently; a single formula failing does not fail the batch.

---

## State queries

### get_outputs

The unified state-query endpoint (it replaced the older per-account query endpoints). It
returns outputs from the LRB state indexed by a value (a controller / holder / target hash),
with filtering and sorting.

`/api/v1/get_outputs?index_value=<hex>&...`

| Param | Required | Meaning |
|-------|----------|---------|
| `index_value` | **yes** | The indexed value to look up (1–255 bytes hex). With `spendable=true` it is treated as the wallet holder ID. |
| `max_outputs` | no | Max outputs to return (default 200). |
| `sort_by` | no | `timestamp` (default) or `amount`. |
| `sort_order` | no | `asc` (default) or `desc`. |
| `for_amount` | no | Return the shortest prefix whose summed amount ≥ this value (`none`/0 = no cut). |
| `lock_type` | no | Filter by lock kind and the role `index_value` plays in it: `all`, `sigLock` (default), `chainLock`, `tagAlongMaster`, `tagAlongTarget`, `delegateMaster`, `delegateTarget`. |
| `chained` | no | `true`/`false` — tri-state; omitted = both; `true` = only outputs with a chain constraint. |
| `spendable` | no | `true`/`false` (default false) — keep only outputs claimable by `index_value` under a single-signature unlock at `target_slot`. |
| `target_slot` | no | Slot used for the deadline check on `sendWithDeadline` outputs (0/omitted = current LRB slot). |

Response: `error`, `outputs` (array of [`OutputDataWithID`](#common-response-shapes)),
`available_amount` (sum over the filtered set, before `max_outputs` truncation),
`limit_exceeded` (server iteration cap hit), `lrbid` (hex).

### get_chain_output

The current output of a chain.

`/api/v1/get_chain_output?chainid=<hex>`

Response: [`OutputDataWithID`](#common-response-shapes) fields (`id`, `data`) plus `lrbid`
and `error`.

### get_output

A single output by ID.

`/api/v1/get_output?id=<hex output ID>`

Response: `error`, `output_data` (hex), `lrbid`. Not found → `error: "output not found"`.

### get_all_chains

All chain outputs in the LRB.

`/api/v1/get_all_chains`

Response: `error`, `chains` (map of chain-ID hex → [`OutputDataWithID`](#common-response-shapes)),
`lrbid`.

### get_sequencers

All sequencer chains in the LRB.

`/api/v1/get_sequencers`

Response: `error`, `lrbid`, `sequencers` (map of sequencer-ID hex → object with
[`OutputDataWithID`](#common-response-shapes) fields plus `num_delegations`).

### get_sequencer_target_info

Detailed info about a sequencer as a delegation target. Used by `proxi node delegate
target_info` / `estimate`.

`/api/v1/get_sequencer_target_info?chainid=<hex>` — errors if the chain is not a sequencer.

Response: `error`, `lrbid`, `sequencer_id`, `name`, `origin_slot`, `current_output_slot`,
`transition_counter`, `branch_counter`, `token_balance`, `storage_deposit`,
`frozen_coverage` (array), `cumulative_chain_inflation`, `cumulative_branch_bonus`,
`minimum_fee`, `profit_margin_promille`, `greedy`, `pace`, `ignore_freeze_bound`, `now_slot`,
`current_epoch`, `next_epoch_boundary_slot`, `max_frozen_epochs`, `epoch_duration_slots`,
`coverage_lower_bound`, `coverage_upper_bound`.

### get_inactive

Inactive (long-unspent) UTXOs, oldest first.

`/api/v1/get_inactive?slots_back=<n>` — `slots_back` optional (default 360 ≈ 1 hour). Capped
at 1000 results.

Response: `error`, `lrbid`, `since_slot`, `utxos` — array of
`{ "id": <hex>, "lock": <lock source>, "amount": <number>, "output_string": <string> }`.

---

## Transactions

### submit_tx

Submits a raw transaction.

**POST** `/api/v1/submit_tx` — JSON body (max 2 MiB):

```json
{
  "tx_bytes": "<hex raw transaction bytes>",
  "consumed_utxos": ["<hex>", "..."],
  "validate_only": false
}
```

`tx_bytes` required. `consumed_utxos` optional, ordered to match the transaction's inputs;
supplying them enables full-context validation before submission. `validate_only` validates
without enqueuing.

Response (always HTTP 200): `ok` (bool), `tx_id` (hex, on success), `stage` (`parse` / `full`
/ `submit`, on failure), `error`.

### check_txid_in_lrb

Checks whether a transaction is included in the LRB (or up to `max_depth` branches back).

`/api/v1/check_txid_in_lrb?txid=<hex>&max_depth=<n>` — `max_depth` optional (default 1).

Response: `error`, `txid`, `lrbid`, `found_at_depth` (the branch depth it was found at).

---

## Branches and chain state

### get_latest_reliable_branch

The current LRB.

`/api/v1/get_latest_reliable_branch`

Response: `error`, `branch_id` (hex), `branch_data` ([`BranchDataJSONAble`](#common-response-shapes)).

### get_mainchain

A run of branches back along the main chain.

`/api/v1/get_mainchain?max=<n>` — `max` optional (default 20).

Response: `error`, `branches` — array of `{ "id": <hex>, "data": `[`BranchDataJSONAble`](#common-response-shapes)` }`.

### get_branch_list

Branch IDs along the main chain, oldest first. Used by the sync module.

`/api/v1/get_branch_list?after_branch=<hex>&from_slot=<n>&max=<n>`

- `after_branch` (optional, hex txid): fork-safe mode — branches after it (errors if it is not
  on the main chain). Takes priority over `from_slot`.
- `from_slot` (optional): branches with slot greater than this.
- `max` (optional): default 100.

Response: `error`, `branches` (array of hex txids, oldest first), `lrb_slot`.

### last_known_milestones

The latest milestone seen per sequencer.

`/api/v1/last_known_milestones`

Response: `error`, `sequencers` — map of sequencer-ID hex → `{ latest_milestone_txid,
last_branch_txid, milestone_count, last_activity_unix_nano }`.

---

## Node status

### sync_info

`/api/v1/sync_info`

Response: `error`, `synced`, `current_slot`, `lrb_slot`, `ledger_coverage`, `per_sequencer`
(map of sequencer-ID → `{ synced, latest_healthy_slot, latest_committed_slot,
ledger_coverage }`).

### node_info

`/api/v1/node_info`

Response: `id` (peer ID), `version`, `commit_hash`, `commit_time`, `num_static_peers`,
`num_dynamic_alive`, `sequencers`, `memory_stress_level` (0–100), `pipeline_size`,
`is_syncing`, `is_snapshotting`.

### peers_info

`/api/v1/peers_info`

Response: `error`, `host_id`, `peers` — array of `{ id, multiAddresses, is_static, is_alive,
when_added, num_incoming_pull, num_incoming_tx, rtt_ms }`.

---

## Snapshots

The snapshot endpoints require `snapshot.enable_api: true` in the node config.

### get_snapshot_info

Metadata about the latest snapshot.

`/api/v1/get_snapshot_info`

Response: `error`, `slot`, `file_size`, `file_name`.

### get_snapshot_branch_id

The branch ID of the latest snapshot.

`/api/v1/get_snapshot_branch_id`

Response: `error`, `id` (hex txid).

### get_snapshot

Downloads the snapshot file. **The response body is the raw snapshot file**, not JSON
(served as an attachment). See [`snapshot_format.md`](../ledger/multistate/snapshot_format.md).

`/api/v1/get_snapshot`

---

## Explorer backends

These JSON/text endpoints back the browser tools (`/dag_explorer`, `/chain_explorer`). Their
shapes are display-oriented and may evolve with the UIs.

### dag_explorer/past_cone

`/api/v1/dag_explorer/past_cone?txid=<hex>&depth=<n>` — `depth` optional (default 6). Returns
a `{ vertices, edges, tip_id, diagnostic }` graph. Each vertex carries `id`, `short_id`,
`slot`, `tick`, `is_seq`, `is_branch`, `seq_chain_id`, `num_inputs`, `num_outputs`, and
display flags; each edge carries `from`, `to`, `type` (`input`/`endorsement`/`baseline`).
Errors use real HTTP status codes.

### dag_explorer/slot

`/api/v1/dag_explorer/slot?slot=<n>&slots_back=<n>` — `slots_back` optional (default 0). Same
graph shape as `past_cone`.

### dag_explorer/find_tx

`/api/v1/dag_explorer/find_tx?q=<query>` — `q` is a dashed short transaction ID
(`[s]slot-tick-hashPrefix`) or a hex byte-prefix. Returns an array of `{ id, short_id }`
(max 50).

### dag_explorer/tx_detail

`/api/v1/dag_explorer/tx_detail?txid=<hex>` — returns the parsed transaction as **plain text**
(the same rendering as `proxi db txstore get -p`). 404 if not found.

### chain_explorer/list

`/api/v1/chain_explorer/list?max=<n>&kind=<kind>&index_value=<hex>&controller=<hex>&delegation_target=<hex>`

`kind` is `all` (default) / `sequencer` / `foundry` / `delegation` / `generic`; the hex
filters match index-values entries. Returns LRB-level aggregates (`total_supply`,
`frozen_coverage`, `total_coverage`, `slot_inflation`, counts, etc.) and `rows`, one per
chain: `chain_id`, `output_id`, `kind`, `balance`, `frozen`, `origin_slot`,
`transition_counter`, `last_active_slot`, `index_values`, and an optional nested
`sequencer` / `foundry` / `delegation` block.

### chain_explorer/utxo

`/api/v1/chain_explorer/utxo?chain_id=<hex>` — the current UTXO of a chain (note: chain ID,
not output ID). Returns `chain_id`, `output_id`, `size_bytes`, `elements` (decoded tuple
lines), and optional `chain` / `seq_data` blocks.

---

## Transaction log

The per-transaction log (txlog). Enabling/disabling via the API requires it to be allowed in
the node config.

### txlog/enable

**POST** `/api/v1/txlog/enable?level=<level>` — `level` is `off` (default) / `branch` /
`sequencer` / `non_sequencer` / `all`. Response: `error`, `enabled`, `level`.

### txlog/get

`/api/v1/txlog/get?prefix=<hex>&max=<n>` — `prefix` required (short txid prefix), `max`
optional (default 100). Requires the log to be enabled. Response: `error`, `records` — array
of `{ txid, clock_timestamp (Unix ns), message }`.

### txlog/range

`/api/v1/txlog/range?from=<unix_ns>&to=<unix_ns>&max=<n>` — `from` required, `to` optional
(default now), `max` optional (default 100). Same response shape as `txlog/get`.

### txlog/status

`/api/v1/txlog/status` — Response: `error`, `enabled`, `level`.

---

## WebSocket

### dag_vertex_stream

Real-time stream of DAG vertices, used by the live MemDAG visualizer (`/dagviz`).

`/wsapi/v1/dag_vertex_stream` (WebSocket upgrade)

Server-push only; client messages are ignored. Same-origin only. Connection count and
lifetime are bounded by `api.streaming.max_connections` and
`api.streaming.connection_ttl_minutes`.

Two text-frame message shapes are streamed:

- **Vertex add** — a `VertexWithDependencies` object (the same shape returned by
  [`get_vertex_dep`](txapi.md#get_vertex_dep): `id`, `a`, `i`, `seqid`, `seqname`,
  `num_endorse`, `holder`, `cd`, `supply`, `seqidx`, `stemidx`, `in`, `endorse`,
  `explicit_baseline`).
- **Vertex delete** — `{ "id": "<hex>" }`, sent when a vertex is removed from the MemDAG.

---

## Browser tools (HTML)

The node also serves read-only browser dashboards as HTML pages — `/dashboard`, `/peers`,
`/dagviz`, `/dag_explorer`, `/chain_explorer`. They are described in
[`run_access.md`](run_access.md) (and the testnet ones in [`testnet.md`](testnet.md)).
