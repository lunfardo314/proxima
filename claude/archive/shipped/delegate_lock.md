# DelegateLock Refactoring: $1 Master from Accountable Bytecode to Raw HolderID

## Summary

Analogous to the TagAlongLock refactoring (`tag_along.md`). The DelegateLock EasyFL constraint
previously stored its `$1` master parameter as full `Accountable` bytecode (e.g., `sigLock(0x<hash>)`).
Since master is always a sigLock in practice, simplified to store raw 32-byte HolderID.
The EasyFL validation now wraps with `sigLock($0)` at runtime.

## Files Changed (11 files)

### EasyFL Source
- **`ledger/def/lock_delegate.easyfl`**: `_masterUnlockedConsumed` changed from `$0,` to `sigLock($0),`.
  Comments updated: `$1 master lock` -> `$1 master holder ID (32 bytes)`.

### Core Go
- **`ledger/lock_delegate.go`**: Struct field `MasterLock Accountable` -> `MasterID base.HolderID`.
  Template uses `0x%s` for raw hex. Parsing uses `easyfl.StripDataPrefix` + size check instead of
  `AccountableFromBytesWithLib`. `Master()` returns `SigLock(d.MasterID)`.
- **`ledger/lock_delegate_util.go`**: `MakeDelegateInitOutputParams` fields renamed and simplified.
  All `o.MasterLock` references -> `o.MasterID`.
- **`ledger/def_upgrade0.go`**: Variable reference `delegateLock2Source` -> `delegateLockSource`.

### Transaction Builder
- **`ledger/txbuilder/txbuilder.go`**: `MakeDelegationInitTransactionParams` fields renamed.
  Key match check and remainder lock updated for HolderID.

### Sequencer
- **`sequencer/txbuilder_seq/req_askstop.go`**: Removed type assertion `master, ok := ret.delegation.Master().(ledger.SigLock)`.
  Simplified to direct `o.SenderID != ret.delegation.MasterID` comparison.

### CLI (proxi)
- **`proxi/node_cmd/delegate/amount.go`**: Updated `MakeDelegateInitOutputParams` field names.
- **`proxi/node_cmd/delegate/askstop.go`**: `dOut.MasterLock` -> `dOut.MasterID` comparison.
- **`proxi/node_cmd/delegate/chain.go`**: `NewDelegateLock` call wrapped with `base.HolderID()`.

### Tests
- **`ledger/tests/delegation_test.go`**: Updated `NewDelegateLock` and `MakeDelegationInitTransactionParams` calls.
- **`tests/txbuilder_seq_test.go`**: `delegationInit` helper signature changed, callers updated.

## Naming/Comment Fixes (done as part of this refactoring)

1. Error messages: `"Delegate2LockFromBytes"` -> `"DelegateLockFromBytes"` (stale name from earlier rename)
2. Variable: `delegateLock2Source` -> `delegateLockSource`
3. Param fields renamed for consistency:
   - `Master Accountable` -> `MasterID base.HolderID`
   - `MaxFreezeEpochs` -> `MaxFrozenEpochs` (matches `DelegateLock.MaxFrozenEpochs`)
   - `MaxSeqProfitMargin` -> `RequiredInflationShare` (matches `DelegateLock.RequiredInflationShare`)

## Verification

- `go build ./...` -- clean
- `go test ./ledger/tests/...` -- passed
- `go test ./sequencer/...` -- passed
- `go test -run TestBase ./tests/...` -- passed
- `go test -run 'TestFreeze|TestWithUTXODB' ./tests/...` -- passed

## No Vulnerabilities Detected

The refactoring is strictly a simplification with no change in security properties.
The `sigLock($0)` wrapping in EasyFL produces identical validation behavior.
