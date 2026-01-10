# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Proxima is a DAG-based cooperative distributed ledger written in Go (~52K lines). It uses UTXO transactions as DAG vertices (no blocks, no mempool). Consensus is achieved through the **biggest ledger coverage rule** - similar to Bitcoin's longest chain but based on token coverage in the ledger state rather than proof of work.

Key dependencies (part of Proxima ecosystem):
- `github.com/lunfardo314/easyfl` - EasyFL scripting language for UTXO constraints (covenants)
- `github.com/lunfardo314/unitrie` - Trie data structure and merkle tree for multi-ledger state

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

| Package              | Purpose                                                                                                                                 |
|----------------------|-----------------------------------------------------------------------------------------------------------------------------------------|
| `ledger`             | Ledger model, transaction validity rules, library of UTXO covenants, including locks and other constraints.                             |
| `ledger/base`        | base data types: transaction ID, UTXO/outputs ID, timestamp. Genesis definitions                                                        |
| `ledger/multistate`  | Multiple ledger states (branches) in overlapping Merkle trees (based on `unitrie`). BadgerDB-backed store                               |
| `ledger/transaction` | transaction, transaction context and related code                                                                                       |
| `ledger/txbuilder`   | various utility functions for transaction building                                                                                      |
| `ledger/utxodb`      | in-memory storage for the ledger state. Fully mimics multistate. Intended for unit tests                                                |
| `ledger/tests`       | unit tests for the `ledger` package. Mostly uses `utxodb` for transaction settlement                                                    |
| `core/workflow`      | Main transaction processing engine, coordinates all core modules                                                                        |
| `core/memdag`        | In-memory transaction DAG cache, with weak pointer caching                                                                              |
| `core/attacher`      | Validates and solidifies transactions, constructs UTXO tangle. One attacher goroutine per sequencer transaction                         |
| `core/vertex`        | In-memory transaction representations (`WrappedTx`, `Vertex`, `VirtualTx`)                                                              |
| `core/core_modules`  | permanent transaction workflow processes that handles incoming and outgoing flow of transactions, initiates attachers                   |
| `core/txmetadata`    | Optional data structure that can be attached to each raw transaction for consistency checking                                           |
| `sequencer`          | An optional process on the node, representing a token holder on the network that does _sequencing_ by pro-actively issuing transactions |
| `peering`            | P2P networking via libp2p, Kademlia DHT discovery                                                                                       |
| `api`                | REST and WebSocket API endpoints                                                                                                        |
| `proxi`              | CLI wallet and node management tool                                                                                                     |
| `node`               | Node orchestration, lifecycle management                                                                                                |
| `global`             | Shared infrastructure, logging, metrics, context                                                                                        |

### Some facts

* read [Proxima documentation](https://lunfardo314.github.io) for general proxima narrative
* read [Proxima transaction model](https://lunfardo314.github.io/#/txdocs/intro) for description of the transaction data structure
* all transactions make a directed-acyclic graph, a transaction DAG, called the tangle. MemDAG is in-memory cache of the part of the whole transactoon DAG 
* `solidification` means ensuring past cone of the transaction is known to the node. `solidification`and `attachment` are synonyms
* transaction, issued by a `sequencer` are called `sequencer transactions`
* each transaction has timestamp, a `ledger time`
* `timestamp` of the transaction is part of the `transaction ID`
* a sequencer transaction with timestamp on the slot edge (with ticks == 0) is called `branch transaction`
* each raw transaction is persisted in the `txstore`
* UTXO and `output` are commonly used as synonyms
* each UTXO is a `tuple` of validations scripts or constraints, expressed in EasyFL

### Transaction Flow

1. **Reception**: receive raw transaction bytes from peer of from API in the `txinput_queue`, filter out repeating transactions, parse transaction ID
2. **Parse sender**: in `txsenders`: parse signature, check signature, apply limits of number of transactions per public key
3. **Parse transaction**: Create `VirtualTx` placeholder in the MemDAG
4. **Attachment**: ensure all inputs of the transaction are available in the memDAG. Sequencer transactions are attached by `attacher` goroutine. Baseline branch is determined for each sequencer transaction during attachment
5. **Conflict Detection**: `attacher` checks if a UTXO is not spend twice in the past cone of any transaction in the DAG.
6. **Transaction validation**: execute all UTXO constraints of the attached transaction
7. **Persist updated UTXO sets**: each branch transaction represents a UTXO set that is persisted in the trie, handled by `multistate`.

### Key Data Structures

- **Ledger time** or **timestamp**: 4 bytes of slot + 1 byte. Last byte is 7 bytes of ticks in the slot. Last bit is the sequencer bit.
- ** TransactionID** (32 bytes): 5-byte timestamp + 1 byte of number of produced UTXOs + 26-bytes equal to the last 26 bytes of the 32-byte blake2b hash of the transaction essence bytes.
- **OutputID** (33 bytes): TransactionID + 1-byte output index.

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

- CLAUDE.md is committed to the repo
- CLAUDE.local.md is local session setting.
- Only modify CLAUDE.md and CLAUDE.local.md upon explicit user confirmation
- Never add "Generated by Claude Code" or co-authored lines in commit messages
- Only add "Generated by Claude Code" as a comment in newly generated functions and files, never in modified ones
- Always add explanatory comments to newly generated tests

### How to diagnose memory leak issues

```bash
# Enable pprof in proxima.yaml
pprof:
  enable: true
  port: 8080

# Capture heap profiles
curl -o heap1.pprof http://localhost:8080/debug/pprof/heap
# wait 30-60 minutes
curl -o heap2.pprof http://localhost:8080/debug/pprof/heap

# Compare allocations
go tool pprof -top -diff_base=heap1.pprof heap2.pprof
```

Send diff file to Claude 
