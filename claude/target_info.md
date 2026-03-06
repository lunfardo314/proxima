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
- Transition counter (`ChainConstraint.TransitionCounter`) — on-chain enforced (z64)
- Branch counter (`ChainConstraint.BranchCounter`) — on-chain enforced (z32)
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
- Ignore freeze bound (`SequencerData.IsIgnoreFreezeBound()`)

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

## Task 2: Chain constraint refactoring — DONE

### Changes made
1. **Branch counter added** as arg $6 (z32) to chain constraint, on-chain enforced
   - Increments only on the sequencer output of branch transactions
   - Other chained outputs (delegations, etc.) copy predecessor's value
   - Assertion: `branchCounter <= transitionCounter`
2. **Transition counter** changed from z32 to z64 format
3. **`ChainH` / `BranchH` removed** from `SequencerData` — rely solely on on-chain counters
4. **`IgnoreFreezeBound` flag** added to `SequencerData` (JSON tag `"u"`, default false/omitted)
   - Replaces the node-local `ignore_upper_bound_on_freeze` config parameter
   - Now visible to delegators via on-chain sequencer data

## Task 3: `proxi node seq set` command — DONE

**Command:** `proxi node seq set [flags]`

**Flags** (all optional, only specified flags are changed):
- `--name <string>` — sequencer name
- `--fee <uint64>` — minimum tag-along fee
- `--margin <uint16>` — inflation profit margin promille (0-1000)
- `--greedy` / `--no-greedy` — greedy flag (use `--greedy=false`)
- `--pace <uint8>` — pace value (ticks)
- `--ignore-freeze-bound` / `--ignore-freeze-bound=false`

**File:** `proxi/node_cmd/seq_cmd/set.go`

---

## Verification

### target_info
1. `go build ./proxi/...`
2. `proxi node delegate target_info <known-sequencer-id> -t <node-url>`
3. Verify all sections display correctly

### chain constraint & seqdata refactoring
1. `go test ./ledger/tests/...` — PASS
2. `go test ./sequencer/seqdata/...` — PASS
3. `go build ./...` — PASS
4. Manual test with one_node_bootstrap
