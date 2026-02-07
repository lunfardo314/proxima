# New transaction validation tests

## Goal
Write a set of validation tests for the Proxima part related to ledger and transactions, mostly located in `/ledger`. 

Similar set of test already exists in the `/ledger/tests`, however the goal of this task is to revisit the codebase independently,
check its consistency, detect possible vulnerabilities and attack vectors. 

The new set of tests must contain proofs that potential (theoretically possible) attack vectors are not possible. 

## Requirements

- analyze code and [available docs](https://lunfardo314.github.io/#/txdocs/tx)
- ask clarifying questions one by one, do not overwhelm me.
- keep log and state between sessions in the `tx_test.md`
- Create new tests in the separate file `tx_test.go`. If file grows too big, split it into several logical parts
- do not modify existing code. Detected vulnerabilities and problems must be documented in the `tx_test.go` and only fixed upon request 
- in tests, use usual patterns with the `utxodb`
- start with basic tests. 
- Many of validity rules are encoded as _EasyFL_ covenants. Expect additional instructions when writing tests for separate covenants. 
- Perform each topic (below) one by one. Before starting each topic, present rough understanding of it for confirmation and save it in this file. 

## In tests, cover the following topics
   
- duplicates not allowed among input IDs
- input commitment prevents "faked UTXO" attack: when upon construction of the transaction a malicious node provides tampered with
UTXOs for UTXO IDs
- signature of the transaction must be valid
- edge cases of the basic validation
- propose important topics

The task is incremental, the list to be expanded in the future.

## Session log

### Session 1: Basic transaction validation tests

**Files created:** `ledger/tests/tx_test.go`

**Tests implemented (all passing):**

| # | Test | Validation Stage | Topic |
|---|------|-----------------|-------|
| 1 | `TestTxValidBasicTransfer` | Full context | Sanity: valid transfer succeeds |
| 2 | `TestTxDuplicateInputsRejected` | Partial context | Duplicate input IDs rejected |
| 3 | `TestTxInputCommitmentPreventsFakedUTXO` | Full context | Faked UTXO attack: tampered output provided for valid input ID |
| 4 | `TestTxInputCommitmentWithWrongHash` | Full context | Corrupted input commitment field |
| 5 | `TestTxCorruptedSignatureRejected` | Partial context | Corrupted signature bytes |
| 6 | `TestTxSignatureMatchesButLockMismatch` | Full context | Valid signature but wrong key for consumed output lock |
| 7 | `TestTxEdgeCaseNoInputs` | Partial context | Zero inputs rejected |
| 8 | `TestTxEdgeCaseInputCommitmentCorrectness` | Full context | blake2b hash of consumed outputs matches commitment |
| 9 | `TestTxEdgeCaseTransferEntireBalance` | Full context | Transfer full balance (no change output) |
| 10 | `TestTxEdgeCaseMinimumStorageDeposit` | Builder | Output below minimum storage deposit rejected |
| 11 | `TestTxEdgeCaseTimePaceConstraint` | Parse/scan | Time pace constraint violation |
| 12 | `TestTxInputCommitmentMultipleInputs` | Full context | Multi-input tampering detected |
| 13 | `TestTxConsumedOutputHashMechanism` | Unit | Hash determinism, order sensitivity, content sensitivity |

**Key findings during implementation:**

1. **3-stage validation architecture**: Parse (Stage 1) → Partial context (Stage 2, no consumed UTXOs needed) → Full context (Stage 3, requires consumed UTXOs)
2. **EasyFL error messages**: `!!!underscored_names` are displayed with spaces ("underscored names")
3. **`validSignature` scope**: The EasyFL `validSignature(txID, txSignatureData)` only verifies that the ed25519 signature matches the public key embedded in the signature data. It does NOT verify that the public key matches any consumed output lock — that check is a separate constraint evaluated on each consumed output during full context validation
4. **Input commitment is a full-context check**: The blake2b hash comparison between `pathToInputCommitment` and `blake2b(pathToConsumedOutputs)` happens in `txIntegrityValidatorFullContext0`, not during partial context
5. **Time pace constraint**: Enforced during `scanInputs()` at parse stage, before EasyFL validation

**No vulnerabilities detected** in the covered areas. All attack vectors tested are properly rejected.

### Session 2: Amount conservation and token theft prevention

**Tests added (all passing):**

| # | Test | Validation Stage | Topic |
|---|------|-----------------|-------|
| 14 | `TestTxAmountProduceMoreThanConsumed` | Full context | Creating tokens from nothing (1.5B from 1B) rejected |
| 15 | `TestTxAmountProduceLessThanConsumed` | Full context | Destroying tokens (500M from 1B) rejected |
| 16 | `TestTxAmountOffByOne/one_extra_token` | Full context | +1 token difference detected |
| 17 | `TestTxAmountOffByOne/one_missing_token` | Full context | -1 token difference detected |
| 18 | `TestTxAmountConservationMultipleOutputs` | Full context + UTXODB | Correct 3-way split (300M+300M+400M=1B) settles |
| 19 | `TestTxAmountMultipleOutputsExcess` | Full context | 3x400M=1.2B from 1B rejected |
| 20 | `TestTxTheftSpendWithWrongKey` | Full context | Bob signs to spend Alice's UTXO — sigLock fails |
| 21 | `TestTxTheftUnlockReferenceDifferentLock` | Full context | Reference bypass: Alice's lock ≠ Bob's lock |
| 22 | `TestTxTheftReplayTransaction` | Full context (UTXODB) | Replay of settled tx — consumed outputs gone |
| 23 | `TestTxTheftRecipientOwnership` | Full context (UTXODB) | After transfer: Bob can spend, Alice cannot |

**Key findings:**

1. **Amount conservation invariant** (`consumed + inflation = produced`) is checked in TWO places as defense-in-depth:
   - `validate.go:validateOutputs()` line 160 — "mismatch between token amounts" (fires first)
   - `validate.go:ValidateFullContext()` line 95 — "unbalanced amount" (backup check)
2. **Single token precision**: Even a ±1 token difference triggers rejection
3. **sigLock constraint** (`lock_signature.easyfl`): `equal($0, txSpenderID(txSignatureData))` ensures only the private key holder can spend
4. **Unlock reference security**: `unlockedByReference` requires `equal(self, consumedConstraintByIndex($0, lockConstraintIndex))` — byte-for-byte identical lock. Cross-address references are impossible.
5. **Replay protection**: Consumed UTXOs are removed from state immediately. Replaying a settled transaction fails at `SetFullContext` because inputs no longer exist.

**No vulnerabilities detected** in covered areas. Token conservation and access control are robust.

**Total tests: 22 (13 from Session 1 + 9 from Session 2), all passing.**

### Session 3: Cleanup, benchmarks, overflow tests, size limit plan

**Code changes:**

1. **Removed redundant amount conservation check** in `validate.go:ValidateFullContext()` line 95.
   The primary check in `validateOutputs()` line 160 is guaranteed: called unconditionally via
   `ValidateFullContext()` -> `validateOutputs()` -> `_sumConsumedTotals()` -> conservation comparison.

2. **Updated test comments** to reflect the single enforcement point.

**Tests/benchmarks added:**

| # | Test | Type | Topic |
|---|------|------|-------|
| 24 | `TestParseRubbishDataRejected` | Test | Random data of 0-100KB rejected at stage 1 |
| 25 | `BenchmarkParseRubbishData` | Bench | Stage 1 rejection: ~690ns constant for 10B-1MB |
| 26 | `BenchmarkParseRubbishDataAllZeros` | Bench | Stage 1 rejection of zeros: ~275ns constant |
| 27 | `TestTxOverflowConsumedBalance` | Test | Two MaxInt64/2+1 consumed outputs overflow |
| 28 | `TestTxOverflowProducedBalance` | Test | Two MaxInt64/2+1 produced outputs overflow |
| 29 | `TestTxOverflowSingleMaxAmount` | Test | Boundary: MaxInt64-1 OK, MaxInt64 overflows, 2x overflow |
| 30 | `TestTxOverflowConservationCheckSafe` | Test | Large consumed + small produced -> mismatch detected |
| 31 | `TestTxOverflowAddToVectorContinuesAfterDetection` | Test | AddToVector wraps but flags overflow |

**Key findings:**

1. **Stage 1 rejection is O(1)**: ~690ns regardless of input size (10B to 1MB).
   Tuple parser fails on first few bytes. No DoS risk from large garbage payloads.

2. **AddToVector overflow check is conservative**: `vect[i] >= MaxInt64 - v` treats
   MaxInt64 itself as overflow. Maximum safe single amount is `MaxInt64 - 1`.

3. **Conservation check is safe despite potential wrapping**: If `consumed + inflation`
   could wrap (both near MaxInt64), the result is negative, which can't equal `produced > 0`.

4. **No arithmetic overflow vulnerabilities detected** in amount calculations.

**Total tests: 28 (22 previous + 1 rubbish + 5 overflow), all passing.**

### Plan: Data size limits for transaction elements

#### Current state of size enforcement

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

#### Gap analysis: missing limits

1. **No max transaction byte size at validation level.** The network caps at ~65KB,
   but no validation-level check in `Parse()`. Locally-constructed oversized transactions
   could waste CPU on tuple parsing.

2. **No per-output size limit.** An individual UTXO can theoretically be up to 4.3GB.
   No explicit bound on constraint data size within an output.

3. **No "other data" field size limit.** `TxOtherData` (element 10) has no size validation.

4. **No max unlock params size.** Each unlock block has no size limit.

5. **No max constraint bytecode size.** Individual constraint scripts have no size limit.

#### Proposed limits

| Element | Proposed limit | Rationale |
|---------|---------------|-----------|
| **Total transaction bytes** | 65,536 (64KB) | Match API/network limits. Check first in `Parse()`. |
| **Individual output (UTXO) bytes** | 8,192 (8KB) | Generous for any realistic constraints. |
| **Individual constraint data** | 4,096 (4KB) | EasyFL scripts are typically 10-100 bytes. |
| **"Other data" total** | 4,096 (4KB) | Arbitrary data field; limit prevents abuse. |
| **Unlock params per input** | 1,024 (1KB) | Unlock data is typically <100 bytes. |

#### Implementation: hybrid approach (recommended)

- Hard-code total transaction size limit in `Parse()` (must happen before any processing)
- Use EasyFL constants for per-output and per-constraint limits (checked during scanning)
- Critical limit always enforced, fine-grained limits upgradeable

### Key design finding: single-signature model

Proxima intentionally supports only a single transaction signature. This is a deliberate design
choice, not a limitation:
- The mandatory single signature uniquely identifies the spender
- All consumed inputs must be unlockable by that single spender (via signature or reference)
- Secure spender identification is also crucial for:
  - **Spam prevention**: the `txsenders` module rate-limits by public key
  - **Tag-along commands**: sequencer identifies the sender for tag-along output handling
- Multi-signature schemes (m-of-n) are intentionally not supported at the protocol level

### Next topics for future sessions

Priority order:

1. Chain constraint validation (origin vs successor)
2. Endorsement validation (cross-slot rejection, pace constraints, duplicate endorsements)
3. Sequencer transaction specific rules
4. Sender address lock constraints
5. Deadlock lock constraints
6. Tag-along output handling
7. Delegation related covenants
8. Output index bounds checking
