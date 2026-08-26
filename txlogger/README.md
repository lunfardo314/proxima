# Transaction logger (txlogger)

Tracks events attributed to individual transactions, so that after the fact you
can ask "what happened to this transaction, and when". Node components record
messages against a transaction ID; the log can be searched by transaction ID
prefix or by time range.

Records live in their own Badger store with a TTL, and the whole logger can be
enabled and disabled at runtime — disabled means the database is closed.

Because logs are events in the order the node processed them, they are **not**
evidence of DAG topology. To reason about inputs, endorsements and chain
relationships, read the raw transaction (`proxi db txstore get`, or the dagviz
APIs). Inferring successor relationships from log order has produced wrong
analyses before.

## Configuration

```yaml
txlogger:
  enable_on_start: false       # auto-enable on node start (default false)
  level: "all"                 # off, branch, sequencer, non_sequencer, all
  ttl_hours: 1                 # record TTL in hours (default 1)
  enable_on_off_api: false     # allow enabling/disabling over the API (default false)
```

Levels select which transactions are logged: `branch` only branches,
`sequencer` all sequencer transactions, `non_sequencer` only non-sequencer
ones, `all` everything, `off` nothing.

## Storage

Two partitions give two indexes over the same records (`||` is concatenation):

| Partition | Key | Purpose |
|-----------|-----|---------|
| `0x01` | `0x01 \|\| TransactionIDShort` (27 B) `\|\| timestamp_ns` (8 B) → log message | Prefix lookup by transaction, including a partial prefix |
| `0x02` | `0x02 \|\| timestamp_ns` (8 B, big-endian) `\|\| TransactionIDShort` (27 B) → marker | Time-range iteration |

`TransactionIDShort` is 27 bytes: 1 byte of max output index + 26 bytes of
transaction hash. Each message of a given transaction carries a unique
nanosecond timestamp; a collision (very rare) loses the earlier message.

Two background loops keep the store bounded, both stopping when the logger is
disabled: a cleanup pass deletes records past the TTL — implemented manually
over the time-indexed keys rather than with Badger's native TTL, so the store
stays abstracted behind `global.Store` — and a periodic `RunValueLogGC`.

## API

| Endpoint | Does |
|----------|------|
| `POST /api/v1/txlog/enable?level=<level>` | Enable at a level, or `off` to disable |
| `GET /api/v1/txlog/get?prefix=<hex>&max=<n>` | Records for transactions matching a short-ID prefix |
| `GET /api/v1/txlog/range?from=<unix_ns>&to=<unix_ns>&max=<n>` | Records in a time range |

```bash
curl -X POST "http://localhost:8080/api/v1/txlog/enable?level=all"
curl "http://localhost:8080/api/v1/txlog/get?prefix=aabbcc&max=50"
curl "http://localhost:8080/api/v1/txlog/range?from=1706000000000000000&max=100"
```

## CLI

```bash
proxi node txlog enable              # default level: non_sequencer
proxi node txlog enable --level all
proxi node txlog disable
proxi node txlog get aabbcc          # by transaction ID short prefix
proxi node txlog tail                # last minute
proxi node txlog tail --back 5       # last 5 minutes
```

## From node components

```go
node.LogTx(time.Now(), "transaction received", txid)
node.LogTx(time.Now(), "committed to ledger", txid1, txid2, txid3)
```

---

The original specification — requirements, phase plan and the as-built file
list — is archived at
[`claude/archive/shipped/txlogger.md`](../claude/archive/shipped/txlogger.md).
