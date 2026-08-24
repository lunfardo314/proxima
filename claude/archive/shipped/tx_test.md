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
3. **sigLock constraint** (`lock_signature.easyfl`): `equal($0, txHolderID(txSignatureData))` ensures only the private key holder can spend
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

Data size limit analysis and implementation moved to `limits.md` (same bucket); rewritten as `ledger/limits.md`.

Chain constraint analysis and test plan: [claude/archive/shipped/chain_constraint.md](../shipped/chain_constraint.md).

### Session 5: Endorsement validation tests

**File created:** `ledger/tests/endorsement_test.go`

**Tests implemented (all passing):**

| # | Test | Validation Stage | Topic |
|---|------|-----------------|-------|
| 1 | `TestEndorsementNonSequencerRejected` | Partial context (EasyFL) | Non-sequencer tx with endorsement rejected |
| 2 | `TestEndorsementCrossSlotRejected` | Parse (Go scan) | Endorsement in different slot rejected |
| 3 | `TestEndorsementPaceViolation` | Parse (Go scan) | Endorsement too close in time (< TransactionPaceSequencer) |
| 4 | `TestEndorsementTooMany` | Partial context (EasyFL) | 9 endorsements exceeds max (8) |
| 5 | `TestEndorsementDuplicateRejected` | Partial context (EasyFL) | Same endorsement twice rejected |
| 6 | `TestEndorsementValidSingle` | Partial context (pass) | Valid single endorsement accepted |
| 7 | `TestEndorsementMaxAccepted` | Partial context (pass) | Exactly 8 endorsements accepted |

**Key findings:**

1. **Two-stage endorsement validation**: Go-side `scanEndorsements()` checks cross-slot and pace
   at parse stage. EasyFL `_validEndorsements` checks sequencer-only, max count, and duplicates
   at partial context stage.
2. **Sequencer chain origin must endorse**: Creating a sequencer transaction as a chain origin
   requires at least one endorsement (EasyFL rule `_noChainPredecessorCase`). Non-origin
   successors with cross-slot predecessors also need endorsements, branch status, or explicit baseline.
3. **TransactionPaceSequencer** (default 2 ticks) governs endorsement pace — separate from
   TransactionPace (default 12 ticks) for non-sequencer input pace.
4. **PostBranchConsolidationTicks** (12) requires non-branch sequencer txs to have tick >= 12.

**No vulnerabilities detected** in endorsement validation.

Endorsement analysis: [claude/archive/shipped/endorsement.md](../shipped/endorsement.md).

### Session 6: Sequencer transaction specific rules

**File created:** `ledger/tests/sequencer_test.go`

**Tests implemented (all passing):**

| # | Test | Validation Stage | Topic |
|---|------|-----------------|-------|
| 1 | `TestSequencerMinimumAmountViolation` | Full context (EasyFL) | 500M tokens < 1B minimum rejected |
| 2 | `TestSequencerMinimumAmountExact` | Full context (pass) | Exactly 1B tokens accepted |
| 3 | `TestSequencerPostBranchConsolidation` | Full context (EasyFL) | Tick 5 < PostBranchConsolidationTicks (12) |
| 4 | `TestSequencerPreBranchConsolidation/multi_input_rejected` | Full context (EasyFL) | 2 inputs at tick 110 > 102 |
| 5 | `TestSequencerPreBranchConsolidation/single_input_accepted` | Full context (pass) | 1 input at tick 110 (skips check) |
| 6 | `TestSequencerSlotBoundaryNonBranch` | Parse (Go) | Tick 0 without stem output |
| 7 | `TestSequencerInputPace/one_tick_gap_rejected` | Parse (Go scan) | Gap 1 < TransactionPaceSequencer (2) |
| 8 | `TestSequencerInputPace/two_tick_gap_accepted` | Full context (pass) | Gap 2 = minimum accepted |
| 9 | `TestSequencerSameSlotNonSeqPredecessor` | Full context (EasyFL) | Non-sequencer same-slot predecessor |
| 10 | `TestSequencerCrossSlotNoEndorsements` | Full context (EasyFL) | Cross-slot without endorsements/branch/baseline |

**Key findings:**

1. **Slot boundary defense-in-depth**: At tick 0, Go parser assumes branch status and tries
   to find stem output before EasyFL `zeroTickOnBranchOnly` is reached. Missing stem output
   causes parse failure ("ParseSequencerData stem: wrong output index 255").
2. **Pre-branch consolidation window**: Ticks 103-127 (last 25 ticks of slot). Multi-input
   sequencer txs blocked; single-input exempted. Forces UTXO consolidation near slot boundary.
3. **Sequencer input pace** (2 ticks) is separate from non-sequencer input pace (12 ticks).
4. **Three predecessor cases**: Each with distinct requirements (endorsements, sequencer flag, etc.)

**Not tested (require complex infrastructure):**
- Branch transactions with stem output and VRF proof
- Explicit baseline validation

**No vulnerabilities detected** in sequencer-specific validation.

### Session 7: Tag-along output handling

**File created:** `ledger/tests/claude_tag_along_test.go`

**Tests implemented (all passing):**

| # | Test | Validation Stage | Topic |
|---|------|-----------------|-------|
| 1 | `TestClaudeTagAlongSpoofedSenderID` | Full context (EasyFL) | Third party creates tag-along with victim's HolderID |
| 2 | `TestClaudeTagAlongWrongSequencerConsumes` | Full context (EasyFL) | Chain B tries to consume tag-along targeted at chain A |
| 3 | `TestClaudeTagAlongManipulatedUnlockParams/wrong_chain_constraint_index` | Full context (EasyFL) | Wrong constraint index in chain lock unlock params |
| 4 | `TestClaudeTagAlongManipulatedUnlockParams/self-referencing_unlock_params` | Full context (EasyFL) | Tag-along unlock params reference self output |
| 5 | `TestClaudeTagAlongPurgeWindowSettle` | Full context + UTXODB | Random party consumes old tag-along (purge window) |
| 6 | `TestClaudeTagAlongTargetBalanceTampering` | Full context (EasyFL) | Target produces inflated balance on consumption |
| 7 | `TestClaudeTagAlongSenderHashForgeryOnReclaim` | Full context (EasyFL) | Random party tries reclaim in reclaim window |
| 8 | `TestClaudeTagAlongValidTargetConsumptionSettles` | Full context + UTXODB | Valid target consumption (positive E2E) |
| 9 | `TestClaudeTagAlongSenderReclaimSettles` | Full context + UTXODB | Valid sender reclaim (positive E2E) |

**Key findings:**

1. **Sender ID is cryptographically bound at production**: EasyFL `equal($1, txHolderID(txSignatureData))`
   prevents anyone from creating a tag-along claiming another party's sender ID. This is critical
   because the sender ID controls who can reclaim in the reclaim window.
2. **Cross-chain theft impossible**: `chainLock($0)` on consumption validates that the referenced
   chain constraint matches the target sequencer ID. A different sequencer's chain constraint
   has a different chain ID, so `_validChainUnlock` fails.
3. **Self-referencing prevented**: Chain lock EasyFL explicitly checks
   `not(equal(selfOutputIndex, byte(selfUnlockParameters,0)))`.
4. **Amount conservation prevents balance inflation**: Even with valid chain lock unlock,
   the target cannot produce more tokens than consumed (chain_amount + fee).
5. **Three-window unlock logic is sound**: Tag-along window → chain lock only;
   reclaim window → sigLock only; purge window → anyone. No overlap, no bypass.

**Other changes:**
- Marked `util.RequireErrorWithOld` as deprecated; tests use `require.NoError(t, util.MustErrorWith(...))`.

**No vulnerabilities detected** in tag-along output handling.

### Session 8: Delegation related covenants

**File created:** `ledger/tests/claude_delegation_test.go`

**Tests implemented (all passing):**

| # | Test | Validation Stage | Topic |
|---|------|-----------------|-------|
| 1 | `TestClaudeDelegationWrongMasterUnlock` | Full context (EasyFL) | Third party uses master unlock mode (0xff) — sigLock(masterID) rejects |
| 2 | `TestClaudeDelegationTargetReducesAmount` | Full context (EasyFL) | Target steals tokens by reducing delegation successor amount |
| 3 | `TestClaudeDelegationTargetChangesLock` | Full context (EasyFL) | Target replaces masterID on successor — lock immutability check |
| 4 | `TestClaudeDelegationTargetDiscontinuesChain` | Full context (EasyFL) | Target terminates delegation chain — only master can discontinue |
| 5 | `TestClaudeDelegationOriginCannotBeFrozen` | Full context (EasyFL) | Delegation origin created in frozen state rejected |
| 6 | `TestClaudeDelegationWrongConstraintCount` | Full context (EasyFL) | 5 constraints on delegation output — must be exactly 4 |
| 7 | `TestClaudeDelegationSafeRevocationWindow/target_blocked` | Full context (EasyFL) | Target unlock during safe revocation window rejected |
| 8 | `TestClaudeDelegationSafeRevocationWindow/master_can_unlock` | Full context (pass) | Master reclaims after safe revocation window |
| 9 | `TestClaudeDelegationInflationShareAbove1000` | Full context (EasyFL) | requiredInflationShare=1001 promille rejected (max 1000) |
| 10 | `TestClaudeDelegationOnHoldTargetRelock` | Full context (EasyFL) | On-hold delegation cannot be re-frozen by target |

**Key findings:**

1. **Master unlock requires sigLock(masterID)**: The EasyFL `_masterUnlockedConsumed` wraps the raw
   HolderID with `sigLock($0)` and verifies against the transaction signer. A third party cannot
   impersonate the master even with master unlock byte (0xff).
2. **Target cannot reduce amount**: `lessOrEqualThan(selfTokenBalanceValue, _amountOnSuccessor)`
   prevents any decrease in delegated token balance.
3. **Lock immutability on target unlock**: `equal(successorConstraint(1), selfSiblingConstraint(lockConstraintIndex))`
   ensures the delegation lock (including masterID, target, maxFrozenEpochs, inflationShare) is
   byte-identical on the successor. Target cannot swap in their own masterID.
4. **Chain discontinuation protection**: `not(equal(selfSiblingUnlockParams(2),0xffff))` blocks
   target from terminating the delegation chain. Only master with 0xff unlock mode can discontinue.
5. **Safe revocation window**: After freeze expires, 60-slot window where target is blocked and
   master can reclaim. Prevents target from immediately re-freezing after unfreeze.
6. **Constraint count enforcement**: `equal(selfNumConstraints, u64/4)` prevents injection of
   extra constraints into delegation outputs.
7. **On-hold is terminal for target**: `not(_selfIsMarkedOnHold)` in `_requireUnlockableByTheTarget`
   ensures once a delegation is on-hold, only the master can unlock it.

**No vulnerabilities detected** in delegation constraint validation.

### Session 9: Output index bounds checking

**File created:** `ledger/tests/claude_index_bounds_test.go`

**Tests implemented (all passing):**

| # | Test | Validation Stage | Topic |
|---|------|-----------------|-------|
| 1 | `TestIndexSigLockCrossLockReference/attacker_references_own_input` | Full context (EasyFL) | Bob references his input to unlock Alice's UTXO — lock bytes differ |
| 2 | `TestIndexSigLockCrossLockReference/valid_same_lock_reference` | Full context + UTXODB | Valid backward reference between two same-lock inputs |
| 3 | `TestIndexSigLockReferenceToChainLocked` | Full context (EasyFL) | sigLock input references chainLock-ed input — type mismatch |
| 4 | `TestIndexChainLockWrongConstraintType` | Full context (EasyFL) | chainLock unlock params point to amount constraint (not chain) |
| 5 | `TestIndexTagAlongOutOfRangeUnlockParams` | Full context (EasyFL) | Tag-along unlock params reference input index 5 (only 2 exist) |
| 6 | `TestIndexDelegationOutOfRangeUnlockParams` | Full context (EasyFL) | Delegation unlock params reference input index 10 (only 2 exist) |
| 7 | `TestIndexChainPredecessorNonExistentInput` | Full context (EasyFL) | Chain successor claims predecessor at input 5 (only 1 exists) |
| 8 | `TestIndexChainLockSelfReference` | Full context (EasyFL) | chainLock output references itself in unlock params |

**Key findings:**

1. **sigLock defense-in-depth**: sigLock has an `or` clause — either `unlockedByReference` OR
   `equal($0, txHolderID)`. When signed by the correct key, signature always succeeds as fallback.
   The `lessThan` ordering check in `unlockedByReference` is defense-in-depth; the byte-exact
   `equal(self, consumedConstraintByIndex($0, lockConstraintIndex))` is the primary protection
   against cross-lock reference attacks.
2. **EasyFL runtime bounds checking**: Out-of-range index in `atPath()` consistently produces
   "Tuple.At(N): index is out of range" panic-errors. This applies uniformly to tag-along,
   delegation, chain, and all lock types that use `consumedConstraintByIndex`.
3. **chainLock self-reference prevention**: `not(equal(selfOutputIndex, byte(selfUnlockParameters,0)))`
   prevents a chainLock-ed output from referencing itself as the unlock source.
4. **Cross-type lock reference**: sigLock ≠ chainLock at byte level, so referencing across
   lock types always fails the `equal(self, ...)` check.
5. **Chain predecessor crosscheck**: Produced chain validates that the consumed chain's unlock
   params reference back correctly. A fake predecessor index causes "crosscheck failed" even
   if the consumed chain has valid unlock params pointing to the correct successor.

**No vulnerabilities detected** in output index bounds checking.

### All topics completed

Priority order:

1. ~~Chain constraint validation~~ — done (Session 4)
2. ~~Endorsement validation~~ — done (Session 5)
3. ~~Sequencer transaction specific rules~~ — done (Session 6)
4. ~~Sender address lock constraints~~ — obsolete
5. ~~Deadlock lock constraints~~ — obsolete
6. ~~Tag-along output handling~~ — done (Session 7)
7. ~~Delegation related covenants~~ — done (Session 8)
8. ~~Output index bounds checking~~ — done (Session 9)
