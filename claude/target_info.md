# Task: `proxi node delegate target_info <sequencer ID>`

## Context

Delegators need a way to evaluate a target sequencer before delegating. Currently they must piece together info from multiple commands. This command parses the latest sequencer output and displays comprehensive info for the delegator.

## Task 1: Implement `target_info` API and command — DONE

### Architecture

**API endpoint:** `GET /api/v1/get_sequencer_target_info?chainid=<hex>`
- Server-side: computes library-dependent values (coverage bounds, epoch info, constants)
- Returns `api.SequencerTargetInfo` JSON struct with only primary data
- No delegator-assumption-dependent calculations (max delegation, advance estimates)

**API response struct:** `api.SequencerTargetInfo` in `api/api.go`
- Only primary data stored; derived values computed by consumers:
  - `AvailableForAdvance` = `TokenBalance - StorageDeposit`
  - `InflatableAmount` = `TokenBalance + FrozenCoverage[0]`
  - `InflatableWithinBounds` = bounds check on inflatable amount

**Client function:** `client.GetSequencerTargetInfo(chainID)` in `api/client/client.go`

**CLI command:** `proxi node delegate target_info <sequencer ID> [--json]`
- File: `proxi/node_cmd/delegate/target_info.go`
- Registered in `delegate_cmd.go`
- Display-only, no delegation estimates

### Files changed
- `api/api.go` — `PathGetSequencerTargetInfo`, `SequencerTargetInfo` struct
- `api/server/server.go` — `getSequencerTargetInfo` handler
- `api/client/client.go` — `GetSequencerTargetInfo` client method
- `proxi/node_cmd/delegate/target_info.go` — CLI command
- `proxi/node_cmd/delegate/delegate_cmd.go` — registration

### TODO (next session)
- `proxi node delegate chain/amount` commands should use this API for delegation
  estimates with delegator-specific flags (frozen epochs, inflation share, etc.)
- Reuse `estimateAdvance()` and `estimateMaxDelegationAmount()` from `check_advance.go`
  client-side with delegator assumptions

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
