# Transaction logger (txlogger)

## Purpose
Transaction logger is a core service in the Proxima node that allows tracking events related to individual transactions.
It allows other components of the node:
- to record log messages about specific transactions with transaction ID and timestamp
- to retrieve records attributed to a specific transaction based on its ID or a prefix of it (prefix matching)
- to retrieve events in a specified period of time

The log records are stored in a key-value store. The store is cleaned automatically according to a specified TTL.

The `txlogger` can be globally enabled and disabled dynamically.
When logger is disabled, any write to it is a no-op and any read from it returns an error.

When enabling logger, user can specify which types of transactions should be logged, while others are ignored.

## Requirements

### Core Requirements
* The implementation should be based on interfaces and types `TxLog*` defined in `global/types.go`
* The underlying key-value store must be abstracted via the `global.Store` interface
* The implementation must be based on Badger just like other key-value stores in the node
* Disabled `txlogging` means closing the database
* Enabled `txlogging` means opening database or creating a new one if it does not exist

### Configuration (in `proxima.yaml`)
```yaml
txlogger:
  enable: false          # master switch, default is false
  enable_on_start: false # auto-enable on node start, default is false
  ttl_hours: 1           # record TTL in hours, default is 1
```

### Key Structure
The key-value store uses two types of keys for dual indexing (`||` denotes concatenation):
* **By transaction**: `prefix_byte_txid` || `TransactionIDShort` (27 bytes) || `timestamp_nanosec` (8 bytes big-endian)
* **By timestamp**: `prefix_byte_time` || `timestamp_nanosec` (8 bytes big-endian) || `TransactionIDShort` (27 bytes)

Where:
- `TransactionIDShort` is 27 bytes: 1 byte max output index + 26 bytes TransactionHash
- `timestamp_nanosec` is 8 bytes: Unix nanoseconds in big-endian format
- Different prefix bytes distinguish the two key types

This dual-key structure enables:
- Efficient prefix-based lookup by transaction ID (or partial prefix)
- Efficient time-range iteration for retrieving records within a time period

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
Comprehensive unit tests covering:
- Write and read operations
- Prefix-based lookups with various prefix lengths
- Time-range queries
- TTL expiration
- Enable/disable lifecycle
- Log level filtering

### Integration
* Other components access txLogging via `TxLogWriter` interface
* Logging must be queued using patterns from `core_modules` (queue-based async processing)
* The txlogger service is initialized in the node package (similar to txstore)

### API Endpoints
* `POST /api/v1/txlog/enable?level=<level>` - enable txlogger with specified level
* `POST /api/v1/txlog/disable` - disable txlogger
* `GET /api/v1/txlog/get?txid_prefix=<hex_prefix>&max=<max>` - get records by transaction ID prefix
* `GET /api/v1/txlog/range?from=<unix_ns>&to=<unix_ns>&max=<max>` - get records in time range

---

## Interface Changes Required

Update `global/types.go` to use `TransactionIDShort` instead of `TransactionHash`:

```go
type TxLogReader interface {
    // TxLogGetByPrefix returns log records for transactions matching the given prefix
    // prefix can be 1-27 bytes of TransactionIDShort
    TxLogGetByPrefix(prefix []byte, maxRecords int) ([]TxLogRecordWithID, error)
    // TxLogIterate iterates over records in the time range [begin, end)
    TxLogIterate(begin, end time.Time, maxRecords int, fun func(rec TxLogRecordWithID) bool) error
}

type TxLogRecordWithID struct {
    TxIDShort base.TransactionIDShort
    Timestamp time.Time
    Message   string
}
```

---

## Implementation Plan

### Phase 1: Core Infrastructure

#### 1.1 Update Interfaces in `global/types.go`
- Change `TxLogReader.TxLogGet(txHash TransactionHash)` to `TxLogGetByPrefix(prefix []byte, maxRecords int)`
- Add `TxLogRecordWithID` struct with `TxIDShort`, `Timestamp`, `Message`
- Update `TxLogIterate` signature to include `end time.Time` and `maxRecords`
- Keep `TxLogWriter.TxLog(timestamp time.Time, msg string, txid ...base.TransactionID)` as is

#### 1.2 Create `txlogger` Package
Location: `/home/lunfardo/go/src/github.com/lunfardo314/proxima/txlogger/`

Files to create:
- `txlogger.go` - main TxLogger struct, implements Store interface wrapping badger
- `keys.go` - key construction helpers for dual-key indexing
- `txlogger_test.go` - unit tests

#### 1.3 TxLogger Core Implementation
```go
type TxLogger struct {
    global.NodeGlobal         // embedded for RepeatInBackground, Ctx, logging, etc.
    db          global.Store  // abstracted store interface, not badger-specific
    rawDB       *badger_adaptor.DB  // for GC only, nil after migration to RocksDB
    ttl         time.Duration
    level       atomic.Int32  // TxLogLevel
    enabled     atomic.Bool
    mu          sync.RWMutex  // protects db open/close
    dbPath      string
}
```

Key methods:
- `New(glb global.NodeGlobal, cfg Config) *TxLogger`
- `Enable(level TxLogLevel) error` - opens DB if closed, starts background tasks
- `Disable() error` - closes DB (background tasks stop via return false)
- `Write(timestamp time.Time, msg string, txid base.TransactionID) error`
- `GetByPrefix(prefix []byte, maxRecords int) ([]TxLogRecordWithID, error)`
- `Iterate(begin, end time.Time, maxRecords int, fn func(TxLogRecordWithID) bool) error`

Background tasks using `RepeatInBackground()`:
- `txlogger_cleanup` - runs every 10 minutes, deletes records older than TTL using time-indexed keys; returns `false` when disabled to stop
- `txlogger_gc` - runs every 5 minutes, calls `rawDB.RunValueLogGC(0.5)` for Badger GC; returns `false` when disabled to stop

This uses node's `RepeatInBackground()` infrastructure which provides:
- Global context integration for graceful shutdown
- Work process tracking (`MarkWorkProcessStarted/Stopped`)
- Proper lifecycle management

### Phase 2: Queue-Based Writer

#### 2.1 Create `txlog_writer` Core Module
Location: `/home/lunfardo/go/src/github.com/lunfardo314/proxima/core/core_modules/txlog_writer/`

This module provides async queued writing following core_modules patterns:
```go
type TxLogWriterModule struct {
    *core_modules.CoreModule[input]
    logger TxLoggerBackend  // interface to the actual txlogger
}

type input struct {
    timestamp time.Time
    msg       string
    txid      base.TransactionID
}
```

### Phase 3: Node Integration

#### 3.1 Add to ProximaNode (`node/node.go`)
- Add `txLogger *txlogger.TxLogger` field
- Add `txLogWriter *txlog_writer.TxLogWriterModule` field
- Add initialization in `Start()` sequence
- Add graceful shutdown handling

#### 3.2 Configuration Reading
Read config from `proxima.yaml`:
```go
viper.GetBool("txlogger.enable")
viper.GetBool("txlogger.enable_on_start")
viper.GetInt("txlogger.ttl_hours")
```

#### 3.3 Workflow Integration
- Pass `TxLogWriter` interface to workflow environment
- Components can call `TxLog()` method for transaction events

### Phase 4: API Implementation

#### 4.1 Add API Handlers (`api/server/txlogger_api.go`)
- `enableTxLogger` - POST handler to enable with level
- `disableTxLogger` - POST handler to disable
- `getTxLogByPrefix` - GET handler for prefix-based lookup
- `getTxLogRange` - GET handler for time-range query

#### 4.2 Add API Path Constants (`api/api.go`)
```go
PathTxLogEnable  = PrefixAPIV1 + "/txlog/enable"
PathTxLogDisable = PrefixAPIV1 + "/txlog/disable"
PathTxLogGet     = PrefixAPIV1 + "/txlog/get"
PathTxLogRange   = PrefixAPIV1 + "/txlog/range"
```

### Phase 5: Testing

#### 5.1 Unit Tests (`txlogger/txlogger_test.go`)
- Test dual-key write and read
- Test prefix matching with various prefix lengths (1, 4, 8, 27 bytes)
- Test time-range iteration
- Test TTL expiration (may need shorter TTL for testing)
- Test enable/disable lifecycle
- Test log level filtering (branch, sequencer, non-sequencer, all)

#### 5.2 Integration Tests
- Test API endpoints
- Test queued writing under load
- Test concurrent enable/disable

---

## File Structure Summary

```
global/types.go                           # Updated interfaces
txlogger/
    txlogger.go                           # Core TxLogger implementation
    keys.go                               # Key construction helpers
    txlogger_test.go                      # Unit tests
core/core_modules/txlog_writer/
    txlog_writer.go                       # Queue-based writer module
node/
    node.go                               # Add txLogger fields
    txlogger.go                           # Init and lifecycle (new file)
api/
    api.go                                # Add path constants
    server/
        txlogger_api.go                   # API handlers (new file)
```

---

## Open Questions (Resolved)

1. **Key type**: Using `TransactionIDShort` (27 bytes) instead of `TransactionHash` (26 bytes) - includes max output index
2. **Lookup**: Prefix matching supported - callers can provide 1-27 byte prefix
3. **TTL**: Manual cleanup via background task using `RepeatInBackground()` - no Badger-specific TTL features for RocksDB portability
4. **DB lifecycle**: Initialize at node startup, open/close based on enable state
5. **Module location**: TxLogger as node service, TxLogWriter as core_module for queued writes
6. **Background tasks**: Use node's `RepeatInBackground()` for cleanup and Badger GC - provides graceful shutdown and work process tracking
7. **Abstraction**: Use `global.Store` interface for KV operations, isolate Badger-specific code (GC) for future RocksDB migration
