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
- do not modify edit existing code. Detected vulnerabilities and problems must be documented in the `tx_test.go` and only fixed upon request 
- in tests, use usual patterns with the `utxodb`
- start with basic tests. 
- Many of validity rules are encoded as _EasyFL_ covenants. Expect additional instructions when writing tests for separate covenants. 

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

### Proposed additional topics for future sessions

- Endorsement validation (cross-slot rejection, pace constraints, duplicate endorsements)
- Amount conservation invariant (consumed + inflation = produced)
- Chain constraint validation (origin vs successor)
- Sequencer transaction specific rules
- Multi-signature unlock patterns
- Sender address lock constraints
- Deadlock lock constraints
- Tag-along output handling
- Maximum transaction size limits
- Output index bounds checking
