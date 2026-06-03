# TX API (`/txapi/v1`)

The TX API exposes transaction-building and parsing helpers. It is used mainly by wallets
and by frontends such as explorers and DAG visualizers.

- All endpoints are HTTP `GET` and return JSON.
- EasyFL scripts and bytecode are compiled / decompiled in the context of the node's
  **latest** ledger library.
- Transaction IDs and output IDs are passed **hex-encoded**.
- On error, the response is `{"error": "<message>"}`.

Example base URL in the snippets below: `http://localhost:8000`.

Endpoints:

* [compile_script](#compile_script)
* [decompile_bytecode](#decompile_bytecode)
* [parse_output_data](#parse_output_data)
* [parse_output](#parse_output)
* [get_txbytes](#get_txbytes)
* [get_parsed_transaction](#get_parsed_transaction)
* [get_vertex_dep](#get_vertex_dep)

## compile_script

Compiles an EasyFL script and returns its bytecode.

`/txapi/v1/compile_script?source=<EasyFL script source>`

```bash
curl -L -X GET 'http://localhost:8000/txapi/v1/compile_script?source=slice(0x0102,0,0)'
```

Response: `{ "bytecode": "<hex-encoded bytecode>" }`

## decompile_bytecode

Decompiles bytecode back to an EasyFL script.

`/txapi/v1/decompile_bytecode?bytecode=<hex-encoded bytecode>`

```bash
curl -L -X GET 'http://localhost:8000/txapi/v1/decompile_bytecode?bytecode=1182010281008100'
```

Response: `{ "source": "<EasyFL script source>" }`

## parse_output_data

Parses raw output bytes as a tuple and decompiles each constraint script. Unlike
[parse_output](#parse_output), it works on supplied bytes and does not need to look anything
up in the ledger state.

`/txapi/v1/parse_output_data?output_data=<hex-encoded output bytes>[&human_readable]`

Add `human_readable` to get the constraints in human-readable form instead of EasyFL source.

Response (`ParsedOutput`):

| Field | Type | Description |
|-------|------|-------------|
| `data` | hex string | the raw output bytes |
| `constraints` | string array | decompiled constraint scripts (EasyFL source, or human-readable if requested) |
| `amount` | number | token amount on the output |
| `lock_name` | string | name of the lock constraint |
| `chain_id` | hex string | present only for chain outputs |

## parse_output

Like [parse_output_data](#parse_output_data), but takes an output ID and reads the raw
output from the **latest reliable branch (LRB)** state.

`/txapi/v1/parse_output?output_id=<hex-encoded output ID>`

Response: the same `ParsedOutput` shape as [parse_output_data](#parse_output_data).

## get_txbytes

Returns the canonical raw bytes of a transaction.

`/txapi/v1/get_txbytes?txid=<hex-encoded transaction ID>`

Response: `{ "tx_bytes": "<hex-encoded canonical transaction bytes>" }`

## get_parsed_transaction

Returns a transaction in JSON form. This form contains all elements of the transaction
except the signature payload of inputs; it is **not** the canonical form and cannot be used
to reconstruct the binary transaction. Its purpose is display in frontends.

`/txapi/v1/get_parsed_transaction?txid=<hex-encoded transaction ID>`

Response (`TransactionJSONAble`):

| Field | Type | Description |
|-------|------|-------------|
| `id` | hex string | transaction ID |
| `total_amount` | number | total amount produced on the transaction |
| `total_inflation` | number | total inflation on the transaction |
| `is_branch` | bool | whether this is a branch transaction |
| `sequencer_tx_data` | object | present only for sequencer transactions (see below) |
| `signature` | hex string | transaction signature |
| `inputs` | array | each `{ "output_id": <hex>, "unlock_data": <hex> }` |
| `outputs` | array | `ParsedOutput` objects (see [parse_output_data](#parse_output_data)) |
| `endorsements` | hex string array | endorsed transaction IDs; omitted if none |

`sequencer_tx_data`:

| Field | Type | Description |
|-------|------|-------------|
| `sequencer_id` | hex string | sequencer chain ID |
| `sequencer_output_index` | number | index of the sequencer output |
| `stem_output_index` | number | index of the stem output; omitted for non-branch transactions |
| `milestone_data` | object | `{ "name", "minimum_fee", "transition_counter", "branch_counter" }`; omitted if absent |

## get_vertex_dep

Returns a compact form of the transaction's DAG vertex — its dependencies and a few display
attributes. Its primary use is DAG visualizers, so the JSON keys are deliberately short. It
is the same shape streamed by the [dag_vertex_stream](api.md#dag_vertex_stream) WebSocket.

`/txapi/v1/get_vertex_dep?txid=<hex-encoded transaction ID>`

Response (`VertexWithDependencies`):

| JSON key | Type | Description |
|----------|------|-------------|
| `id` | hex string | transaction ID |
| `a` | number | total amount produced |
| `i` | number | total inflation; omitted if 0 |
| `seqid` | hex string | sequencer chain ID; omitted for non-sequencer transactions |
| `seqname` | string | sequencer name from on-chain data; omitted if none |
| `num_endorse` | number | number of endorsements; omitted if 0 |
| `holder` | hex string | holder ID; set for non-sequencer transactions (vertical placement) |
| `cd` | number | coverage delta; sequencer transactions only |
| `supply` | number | total supply; sequencer transactions only |
| `seqidx` | number | input index of the sequencer predecessor |
| `stemidx` | number | input index of the stem predecessor |
| `in` | hex string array | input transaction IDs |
| `endorse` | hex string array | endorsed transaction IDs; may be omitted |
| `explicit_baseline` | hex string | explicit baseline transaction ID, if present |
