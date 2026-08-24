# Data size limits for transaction elements

> **QUEUED → `ledger/limits.md`** — Size and count limits on transaction elements.
> Rewritten there, then archived. See `claude/kb_reorg.md`.

## Goal

Add reasonable data size limits at the validation level to complement the existing network/API limits.
Tests in `ledger/tests/limits_test.go`.

## Current state of size enforcement

| Layer | Limit | Value | Location |
|-------|-------|-------|----------|
| P2P network | Max message | 65,531 bytes | `peering/misc.go:15` |
| API | Max upload | 65,536 bytes | `api/server/server.go:598` |
| Parse (stage 1) | Top-level elements | exactly 11 | `parse.go:36` |
| Parse (stage 1) | Produced outputs | 1-256 | `parse.go:102` |
| Partial (stage 2) | Endorsements | max 8 | `constMaxNumberOfEndorsements` |
| Partial (stage 2) | Duplicate inputs | rejected | EasyFL `tupleHasDuplicatesAtPath` |
| Tuple package | Elements per tuple | max 16,383 | `tuples.go:49` |
| Tuple package | Element data | max 4,294,967,295 bytes | `tuples.go:318` |
| Attacher | I/O cost | max 550 | `constAttachmentCostBudget` |

## Gap analysis (before implementation)

1. **No max transaction byte size at validation level.** Network caps at ~65KB, but no check in `Parse()`.
2. **No per-output size limit.** Individual UTXO can theoretically be up to 4.3GB.
3. **No "other data" field size limit.** `TxOtherData` has no size validation.
4. **No max unlock params size.** Each unlock block has no size limit.
5. **No max constraint bytecode size.** Individual constraint scripts have no size limit.

## Implemented limits

| Element | Limit | Constant | Check location |
|---------|-------|----------|----------------|
| **Total transaction bytes** | 65,536 (64KB) | `MaxTransactionSize` | `Parse()` — first check before any tuple parsing |
| **Individual output (UTXO) bytes** | 8,192 (8KB) | `MaxOutputSize` | `scanProducedOutputs()` — during output scanning |
| **"Other data" total** | 4,096 (4KB) | `MaxOtherDataSize` | `scanPartialContext()` — during partial context scan |
| **Unlock params per input** | 1,024 (1KB) | `MaxUnlockParamsSize` | `scanInputs()` — during input scanning |

Implementation approach: hard-coded constants in `ledger/transaction/parse.go`.
All constants are exported for use in tests.

## Session log

### Session 3: Initial implementation

- Added 4 size limit constants and checks in `ledger/transaction/parse.go`
- Size checks integrated into existing scanning loops (no separate pass):
  - `MaxTransactionSize` in `Parse()` as first check
  - `MaxOutputSize` in `scanProducedOutputs()` loop
  - `MaxUnlockParamsSize` in `scanInputs()` loop
  - `MaxOtherDataSize` inline in `scanPartialContext()`
- Created `ledger/tests/limits_test.go` with 8 tests — all passing:
  - `TestLimitsValidTransactionUnderAllLimits` — sanity: normal tx well under limits
  - `TestLimitsMaxTransactionSize` — oversized tx rejected at Parse()
  - `TestLimitsTransactionAtExactMax` — exact boundary not size-rejected
  - `TestLimitsMaxOutputSize` — oversized output rejected in scanProducedOutputs()
  - `TestLimitsMaxOtherDataSize` — oversized other data rejected
  - `TestLimitsOtherDataAtExactMax` — within-limit other data accepted
  - `TestLimitsMaxUnlockParamsSize` — oversized unlock params rejected
  - `TestLimitsConstantsConsistency` — constants internally consistent
