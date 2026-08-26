# Size and count limits

Limits are enforced at four different layers, and it matters which one rejects
what: a transaction refused by the network never reaches validation, while one
refused at parse never reaches the constraint engine.

## Network and API

| Limit | Value | Where |
|-------|-------|-------|
| P2P message payload | 65,531 bytes (`MaxUint16 − 4`) | `peering/misc.go` — `MaxPayloadSize` |
| API upload | 2 MiB | `api/server/server.go` — `maxTxUploadSize`, on `/api/v1/submit_tx` |

These bound what can arrive, not what is valid. A transaction larger than the
P2P payload cap simply cannot be gossiped. The API cap is much larger than any
valid transaction because the request body also carries `consumed_utxos`; the
transaction itself is still bounded by `MaxTransactionSize` below.

## Parse (stage 1)

Structural checks, applied before any tuple content is interpreted. All in
`ledger/transaction/parse.go`.

| Limit | Value | Constant |
|-------|-------|----------|
| Total transaction size | 65,536 bytes | `MaxTransactionSize` — first check in `Parse()`, before parsing anything |
| Individual produced output | 8,192 bytes | `MaxOutputSize` — in `scanProducedOutputs` |
| Unlock params per input | 1,024 bytes | `MaxUnlockParamsSize` — in `scanInputs` |
| Top-level tuple elements | exactly `TxTreeTupleNumElements` | not a range: the wrong count is not a transaction |
| Produced outputs | 1–256 | |

`MaxTransactionSize` is 64 KB, matching the P2P payload cap closely enough that
a transaction which parses is one that could also have been gossiped.

## Validation (stage 2 and 3)

| Limit | Value | Where |
|-------|-------|-------|
| Endorsements | max 8 | `constMaxNumberOfEndorsements`, checked in `tx_integrity_validator.easyfl` |
| Duplicate inputs | rejected | EasyFL, `tupleHasDuplicatesAtPath` |
| Attachment cost budget | 550 | `constAttachmentCostBudget` |

The attachment cost budget is not a size limit but a work limit: it bounds what
one transaction may make a node do, and is deliberately set above the cost of a
maximal transaction with 256 inputs and 256 outputs.

## Underlying tuple format

`tuples` imposes its own ceilings, far above anything the ledger allows: 16,383
elements per tuple, and 4,294,967,295 bytes per element. They never bind in
practice — the parse limits above cut in first — but they are the reason an
unbounded output size was once possible.

## Why the parse-level limits exist

Before them, the network capped a message at roughly 64 KB but nothing in
`Parse()` did, and the tuple format would accept an individual output of up to
4 GB. The caps close that gap at the earliest point where it can be closed
cheaply, so that a malformed or hostile transaction is discarded before any
constraint is evaluated.

Tests are in `ledger/tests/limits_test.go`, including boundary cases that assert
a transaction at exactly the maximum is *not* rejected for size.
