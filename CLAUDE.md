# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Proxima is a DAG-based cooperative distributed ledger written in Go (~52K lines). It uses UTXO transactions as DAG vertices (no blocks, no mempool). Consensus is achieved through the **biggest ledger coverage rule** - similar to Bitcoin's longest chain but based on token coverage rather than work.

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

### Transaction Flow

1. **Reception** → Peer sends transaction bytes
2. **Parsing** → Create `VirtualTx` placeholder in MemDAG
3. **Solidification** → Pull missing inputs from peers
4. **Attachment** → Connect to branches via `attacher`
5. **Validation** → Execute output lock scripts (EasyFL)
6. **Conflict Detection** → Check double-spends against baseline
7. **Persistence** → Write to multistate DB and TxBytesStore

### Key Data Structures

**TransactionID** (32 bytes): 5-byte timestamp + 27-byte hash. Sequencer flag in timestamp's last bit.

**OutputID** (33 bytes): TransactionID + 1-byte output index.

**WrappedTx**: Thread-safe transaction wrapper with flags (Good/Bad/Solid/Validated/Rooted/InTrie).

**BranchData**: Branch metadata containing root hash, sequencer ID, coverage delta, supply, inflation.

### Consensus Model

- **Cooperative consensus**: No PoW, no BFT voting
- **Biggest ledger coverage rule**: Nodes follow branch with highest token coverage
- **Latest Reliable Branch (LRB)**: Current consensus state
- **Probabilistic finality**: 1-3 slots (10-30 seconds)
- **Slot duration**: 10 seconds
- **Sequencer pace**: 100 ticks per slot

### Lock Types (UTXO Constraints)

- `Ed25519` - Signature-based (standard)
- `Delegate` - Delegation to another account
- `Chain` - Constrained to specific chain
- `Deadline` - Time-locked
- `TagAlong` - Endorsement of parent outputs
- `Conditional` - Script-based conditions
- `Stem` - Special lock for branch stem outputs

## Configuration

Configuration via `proxima.yaml` in current directory (Viper-based):

```yaml
logger:
  output: <file path>
  verbosity: 0-2

metrics:
  enable: true
  port: 14000

workflow:
  do_not_start_pruner: false

transaction_pull:
  repeat_after_sec: 2
  max_attempts: 30
```

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
