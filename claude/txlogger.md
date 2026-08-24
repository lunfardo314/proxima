# Transaction logger (txlogger)

> **QUEUED → `txlogger/`** — The transaction logger: per-transaction event tracking.
> Rewritten there, then archived. See `claude/kb_reorg.md`.

## Purpose
Transaction logger is a core service in the Proxima node that allows tracking events related to individual transactions.
It allows other components of the node:
- to record log messages about specific transactions with transaction ID and clock timestamp
- to retrieve records attributed to a specific transaction based on its ID or a prefix of its hash part (TransactionIDShort) (prefix matching)
- to retrieve events in a specified period of time

The log records are stored in a key-value store. The store is cleaned automatically according to a specified TTL.

The `txlogger` can be globally enabled and disabled dynamically.
When logger is disabled, any write to it is a no-op and any read from it returns an error.

When enabling logger, user can specify which types of transactions should be logged, while others are ignored.

The `TransactionIDShort` is 27-byte long suffix of the full transaction ID. It is based on the 26 bytes of the transaction hash (except byte 0 that contains number of produced outputs). `TransactionIDShort` uniquely identifies transaction because collisions are practically impossible. 

## Requirements

### Core Requirements
* The implementation should be based on interfaces and types `TxLog*` defined in `global/types.go`
* The underlying key-value store must be abstracted via the `global.Store` interface
* The implementation must be based on Badger just like other key-value stores in the node
* Disabled `txlogging` means closing the database
* Enabled `txlogging` means opening database or creating a new one if it does not exist
* Each message of the same transaction has unique clock timestamp in nanoseconds. Collisions of timestamps (very rare) just means earlier message with the same clock timestamp lost
* we should be able to search transaction by prefix of its `TransactionIDShort`. That naturally includes ability to search by full transaction ID.

### Configuration (in `proxima.yaml`)
```yaml
txlogger:
  enable_on_start: false       # auto-enable on node start, default is false
  level: "all"                 # off, branch, sequencer, non_sequencer, all
  ttl_hours: 1                 # record TTL in hours, default is 1
  enable_on_off_api: false     # allow enabling/disabling via API, default is false
```

### Key Structure
The key-value store uses two partition prefixes and two types of keys for dual indexing (`||` denotes concatenation):
* **By transaction** (partition prefix `0x01`):
   * key = `0x01` || `TransactionIDShort` (27 bytes) || `timestamp_nanosec` (8 bytes) = 36 bytes total
   * value = log message
* **By timestamp** (partition prefix `0x02`):
   * key = `0x02` || `timestamp_nanosec` (8 bytes big-endian) || `TransactionIDShort` (27 bytes) = 36 bytes total
   * value = any value, for example `[]byte{0xff}`

Where:
- `TransactionIDShort` is 27 bytes: 1 byte max output index + 26 bytes TransactionHash
- `timestamp_nanosec` is 8 bytes: Unix nanoseconds in big-endian format

This dual-key structure enables efficient:
- prefix-based lookup by `TransactionIDShort` (or partial prefix)
- lookup by full transaction ID
- time-range iteration for retrieving records within a time period

### TTL Strategy
Implement manual cleanup using `RepeatInBackground()` (do NOT use Badger's native TTL):
- Store record timestamp as part of the key (already in dual-key structure)
- Run periodic cleanup task via `RepeatInBackground()` (e.g., every 10 minutes) that deletes expired records
- Use time-indexed keys to efficiently find and delete old records
- Task returns `false` when txlogger is disabled to stop the loop
- This approach provides proper abstraction for future RocksDB migration

### Database Abstraction
The implementation must be properly abstracted to support future migration from Badger to RocksDB:
- Use `global.Store` interface for all KV operations
- No Badger-specific features (like native TTL) in core logic
- Badger-specific code (GC, options) isolated in initialization

### Badger GC
When using Badger, run periodic garbage collection via `RepeatInBackground()` (similar to `multiStateDB`):
- Call `RunValueLogGC(0.5)` periodically (e.g., every 5 minutes)
- Log GC duration and results
- Task returns `false` when txlogger is disabled to stop the loop

### Unit Tests
Minimal set of unit test tha cover key functionality
Comprehensive unit tests covering:
- Write and read operations
- Time-range queries
- TTL expiration
- Enable/disable lifecycle
- Log level filtering

### Integration
* Other components access txLogging via `TxLogWriter` interface
* Logging must be queued using patterns from `core_modules` (queue-based async processing)
* The txlogger service is initialized in the node package (similar to txstore)

### Package Structure
* `txlogger/` - store implementation (Badger DB wrapper) and reader functionality
* `core/core_modules/txlogger/` - queued async writer module

### API Endpoints
Define path constants in `api.go`
* `POST /api/v1/txlog/enable?level=<level>` - enable txlogger with specified level
   * `off` - disable txlogger
   * `branch` - log branch transactions only
   * `sequencer` - log all sequencer transactions
   * `non_sequencer` - log non-sequencer transactions only
   * `all` - log all transactions
* `GET /api/v1/txlog/get?prefix=<hex_prefix>&max=<max>` - get records by transaction ID short prefix
* `GET /api/v1/txlog/range?from=<unix_ns>&to=<unix_ns>&max=<max>` - get records in time range

### CLI Commands (proxi)
Implement in `proxi/node_cmd/`:

* `proxi node txlog get <prefix>` - list log messages for transactions matching the short ID prefix, sorted ascending by log timestamp
* `proxi node txlog disable` - disable transaction logging (calls enable with level=off)
* `proxi node txlog enable [--level <level>]` - enable transaction logging
   * `--level`: off, branch, sequencer, non_sequencer, all
   * Default level: `non_sequencer`
* `proxi node txlog tail [--back <minutes>]` - list log of all transactions from recent time
   * `--back`: number of minutes to look back (default: 1)

---

## Implementation Plan

### Phase 1: Foundation - Constants and Store Package

**1.1 Add constants to `global/constants.go`:**
- Add `TxLogDBName = "proximadb.txlog"`
- Add partition prefix constants (can be in txlogger package)

**1.2 Create `txlogger/` package with core store:**

Files to create:
- `txlogger/store.go` - main store implementation
- `txlogger/keys.go` - key encoding/decoding helpers
- `txlogger/store_test.go` - unit tests

`store.go` should implement:
```go
type TxLogStore struct {
    db       *badger_adaptor.DB
    mu       sync.RWMutex
    level    global.TxLogLevel
    enabled  bool
}

func New(dbPath string) (*TxLogStore, error)
func (s *TxLogStore) Close() error
func (s *TxLogStore) IsEnabled() bool
func (s *TxLogStore) Level() global.TxLogLevel
func (s *TxLogStore) SetLevel(lvl global.TxLogLevel)

// Writer method (called by queue consumer)
func (s *TxLogStore) WriteRecord(clockTs time.Time, msg string, txids ...base.TransactionID) error

// Reader methods (implements global.TxLogReader)
func (s *TxLogStore) TxLogGet(txShortIDPrefix []byte, max ...int) ([]global.TxLogRecord, error)
func (s *TxLogStore) TxLogIterate(begin time.Time, fun func(rec global.TxLogRecord)) error

// Cleanup methods
func (s *TxLogStore) DeleteExpired(ttl time.Duration) (int, error)
func (s *TxLogStore) RunGC() error
```

`keys.go` should implement:
```go
const (
    partitionByTx   = 0x01
    partitionByTime = 0x02
)

func makeKeyByTx(txShortID base.TransactionIDShort, clockNs int64) []byte
func makeKeyByTime(clockNs int64, txShortID base.TransactionIDShort) []byte
func parseKeyByTx(key []byte) (base.TransactionIDShort, int64, error)
func parseKeyByTime(key []byte) (int64, base.TransactionIDShort, error)
```

### Phase 2: Queued Writer Module

**2.1 Create `core/core_modules/txlogger/` package:**

Files to create:
- `core/core_modules/txlogger/txlogger.go` - queue-based writer module

The module should:
- Embed `CoreModule[input]` pattern from other core_modules
- Define input struct for queue messages
- Implement `global.TxLogWriter` interface
- Filter by transaction type based on current level
- Handle enable/disable lifecycle

```go
type TxLoggerModule struct {
    *core_module.CoreModule[input]
    store *txlogger.TxLogStore
    env   environment
}

type input struct {
    clockTs time.Time
    msg     string
    txids   []base.TransactionID
}

// Implements global.TxLogWriter
func (m *TxLoggerModule) TxLog(timestamp time.Time, msg string, txid ...base.TransactionID)

// Implements global.TxLogger
func (m *TxLoggerModule) TxLogEnable(lvl global.TxLogLevel)
```

### Phase 3: Node Integration

**3.1 Add txlogger initialization to `node/` package:**

- Add field to `ProximaNode` struct
- Create `initTxLogger()` method (similar to `initTxStore()`)
- Start background loops for TTL cleanup and Badger GC
- Handle graceful shutdown

**3.2 Wire up to workflow:**

- Pass `TxLogWriter` interface to workflow/core_modules that need logging
- Ensure proper startup/shutdown ordering

### Phase 4: API Endpoints

**4.1 Add path constants to `api/api.go`:**
```go
PathTxLogEnable = PrefixAPIV1 + "/txlog/enable"
PathTxLogGet    = PrefixAPIV1 + "/txlog/get"
PathTxLogRange  = PrefixAPIV1 + "/txlog/range"
```

**4.2 Add response types to `api/api.go`:**
```go
type TxLogRecordJSON struct {
    TxID           string `json:"txid"`
    ClockTimestamp int64  `json:"clock_ns"`
    Message        string `json:"message"`
}

type TxLogResponse struct {
    Error
    Records []TxLogRecordJSON `json:"records,omitempty"`
}
```

**4.3 Add handlers to `api/server/`:**
- `handleTxLogEnable` - parse level string, call `TxLogEnable()`
- `handleTxLogGet` - decode hex prefix, call `TxLogGet()`, return JSON
- `handleTxLogRange` - parse time range, call `TxLogIterate()`, return JSON

### Phase 5: Testing

**5.1 Unit tests in `txlogger/store_test.go`:**
- Test write and read single record
- Test write batch records
- Test prefix search with various prefix lengths
- Test time-range iteration
- Test TTL cleanup deletes old records
- Test enable/disable lifecycle

**5.2 Integration test (optional):**
- Test full flow through queued writer
- Test API endpoints

### Implementation Order

1. Phase 1.1 - Constants (quick)
2. Phase 1.2 - Store package with keys.go first, then store.go
3. Phase 5.1 - Unit tests (develop alongside store)
4. Phase 2 - Queued writer module
5. Phase 3 - Node integration
6. Phase 4 - API endpoints
7. Final testing and refinement

---

## Implementation Status: COMPLETE

All phases implemented. Files created/modified:

### Phase 1: Foundation
- `global/constants.go` - Added `TxLogDBName = "proximadb.txlog"`
- `txlogger/keys.go` - Key encoding/decoding with partition prefixes
- `txlogger/store.go` - TxLogStore with dual-index storage
- `txlogger/store_test.go` - 8 unit tests

### Phase 2: Queued Writer
- `core/core_modules/txlogger/txlogger.go` - Queue-based async writer module
- `core/core_modules/txlogger/txlogger_test.go` - 3 unit tests

### Phase 3: Node Integration
- `node/node.go` - Added `txLogger` field, import, `LogTx()` method
- `node/db.go` - Added `initTxLogger()`, `parseTxLogLevel()`

### Phase 4: API Endpoints
- `api/api.go` - Path constants and response types
- `api/server/server.go` - Environment interface updates, handler registration
- `api/server/txlog_handlers.go` - API handlers for enable/get/range
- `node/apiserver.go` - TxLogger method implementations

### Tests
All 11 tests pass:
- 8 store tests (keys, CRUD, prefix search, batch, time iteration, TTL, level filtering)
- 3 module tests (basic ops, level filtering, batch logging)

### Usage

**Configuration (`proxima.yaml`):**
```yaml
txlogger:
  enable_on_start: true        # auto-enable on node start
  level: "all"                 # off, branch, sequencer, non_sequencer, all
  ttl_hours: 1                 # record TTL in hours (default: 1)
  enable_on_off_api: false     # allow enabling/disabling via API (default: false)
```

**API:**
```bash
# Enable with level
curl -X POST "http://localhost:8080/api/v1/txlog/enable?level=all"

# Get records by txid prefix (hex)
curl "http://localhost:8080/api/v1/txlog/get?prefix=aabbcc&max=50"

# Get records in time range
curl "http://localhost:8080/api/v1/txlog/range?from=1706000000000000000&max=100"
```

**Programmatic (from node components):**
```go
// Log a transaction event
node.LogTx(time.Now(), "transaction received", txid)

// Log batch event
node.LogTx(time.Now(), "committed to ledger", txid1, txid2, txid3)
```

### Phase 5: CLI Commands
- `proxi/node_cmd/txlog.go` - CLI commands for txlog management
- `proxi/node_cmd/node_cmd.go` - Registered txlog command
- `api/client/client.go` - Added TxLogEnable, TxLogGet, TxLogRange methods

**CLI Usage:**
```bash
# Enable transaction logging (default level: non_sequencer)
proxi node txlog enable

# Enable with specific level
proxi node txlog enable --level all

# Disable transaction logging
proxi node txlog disable

# Get logs by transaction ID prefix (hex)
proxi node txlog get aabbcc

# View recent logs (last 1 minute by default)
proxi node txlog tail

# View logs from last 5 minutes
proxi node txlog tail --back 5
```
