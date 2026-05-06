# Unified state-query endpoint: `get_outputs`

## Goal

Collapse the historical mess of state-query / UTXO-retrieval APIs
(`get_account_outputs`, `get_account_parsed_outputs`,
`get_utxo_controlled_by`, `get_account_simple_siglocked`,
`get_outputs_for_amount`, `get_nonchain_balance`, `get_chained_outputs`,
`get_delegation_outputs`, …) into a single endpoint. Rewrite both the
server side and the caller side (proxi commands, API client) around it.

The existing `Controller`-based shape is the wrong abstraction (see
`feedback_index_api_shape.md`): the trie indexes raw byte values; lock
kind is not part of the trie key. The caller is the right place to
interpret the value (holder vs. chain ID vs. target …) and to filter
by lock kind / role.

## Endpoint shape

`GET /api/v1/get_outputs`

### Response

```go
type GetOutputsResponse struct {
    Error           string             `json:"error,omitempty"`
    Outputs         []OutputDataWithID `json:"outputs,omitempty"`
    AvailableAmount uint64             `json:"available_amount,omitempty"`
    LimitExceeded   bool               `json:"limit_exceeded,omitempty"`
    LRBID           string             `json:"lrbid,omitempty"`
}
```

`LimitExceeded` is set when the server-side iteration cap (see "Server-side
cost" below) was hit before the lookup completed. When true, `Outputs`
and `AvailableAmount` reflect a partial view, not the full filtered
set; the caller should treat the result as incomplete and act on it
accordingly (e.g. prompt the user to compact UTXOs and retry, or
treat `AvailableAmount` as a lower bound).

`OutputDataWithID` on the wire is `{OutputID, Data []byte}` (raw); the
Go client parses it into `OutputWithID` (see "Wire format vs Go
client" below).

`AvailableAmount` is the **sum of all outputs matching the lookup +
filters** (before `for_amount` and `max_outputs` truncation). It
always reflects the true balance under the query, so the caller can:

- Compare against the requested `for_amount` to detect a shortfall
  without a separate call. There is no error in the shortfall case;
  `Outputs` is still populated with what was available, and
  `AvailableAmount < for_amount` is the signal.
- Render "have N (returned k of m)" UIs without a second roundtrip.

The server reads from the **current LRB** (latest reliable branch);
`LRBID` is reported back so callers can correlate / detect drift.
Internally the trie iteration is non-deterministic; the server applies
the requested sort **before** truncating / amount-summing.

### Parameters

| Name | Required | Default | Values | Notes |
|------|----------|---------|--------|-------|
| `index_value` | yes | — | hex, 1–255 bytes | Raw value looked up under TriePartitionControllers. Length is encoded as 1 byte in the trie key, so the upper bound is 255. The stem output is requestable with `index_value=00` (its 1-byte placeholder). |
| `max_outputs` | no | `200` | uint | Caps returned count. Applied **after** sort + filters + for_amount. |
| `sort_by` | no | `timestamp` | `timestamp` \| `amount` | Pre-sort key. |
| `sort_order` | no | `asc` | `asc` \| `desc` | Sort direction. |
| `for_amount` | no | `none` | uint \| `none` | If set, server returns the smallest prefix (after sort) whose amount sum ≥ `for_amount`. If the full filtered set sums to less than `for_amount`, no error: `Outputs` carries everything available and `AvailableAmount < for_amount` signals the shortfall to the caller. |
| `lock_type` | no | `sigLock` | see below | Filter by lock kind / role of the `index_value` in the UTXO. |
| `chained` | no | `false` | `true` \| `false` | If `false`, exclude chained outputs (those carrying a chain constraint at index 3). If `true`, **only** chained outputs. |

### `lock_type` values

The trie-indexed value plays different roles in different lock kinds.
The filter says both *which lock kind* and *which role* the
`index_value` plays inside that lock:

| Value | Matches | `index_value` role |
|-------|---------|--------------------|
| `all` | any UTXO whose IndexValues contains `index_value` | irrelevant |
| `sigLock` (default) | sig-locked UTXOs | holder ID (position 0) |
| `chainLock` | chain-locked UTXOs | chain ID (position 0) |
| `tagAlongMaster` | tag-along UTXOs | master/sender at position 0 |
| `tagAlongTarget` | tag-along UTXOs | target sequencer chainID at position 1 |
| `delegateMaster` | delegate UTXOs | master at position 0 |
| `delegateTarget` | delegate UTXOs | target chain at position 1 |

Stem outputs aren't normally user-queryable, but the trie does index
their 1-byte placeholder (`0x00`); `lock_type=all` + `index_value=00`
is the way to surface them.

### Order of operations on the server

1. Trie lookup under `TriePartitionControllers` for `index_value`
   (any length 1..255). Iterate up to `IterationCap` hits (see
   "Server-side cost"); if the cap is hit before iteration is
   complete, set `LimitExceeded=true` and stop.
2. Hydrate to outputs.
3. Filter by `lock_type` (kind + role of `index_value`).
4. Filter by `chained`.
5. Sort by `sort_by` / `sort_order`.
6. Compute `AvailableAmount` = sum of all amounts in the (possibly
   capped) filtered set, before any truncation.
7. If `for_amount` is set: take the smallest prefix (after sort) whose
   sum ≥ `for_amount`. If unreachable (`AvailableAmount < for_amount`),
   keep the full filtered set — no error; the caller detects the
   shortfall from `AvailableAmount`.
8. Truncate to `max_outputs`.

(Steps 5 → 6 → 7 are deliberate: `for_amount` and `max_outputs` operate
on the sorted list. The user's spec: "Outputs are always pre-sorted
before applying max_outputs and for_amount, if applicable.")

## Out of scope (this endpoint)

- Lock-construction syntax (the wallet-side `--target` / "send to
  this kind of address" mini-syntax `a/<hex>` `c/<hex>` `t/<hex>`
  `d/<hex>`). That's a separate piece of work — the wallet builds
  `Lock` values for *outgoing* outputs; this endpoint is for
  *incoming* state queries.
- Chained-output specific shapes (delegations, sequencers, etc.).
  Use `chained=true` + `lock_type=chainLock|delegateMaster|...` and
  let the caller post-process if it wants more.

## Migration / removal list (server)

To be removed once `get_outputs` covers their use cases. Fold each
caller through `get_outputs` first, then delete:

- `PathGetUTXOsControlledBy`
- `PathGetAccountOutputs` / `PathGetAccountParsedOutputs`
- `PathGetAccountSimpleSiglockedOutputs`
- `PathGetOutputsForAmount`
- `PathGetNonChainBalance`
- `PathGetChainedOutputs`
- `PathGetDelegationOutputs`

Possibly survives (different shape):
- `PathGetChainOutput` (single-chain lookup by full chain ID — not a
  controller-style index query).

## Migration / removal list (callers)

- `proxi/node_cmd/balance.go` → `get_outputs lock_type=sigLock`
  + sum locally.
- `proxi/node_cmd/utxos.go` → `get_outputs lock_type=…` (driven by
  user-supplied filter).
- `proxi/node_cmd/transfer.go` → `get_outputs sort_by=amount
  sort_order=asc for_amount=<needed>` (or `desc` — TBD which packs
  fewer inputs). The current "outputs for amount" path becomes one
  call.
- `proxi/node_cmd/setup_seq.go waitForFunds` → `get_outputs
  for_amount=<needed>`.
- `proxi/node_cmd/seq_cmd/withdraw.go` — uses target lock for
  destination (output construction, not a query) — unaffected.
- `proxi/node_cmd/fund.go` — uses target lock for output
  construction — unaffected.
- `proxi/node_cmd/faucet_srv.go` — same.

## Wire format vs Go client

- **JSON wire format**: raw — `Outputs[]` carries
  `{OutputID, Data []byte}`. The API contract is library-independent.
- **Base Go client function**: parses on the way out, returns
  `[]OutputWithID` (parsed `Output` value with all fields decoded).
  Requires the ledger library to be initialised in the client process
  (proxi already does this via `InitLedgerFromNode()`). Any higher-
  level helpers stack on top of the base function as needed.

## Server-side cost

Steps 3–7 (sort + AvailableAmount + for_amount + truncate) run over
the filtered set collected in step 1. To bound work on a popular
`index_value`, step 1 enforces a hard iteration cap:

- **`IterationCap = 2000`** outputs. If the trie iteration would
  return more, stop after the cap and set `LimitExceeded=true` on the
  response. Subsequent steps (filter / sort / sum / truncate) operate
  on the partial set as collected.
- When `LimitExceeded=true`, `Outputs` and `AvailableAmount` reflect a
  partial view. Callers must treat them as incomplete (e.g. prompt
  the user to compact UTXOs).

This is the only built-in protection. There is no pagination /
cursor — LRB drifts between calls, so multi-call iteration can't be
made deterministic anyway. The intended remedy for "too many
outputs" is to compact, not paginate.

## Pagination — explicitly not supported

`max_outputs` truncates without a cursor. v1 is single-shot only.
Reasons:

- LRB advances between calls, so a `(cursor, page_size)` pair would
  not give a consistent snapshot.
- The intended fix for a query that hits the iteration cap is to
  compact UTXOs, not to walk through them with a cursor.

If a future use case genuinely needs cursors over a stable snapshot,
that's a separate endpoint with its own response shape.

## Phasing (proposed)

1. **Implement `get_outputs` server-side** with all params; add
   `GetOutputsResponse` type + JSON; add API client method. Don't
   remove old endpoints yet.
2. **Migrate proxi callers** (balance, utxos, transfer, setup_seq,
   waitForFunds) to `get_outputs`. Verify on testnet.
3. **Delete legacy endpoints** + their server handlers + client
   methods + tests. Keep `get_chain_output` if still needed.
4. **Update API docs / CLI flag descriptions** to reflect the new
   single-endpoint surface.

Each phase a separate commit. Keep the old endpoints alive until
phase 3 so the testnet doesn't break mid-migration.
