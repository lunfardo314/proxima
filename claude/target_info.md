# Task: `proxi node delegate target_info <sequencer ID>`

## Context

Delegators need a way to evaluate a target sequencer before delegating. Currently they must piece together info from multiple commands. This command parses the latest sequencer output and displays comprehensive info for the delegator.

## Task 1: Implement `target_info` command

### New file: `proxi/node_cmd/delegate/target_info.go`

**Command:** `proxi node delegate target_info <sequencer ID>`

**Data source:** `clnt.GetChainOutput(seqID)` → parse with `seqOut.Output.SequencerOutputData()` → `*ledger.SequencerOutputData` containing `ChainConstraint`, `SequencerData`.

### Display sections

**Identity & chain:**
- Sequencer ID (full hex)
- Name (`SequencerData.Name()`)
- Origin slot (`ChainConstraint.OriginSlot`)
- Current output slot (`seqOut.ID.Slot()`)
- Chain height (`SequencerData.ChainHeight()`) — currently unenforced, from seqdata JSON
- Branch height (`SequencerData.BranchHeight()`) — currently unenforced, from seqdata JSON
- Transition counter (`ChainConstraint.TransitionCounter`) — on-chain enforced
- Up-time estimate: `TransitionCounter / (nowSlot - OriginSlot)` steps per slot

**Balances:**
- Token balance (`seqOut.Output.TokenBalance()`)
- Storage deposit (`ledger.MinimumStorageDeposit(seqOut.Output)`)
- Available for advance: `tokenBalance - storageDeposit`
- Frozen coverage vector (`seqOut.Output.Amounts().FrozenCoverageVector(lib.MaxFrozenEpochs)`) — display non-zero entries with epoch index
- Inflatable amount (`seqOut.Output.InflatableAmount()` = tokenBalance + frozenCoverage[0])
- Cumulative chain inflation (`ChainConstraint.CumulativeChainInflation`)
- Cumulative branch bonus (`ChainConstraint.CumulativeBranchBonus`)

**Sequencer parameters:**
- Minimum fee (`SequencerData.MinimumFee()`)
- Profit margin promille (`SequencerData.InflationProfitMarginPromille()`)
- Greedy flag (`SequencerData.IsGreedy()`)
- Pace (`SequencerData.Pace()`)

**Delegation info:**
- Current epoch (`lib.EpochFromSlotDirect(seqID, nowSlot)`)
- Next epoch boundary slot (`lib.LastSlotInEpochDirect(seqID, currentEpoch)`) + wall clock time
- Max frozen epochs (`lib.MaxFrozenEpochs`)
- Epoch duration (`lib.DelegationEpochSlots` slots)
- Coverage bounds at now: `lib.BranchCoverageLowerBound(nowSlot)` / `BranchCoverageUpperBound(nowSlot)`
- Whether inflatable amount is within bounds

**Max acceptable delegation:**
- Reuse `estimateMaxDelegationAmount()` from `check_advance.go:73` (same package)
- Reuse `estimateAdvance()` from `check_advance.go:59`

### Registration

Add `initTargetInfoCmd()` to `delegate_cmd.go` `AddCommand` list.

### Key reusable code
- `estimateAdvance()` — `proxi/node_cmd/delegate/check_advance.go:59`
- `estimateMaxDelegationAmount()` — `proxi/node_cmd/delegate/check_advance.go:73`
- `ledger.MinimumStorageDeposit()` — `ledger/sdeposit.go:26`
- `ledger.ClockTime()` for wall clock conversion
- Existing display pattern from `status.go`

---

## Task 2: Chain height / branch height refactoring (TBD)

### Current state
- `ChainConstraint.TransitionCounter` (arg $5, `z32`) — **on-chain enforced**, incremented each chain transition
- `SequencerData.ChainH` / `SequencerData.BranchH` — in seqdata JSON blob, **not enforced**, maintained by sequencer software only

### Observation
Chain height is already redundant with `TransitionCounter`. Branch height has no on-chain enforcement at all.

### Possible directions (needs discussion)
1. **Add branch counter to chain constraint** — new arg $6 (`z32`), enforced on-chain for branch transactions. Would require EasyFL changes in `chain.easyfl` and Go code in `chain.go`
2. **Remove redundant `ChainH` from seqdata** — rely solely on `TransitionCounter` for chain height
3. **Keep `BranchH` in seqdata** vs enforce it — trade-off between trust and simplicity

### Impact areas if enforcing branch counter
- `ledger/chain.go` — `ChainConstraint` struct, serialization templates, `NewChainConstraint()`
- `ledger/def/chain.easyfl` — add arg $6, enforcement rule for branch transitions
- `sequencer/txbuilder_seq/txbuilder_seq.go` — build chain constraint with branch counter
- All callers of `NewChainConstraint()` (proxi delegate commands, tests)
- Backward compatibility: existing chain outputs won't have $6

---

## Verification

### target_info
1. `go build ./proxi/...`
2. `proxi node delegate target_info <known-sequencer-id> -t <node-url>`
3. Verify all sections display correctly

### branch counter (if implemented)
1. `go test ./ledger/tests/...`
2. `go test ./sequencer/...`
3. Manual test with one_node_bootstrap
