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

Data size limit analysis and implementation moved to [claude/limits.md](limits.md).

Chain constraint analysis and test plan: [claude/chain_constraint.md](chain_constraint.md).

### Next topics for future sessions

Priority order:

1. Chain constraint validation (origin vs successor) — in progress
2. Endorsement validation (cross-slot rejection, pace constraints, duplicate endorsements)
3. Sequencer transaction specific rules
4. Sender address lock constraints
5. Deadlock lock constraints
6. Tag-along output handling
7. Delegation related covenants
8. Output index bounds checking
