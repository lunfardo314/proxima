# TagAlongLock: Change $1 from SigLock bytecode to raw HolderID

## Motivation

The TagAlongLock EasyFL constraint previously stored its sender as full SigLock bytecode
(`sigLock(0x<holderID>)`, i.e. `a(0x<hash>)`). This added unnecessary complexity:

- **Production validation** had to parse bytecode to extract the holder hash for comparison
  (`parseInlineDataArgument(...)` to get the hash, then compare with `txHolderID`)
- **Consumption (reclaim)** evaluated `$1` as a constraint directly, coupling the lock format
  to the validation logic

Storing a raw 32-byte holder ID is simpler and more direct.

## Changes

### EasyFL source (`ledger/def/lock_tag_along.easyfl`)

**Before:**
- Helper `_selfSenderBytecode` wrapped `$1` in sigLock bytecode format
- Production: `parseInlineDataArgument(...)` to extract hash, then compare
- Reclaim: `$1` evaluated as constraint (was sigLock bytecode)

**After:**
- `$1` is now raw 32-byte holder ID
- Production: `equal($1, txHolderID(txSignatureData))` -- direct comparison
- Reclaim: `sigLock($1)` -- wraps raw ID into sigLock constraint at validation time

### Go side (`ledger/lock_tag_along.go`)

- Struct field: `Sender SigLock` -> `SenderID base.HolderID`
- Template: `"(0x%s, %s)"` -> `"(0x%s, 0x%s)"` (both args are now raw hex data)
- `TagAlongLockFromBytesWithLib()`: parse `args[1]` via `easyfl.StripDataPrefix` (raw data)
  instead of `SigLockFromBytesWithLib` (bytecode parsing)
- `Source()`/`String()`: `hex.EncodeToString(t.SenderID[:])` instead of `t.Sender.Source()`
- `Accounts()`: `SigLock(t.SenderID)` cast (both are `[32]byte`)
- `NewTagAlongOutput()`: parameter `sender SigLock` -> `senderID base.HolderID`

### Sequencer builder (`sequencer/txbuilder_seq/`)

- `parse.go`: removed `lock.Sender.Name() != ledger.SigLockName` check (no longer applicable);
  `ret.SenderID = lock.SenderID` (direct field access)
- `req_seqdata.go`, `req_withdraw.go`, `req_askstop.go`: `Sender: sender` -> `SenderID: base.HolderID(sender)`

### All callers of `NewTagAlongOutput`

Updated in: `ledger/txbuilder/txbuilder.go`, `ledger/txbuilder/endchain.go`,
`api/client/client.go`, `proxi/node_cmd/delegate/chain.go`, `proxi/node_cmd/delegate/amount.go`,
`ledger/tests/tag_along_test.go`, `ledger/tests/delegation_test.go`,
`tests/txbuilder_seq_test.go`, `tests/test_util.go`.

All wrap the `SigLock` value in `base.HolderID(...)` cast since `SigLock` is `type SigLock base.HolderID`.

## Verification

- `go build ./...` -- compiles clean
- `go test ./ledger/tests/...` -- all pass (including tag-along, delegation, chain tests)
- `go test ./sequencer/...` -- all pass
- `go test -run TestBase ./tests/...` -- sequencer txbuilder tests pass

## Findings

No vulnerabilities or issues found. The change is purely a simplification:

1. **No semantic change**: The EasyFL constraint enforces identical rules. Production still
   verifies `$1 == txHolderID(txSignatureData)`. Reclaim still requires sigLock match.
   Purge window still allows anyone. All three unlock windows are unchanged.

2. **Bytecode is smaller**: Raw 32-byte data (34 bytes with prefix) vs sigLock bytecode wrapping
   (~36 bytes). Minor savings per tag-along output.

3. **`SigLock` and `HolderID` are the same underlying type** (`[32]byte`). The cast is
   zero-cost and preserves all existing authorization checks in the sequencer builder
   (sender ID comparison against `HolderIDFromPublicKey`).
