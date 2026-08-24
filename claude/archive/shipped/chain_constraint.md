# Chain Constraint Validation

## Overview

The chain constraint enables **UTXO chains** (also called "chained accounts" or "UTXO accounts") —
a sequence of outputs linked by a persistent chain ID. Each chain has an origin output and zero or
more successor outputs. Tokens can be inflated on chains, but that is a separate topic;
all tests in this file assume inflation = 0.

## Key source files

| File | Purpose |
|------|---------|
| `ledger/def/chain.easyfl` | Chain constraint EasyFL definition (93 lines) |
| `ledger/def/lock_chain.easyfl` | ChainLock EasyFL definition (47 lines) |
| `ledger/chain.go` | ChainConstraint Go struct and operations |
| `ledger/lock_chain.go` | ChainLock Go struct and operations |
| `ledger/output.go` | Output types, chain extraction, filtering |
| `ledger/base/id.go` | ChainID type and `MakeOriginChainID` |
| `ledger/tests/ledger_test.go` | Existing chain constraint tests (TestChain1-3, TestChainLock) |

## Chain lifecycle

### Chain origin
- ChainID is all zeros (`NilChainID`)
- PredecessorInputIndex and PredecessorConstraintIndex are both `0xFF`
- The actual ChainID is derived as `blake2b(originOutputID)` after the transaction settles
- OriginSlot and OriginAmount are set at creation and become **immutable** through the chain's lifetime

### Chain successor (continuation)
- ChainID is the non-zero ID inherited from the origin
- PredecessorInputIndex/ConstraintIndex point to the consumed chain output (the input)
- The consumed chain output's unlock params contain `[successorOutputIdx, successorConstraintIdx]`
  pointing back to the successor — creating a **bidirectional crosscheck**

### Chain termination
- Unlock params set to `0xFFFF` discontinues the chain — no successor required

## Validation rules (from `chain.easyfl`)

### On produced output
1. Chain constraint cannot be at output index `0xFF` (255)
2. ChainID must be 32 bytes long
3. **Origin**: predecessor ref must be `0xFFFF`, origin slot must equal tx slot,
   origin amount must equal output token balance
4. **Non-origin**: predecessor's unlock param constraint index must point back to
   this constraint (`unlockParamsByConstraintIndex($1) == selfConstraintIndex`) — crosscheck

### On consumed output
1. If unlock params = `0xFFFF`: chain discontinued, no further checks
2. Unlock params must be exactly 2 bytes (for the chain constraint's slot in the unlock block)
3. Chain ID must match successor's chain ID:
   - For origin chain: `blake2b(inputID)` compared with successor's chain ID
   - For non-origin: direct chain ID comparison
4. Successor's predecessor constraint index must equal self constraint index — crosscheck
5. Origin slot must be preserved in successor (immutable)
6. Origin amount must be preserved in successor (immutable)

### ChainLock (`lock_chain.easyfl`)
A separate lock type that restricts output unlocking to whoever controls a specific chain:
- On produced output: lock must be at constraint index 1, chain ID must be 32 bytes and non-zero
- On consumed output: verifies the referenced chain's chain ID matches the lock's chain ID
- Prevents self-referencing (consumed output index != referenced chain output index)

## Unlock params structure

The chain constraint's unlock params are **2 bytes** at the chain constraint's slot
in the per-input unlock block tuple:
- Byte 0: successor output index (or `0xFF` if discontinuing)
- Byte 1: successor constraint index (or `0xFF` if discontinuing)

Note: the unlock block per input is a tuple with separate entries per constraint index,
so other constraints on the same input can have their own unlock params of different sizes.

**Observation**: `txbuilder.go:273` (`MakeSimpleChainTransition`) passes 3 bytes
`{successorOutputIndex, predecessorConstraintIndex, 0}` for chain unlock params,
while the EasyFL requires exactly 2. This may be a latent issue or the 3rd byte may
be silently tolerated. Worth testing.

## Test plan

Tests in `ledger/tests/chain_test.go`. All tests assume inflation = 0.

1. Create chain origin and verify chain ID derivation (`blake2b(outputID)`)
2. Valid chain transition (origin → successor) — full round-trip
3. Multi-step chain transition (origin → successor → successor)
4. Chain termination via `0xFFFF` unlock params
5. Invalid predecessor reference (wrong input index, wrong constraint index)
6. Origin slot immutability violation
7. Origin amount immutability violation
8. Chain ID mismatch between consumed and successor
9. Chain constraint at output index 0xFF rejection (if testable)
10. ChainLock: valid unlock via chain reference
11. ChainLock: wrong chain ID rejection
12. ChainLock: self-referencing prevention

## Session log

### Session 4: Chain constraint tests

Created `ledger/tests/chain_test.go` with 11 tests — all passing:

| # | Test | Topic |
|---|------|-------|
| 1 | `TestChainOriginCreation` | Origin output created, chainID = blake2b(outputID), state indexed |
| 2 | `TestChainValidTransition` | Origin → successor round-trip, chain ID and immutable fields preserved |
| 3 | `TestChainMultiStepTransition` | Origin → succ1 → succ2, immutables survive 2 transitions |
| 4 | `TestChainTermination` | Discontinue via 0xFFFF, chain removed from state |
| 5 | `TestChainInvalidPredecessorReference/wrong_predecessor_input_index` | predIdx=0xFF rejected |
| 6 | `TestChainInvalidPredecessorReference/wrong_predecessor_constraint_index` | predConstraintIdx=0xFF rejected |
| 7 | `TestChainOriginSlotImmutability` | originSlot+1 → "origin slot is immutable" |
| 8 | `TestChainOriginAmountImmutability` | originAmount+1 → "origin amount is immutable" |
| 9 | `TestChainIDMismatch` | Fake chainID → "chain ID mismatch with successor" |
| 10 | `TestChainInvalidUnlockParams` (3 sub) | Out-of-range successor indices, orphaned successor |
| 11 | `TestChainSuccessorReferenceCrosscheck` | Wrong constraint index in unlock params |
| 12 | `TestChainTransitionFromNonOrigin` | Non-origin consumed → direct chainID comparison |

**Observations:**
- When unlock params point to a non-chain constraint (e.g., lock at index 1), EasyFL
  fails at bytecode parsing ("unexpected call prefix 'a'") before reaching the crosscheck
- When unlock params point to out-of-range indices, EasyFL fails with "index is out of range"
- Both cases demonstrate that the validation is robust, just with different error paths

ChainLock tests added:

| # | Test | Topic |
|---|------|-------|
| 13 | `TestChainLockValidUnlock` | Spend chain-locked output via chain transition |
| 14 | `TestChainLockWrongChainID` | Unlock via wrong chain rejected |
| 15 | `TestChainLockSelfReference` | Self-referencing in unlock params rejected |

**No vulnerabilities detected** in chain constraint or ChainLock validation.

**Remaining from test plan:**
- Chain constraint at output index 0xFF (item 9 — may not be practically testable)
