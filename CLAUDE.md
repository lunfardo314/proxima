# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Proxima is a DAG-based cooperative distributed ledger written in Go (~52K lines). It uses UTXO transactions as DAG vertices (no blocks, no mempool). Consensus is achieved through the **biggest ledger coverage rule** - similar to Bitcoin's longest chain but based on token coverage in the ledger state rather than work.

Key dependencies (part of Proxima ecosystem):
- `github.com/lunfardo314/easyfl` - EasyFL scripting language for UTXO constraints
- `github.com/lunfardo314/unitrie` - Trie data structure for ledger state

## Build and Test Commands

```bash
# Build the project
go build ./...

# Build the CLI tool
go build -o proxi .

# Run all tests
go test ./...

# Run tests in a specific package
go test ./ledger/tests/...
go test ./core/workflow/...

# Run a single test
go test -run TestName ./path/to/package/...

# Run tests with verbose output
go test -v ./...
```

## Architecture

### Core Packages

| Package | Purpose |
|---------|---------|
| `core/workflow` | Main transaction processing engine, coordinates all core modules |
| `core/memdag` | In-memory transaction DAG with weak pointer caching |
| `core/attacher` | Validates and solidifies transactions, constructs UTXO tangle |
| `core/vertex` | Transaction representations (`WrappedTx`, `Vertex`, `VirtualTx`) |
| `ledger` | Ledger model, transaction rules, output types, locks |
| `ledger/multistate` | Multiple ledger states (branches), BadgerDB-backed store |
| `sequencer` | Milestone-based sequencing, issues sequencer transactions |
| `peering` | P2P networking via libp2p, Kademlia DHT discovery |
| `api` | REST and WebSocket API endpoints |
| `proxi` | CLI wallet and node management tool |
| `node` | Node orchestration, lifecycle management |
| `global` | Shared infrastructure, logging, metrics, context |

### Some facts

* see [Proxima transaction model](https://lunfardo314.github.io/#/txdocs/intro) for description of the transaction data structure
* `solidification` and `attachment` are synonyms
* some transaction are `sequencer transactions`
* each transaction has timestamp, a `ledger time`
* `timestamp` of the transaction is part of the `transaction ID`
* a sequencer transaction with timestamp on the slot edge (with ticks == 0) is called `branch transaction`
* each transaction is persisted in the `txstore`

### Transaction Flow

1. **Reception**: receive raw transaction bytes from peer of from API in the `txinput_queue`, filter out repeating transactions, parse transaction ID
2. **Parse sender**: in `txsenders`: parse signature, check signature, apply limits of number of transactions per public key
3. **Parse transaction**: Create `VirtualTx` placeholder in MemDAG
4. **Attachment**: ensure all inputs of the transaction are available in the memDAG. Sequencer transactions are attached by `attacher` goroutine. Baseline branch is determined for each sequencer transaction during attachment  
5. **Conflict Detection**: `attacher` checks is a UTXO is not spend twice in the past cone of any transaction.
6. **Transaction validation**: Execute output lock scripts (EasyFL) of the attached transaction 
7. **Persist updated UTXO sets**: each branch transaction represents a UTXO ser that is persisted in the trie.

### Key Data Structures

**Ledger time** or **timestamp**: 4 bytes of slot + 1 byte. Last byte is 7 bytes opf ticks in the slot. Last bit is sequencer bit.  
** TransactionID** (32 bytes): 5-byte timestamp + 1 byte of number of produced UTXOs + 26-bytes equal to the last 26 bytes of the 32-byte blake2b hash of the transction essence bytes.

**OutputID** (33 bytes): TransactionID + 1-byte output index.

## Entry Points

- `main.go` - Node entry point, creates `ProximaNode` via `node.New()`
- `proxi/` - CLI commands (init, db, node, wallet, snapshot, util)

## Node Initialization Sequence

1. `startMetrics()` - Prometheus metrics
2. `initMultiStateLedger()` - Initialize UTXO state
3. `initTxStore()` - Initialize transaction store
4. `initPeering()` - Set up P2P network
5. `startWorkflow()` - Start transaction processing
6. `startSequencer()` - Optional sequencer
7. `startAPIServer()` - REST API

## Working Rules

- Only modify CLAUDE.md upon explicit user confirmation

---

## ONGOING: Sequencer Memory Leak Investigation

**Status**: Analysis in progress, awaiting pprof heap profile

### Symptoms
- Memory leak rate: ~50MB/hour
- Only occurs when sequencer is enabled
- **Memdag vertices are stable** (not leaking)
- **Goroutines are stable** (not leaking)

### Areas Cleared (NOT the cause)

| Component | Location | Why Cleared |
|-----------|----------|-------------|
| ownMilestones map | `sequencer/sequencer.go:58` | Purged every 1s with 24-slot TTL, map recreated every 1min |
| Backlog outputs | `sequencer/backlog/backlog.go:38` | Purged with 10-slot TTL, map recreated every 1min |
| consumed map in WrappedTx | `core/vertex/types.go:63` | Memdag vertices stable, so this isn't growing |
| milestoneAttacher | `core/attacher/attacher_milestone.go` | Properly closed via defer with pastCone.Dispose() |
| Event handlers | `core/core_modules/events/events.go` | Only added once per sequencer start |
| Goroutines | - | Confirmed stable |

### Potential Leak Sources (TO INVESTIGATE with pprof)

1. **IncrementalAttacher not closed on `newProposal()` failure**
   - Files: `sequencer/task/proposer_base.go:83-86`, `proposer_endorse1.go:53-56`, all other proposer_*.go
   - Pattern: When `newProposal(a)` returns error, attacher `a` is not closed
   - Even if GC'd eventually, PastCone references persist longer than necessary
   - **Potential fix**: Add `a.Close()` in error paths

2. **`_collectConsumed` creates growing sets**
   - File: `sequencer/own_milestones.go:82-106`
   - Each milestone stores `set.Set[base.OutputID]` of consumed outputs in past chain
   - Set grows as chain lengthens within TTL window

3. **tippool's `latestSequencerData` never purged**
   - File: `core/core_modules/tippool/tippool.go:35`
   - Entries added but never deleted (unlike `latestMilestones`)
   - Small per-entry but unbounded growth

### Next Steps (pprof session)

```bash
# Heap profile
go tool pprof http://localhost:<port>/debug/pprof/heap
# Then: top 20, list newProposal, list _collectConsumed, list AddOwnMilestone

# Check specific allocations
curl http://localhost:<port>/debug/pprof/heap?debug=1 > heap.txt
```

### Quick Fix to Test

In `sequencer/task/proposal.go`, close attacher on error:
```go
func (p *proposer) newProposal(a *attacher.IncrementalAttacher) (*proposal, error) {
    txb, err := txbuilder_seq.New(...)
    if err != nil {
        a.Close()  // ADD THIS
        return nil, fmt.Errorf("newProposal: %w", err)
    }
    for _, vid := range a.Endorsing() {
        if err = txb.AddEndorsement(vid.ID()); err != nil {
            a.Close()  // ADD THIS
            return nil, fmt.Errorf("newProposal: %w", err)
        }
    }
    // ... rest unchanged
}
```
