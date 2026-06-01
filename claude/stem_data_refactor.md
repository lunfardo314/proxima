# Stem data refactor — split `stemLock` args into a constrained lock + an unconstrained `StemData` tuple

Status: implemented on `develop08` (breaking ledger change / hardfork, no
backward compatibility). Spec + rationale below.

## Motivation

The branch stem output used to carry **9 deterministic aggregates as the
arguments of the `stemLock` constraint** (output index 2). Most of those values
are NOT actually verifiable inside EasyFL — they are past-cone aggregations the
node computes in Go. They are "verified" only because they are committed to the
trie: a node that computes them differently produces a different branch root, so
the network rejects it.

Problems with stuffing them into the lock:
- The constraint arity is fixed, so adding a new deterministic aggregate via the
  upgrade mechanism is awkward (arity bump touches registration + all parse
  sites).
- The lock (unlock policy) was overloaded with global ledger-state metadata.

## Final shape

### `stemLock` — 6 args (output index 2), still fully constrained

`stemLock(predOutputID, vrfProof, totalSupply, totalCoverage, coverageDelta, slotInflation)`

All six are verified on-chain:
- supply recurrence: `succTotalSupply == predTotalSupply + succSlotInflation`
- total-coverage halving: `succTotalCov == (predTotalCov >> K) + succCoverageDelta`
- VRF proof, predecessor-output-ID match
- genesis seed-value pinning (vrfProof empty; supply/cov/covDelta = initialSupply; slotInflation = 0)

Because `totalCoverage` + `coverageDelta` stay on args, **both recurrences are
self-contained in `stemLock` args** — the lock never reads the tuple.

Dropped from the EasyFL body (per design decision — they cannot be violated for
other reasons): `frozenCoverage <= totalSupply`, `mustSize(baselineRoot, 24)`,
and the genesis pinning of the moved fields. `selfNumConstraints` check is now
`== 4` (amounts, index-values, stemLock, stemData).

### `StemData` — inline-data literal (output index 3 = `ConstraintIndexChain`), unconstrained

The stem has no chain constraint, so output index 3 is free. `StemData` is stored
there as a **single inline-data literal `0x<serialized-tuple>`**, NOT a registered
constraint. When `runTuple` evaluates the element it returns the literal's
(non-empty) payload, which is truthy — so the branch validates with no EasyFL
logic and no `runTuple` change. (Confirmed: easyfl `dataFunction` returns the
payload bytes; `runTuple` only skips indices 0/1.)

Tuple layout (z64 = big-endian, leading zeros trimmed via
`easyfl_util.TrimmedLeadingZeroUint*`; raw on read via `easyfl_util.Uint*FromBytes`
which pad short/empty → zero):

| inner idx | field | enc |
|----|-------|-----|
| 0 | frozenCoverage | z64 |
| 1 | numConfirmedTransactions | z64 |
| 2 | numSeqTransactions (NEW) | z64 |
| 3 | numSeq (NEW) | z64 |
| 4 | baselineRoot | TrieHashSize raw bytes |

**Extensibility:** new deterministic aggregates append at inner idx 5+. The
literal's arity never changes; `StemDataFromBytes` reads by index with
absent ⇒ zero, so old readers ignore extras and new readers read them. Tuple
elements are raw (no inline-data prefix) — decode directly, do NOT `StripDataPrefix`.

## Two new aggregates

- `numSeqTransactions`: count of new (non-rooted) sequencer txs in the branch's
  past cone.
- `numSeq`: number of distinct sequencers among those.

Computed by `PastCone.NumNewTransactionStats(includeSeq ...base.ChainID)` — a
**single pass** that returns `(numTx, numSeqTx, numSeq)`. `NumNewTransactions()`
is now a thin wrapper over it.

### Branch-build prediction vs. verification (the subtle part)

The verifying milestone attacher's past cone INCLUDES the branch tx, so its
`NumNewTransactionStats()` is authoritative. The sequencer builds the branch
BEFORE the branch tx exists, from the extend target's cone (excludes the branch
tx). To match:
- `numConfirmedTransactions` = past + 1 (branch tx always new)
- `numSeqTransactions` = past + 1 (branch tx is itself a sequencer tx)
- `numSeq`: the branch tx's sequencer might NOT already be in the past-cone
  distinct set (edge case: a branch directly extends a rooted baseline seq
  output). So the builder **seeds the distinct-seq set with its own sequencer ID**
  (`NumNewTransactionStats(p.SequencerID())`) and uses the result verbatim. This
  exactly equals the verifier's branch-inclusive count.

`StemAggregates.NumSeq` is therefore the FINAL value; `NumSeqTransactions` is the
past-cone delta (+1 in `buildStemLock`).

## Files touched

- `ledger/lock_stem.go` — `StemLock` (6 fields) + new `StemData`; serde via `tuples.Tuple`; inline round-trip test.
- `ledger/def/lock_stem.easyfl` — 6-arg lock, dropped checks, `selfNumConstraints==4`, re-indexed successor reads.
- `ledger/output.go` — `Output.StemData()` / `MustStemData()`; `_lines` renders StemData readably (+ raw bytecode in verbose).
- `ledger/genesis.go` — emit StemData literal at index 3.
- `ledger/multistate/{state.go,roots.go,json.go}` — `BranchData`/JSON gain `NumSeqTransactions`/`NumSeq`; `FetchBranchDataByRoot` reads frozen/numTx/baselineRoot from StemData; display lines.
- `core/vertex/past_cone.go` — `NumNewTransactionStats`.
- `core/attacher/{attacher.go,check.go,wrapup.go}` — combined stats wrapper; `enforceStemValues(stemLock, stemData)`; PendingBranchCommit sourced from StemData.
- `core/core_modules/branches/branches.go` — `PendingBranchCommit` + cached BranchData gain the two fields.
- `sequencer/txbuilder_seq/txbuilder_seq.go` — `buildStemLock` returns `(*StemLock, *StemData)`; places StemData at index 3; `StemAggregates` gains the two fields.
- `sequencer/task/proposer.go`, `tests/test_util.go` — seed own sequencer ID into the stats call.

## Display

Stem output index 3 renders via `Output._lines` as
`stemData(frozenCoverage=…, numTx=…, numSeqTx=…, numSeq=…, baselineRoot=0x…)`,
plus raw bytecode in verbose mode.
