# Endorsement Validation

## Overview

Endorsements are references from a sequencer transaction to other sequencer transactions
in the same slot. They form lateral links in the DAG, connecting concurrent sequencer
activity within a time slot.

## Key source files

| File | Purpose |
|------|---------|
| `ledger/def/tx_integrity_validator.easyfl` | `_validEndorsements` EasyFL function (3 checks) |
| `ledger/transaction/parse.go` | `scanEndorsements()` Go-side checks (cross-slot, pace) |
| `ledger/def/sequencer.easyfl` | Sequencer constraint — requires endorsements in certain cases |
| `ledger/constants.go` | `TransactionPaceSequencer`, `PostBranchConsolidationTicks` constants |

## Validation rules

### Stage 1 — Parse (Go-side, `scanEndorsements`)

1. **Cross-slot rejection**: All endorsements must reference transactions in the same slot
   as the endorsing transaction. Different slot → "cross-slot endorsements are not allowed"
2. **Pace constraint**: The gap between the endorsing tx timestamp and the endorsed tx
   timestamp must be at least `TransactionPaceSequencer` ticks (default 2).
   Violation → "violates sequencer time pace constraint"

### Stage 2 — Partial context (EasyFL, `_validEndorsements`)

1. **Sequencer-only**: Only sequencer transactions can have endorsements.
   Non-sequencer with endorsements → "only sequencer transactions can endorse"
2. **Max count**: At most `constMaxNumberOfEndorsements` (8) endorsements per transaction.
   Excess → "number of endorsements too big"
3. **No duplicates**: All endorsement IDs must be unique.
   Duplicate → "duplicated endorsements not allowed"

### Sequencer constraint requirements (EasyFL, `sequencer.easyfl`)

The sequencer constraint imposes additional endorsement requirements depending on
the chain predecessor type:

- **Chain origin** (`_noChainPredecessorCase`): Must have at least one endorsement.
  "sequencer chain origin must endorse another sequencer transaction"
- **Same-slot predecessor** (`_sameSlotPredecessorCase`): Either predecessor is a
  sequencer tx OR the successor has endorsements.
- **Cross-slot predecessor** (`_crossSlotPredecessorCase`): Must be a branch tx,
  OR have endorsements, OR have an explicit baseline.

## Constants

| Constant | Default | Description |
|----------|---------|-------------|
| `TransactionPaceSequencer` | 2 ticks | Minimum gap between endorsed and endorsing tx |
| `constMaxNumberOfEndorsements` | 8 | Maximum endorsements per transaction |
| `PostBranchConsolidationTicks` | 12 | Minimum tick for non-branch sequencer txs |

## Test plan and results

Tests in `ledger/tests/endorsement_test.go`. All passing.

| # | Test | Stage | Topic |
|---|------|-------|-------|
| 1 | `TestEndorsementNonSequencerRejected` | Partial (EasyFL) | Non-sequencer with endorsement |
| 2 | `TestEndorsementCrossSlotRejected` | Parse (Go) | Different slot endorsement |
| 3 | `TestEndorsementPaceViolation` | Parse (Go) | Too-close endorsement |
| 4 | `TestEndorsementTooMany` | Partial (EasyFL) | 9 endorsements (max 8) |
| 5 | `TestEndorsementDuplicateRejected` | Partial (EasyFL) | Duplicate endorsement |
| 6 | `TestEndorsementValidSingle` | Partial (pass) | Valid single endorsement |
| 7 | `TestEndorsementMaxAccepted` | Partial (pass) | Max 8 endorsements |

**No vulnerabilities detected** in endorsement validation.

## Test helper design

The endorsement tests use a two-phase helper pattern:

1. `setupSequencerChain()` — Creates a chain origin with sequencer constraint and a
   dummy endorsement (required for sequencer origins). Settles it in UTXODB. Returns
   the chain output, chain ID, and a pre-computed successor timestamp.

2. `buildSequencerSuccessor()` — Builds a chain successor that inherits the sequencer
   constraint via `Clone()`. The caller provides endorsements relative to the actual
   successor timestamp, avoiding timestamp mismatch bugs.
