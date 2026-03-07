# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Proxima is a DAG-based cooperative distributed ledger written in Go (~52K lines). It uses UTXO transactions as DAG vertices (no blocks, no mempool). Consensus is achieved through the **biggest ledger coverage rule** - similar to Bitcoin's longest chain but based on token coverage in the ledger state rather than proof of work. The principle is called _cooperative consensus_, where token holder's themselves converge to probabilistic consensus by cooperating and thus gravitating together towards the ledger state delta with the biggest coverage.

The multi-ledger DAG-based structure made of UTXO transactions as vertices is called the `tangle`.  

Key dependencies (part of Proxima ecosystem):
- `github.com/lunfardo314/easyfl` - EasyFL scripting language for UTXO constraints (covenants)
- `github.com/lunfardo314/unitrie` - Trie data structure and Merkle tree for multi-ledger state
- `github.com/lunfardo314/lunfrado314.github.io` - Contains all relevant documentation of Proxima

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
| `claude`             | Claude Code .md files with tasks, contexts, task status, findings                                                                       |

### UTXO transaction model 

Proxima uses advanced UTXO model for its transactions.
Read [Transaction Model Documentation](https://lunfardo314.github.io/#/txdocs/intro) or directly in the repo `github.com/lunfardo314/lunfrado314.github.io`.

#### Single-signature transaction model

Each transaction carries exactly one signature (`TxSignatureData`). This is an intentional design choice:
- The single signature uniquely identifies the holder. All consumed inputs must be unlockable by that holder
- Secure holder identification is crucial for spam prevention in the `txsenders` module (rate-limiting by public key)
- Tag-along commands to the sequencer rely on unambiguous sender identification
- Multi-signature schemes (m-of-n) are intentionally not supported at the protocol level. However, it can be supported by a transaction through programmability features  

### Programmability of the transaction
Proxima transaction is composed of data and scripts, that puts constraints on the data. This provides non-Turing complete programmability of transaction and individual UTXOs.
The scripting language is functional language of formulas `EasyFL`. See [claude/easyfl.md](claude/easyfl.md) and [EasyFL docs](https://lunfardo314.github.io/#/txdocs/easyfl)
The `EasyFL` serves also as serialization/deserializtion primitives.


### Some facts and links
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

1. **Reception**: receive raw transaction bytes from peer of from API in the `txinput_queue`, filter out repeating transactions, parse transaction ID. This is _stage 1_ transaction validation.
2. **Parse sender**: in `txsenders`: parse signature, *holder ID*, check signature. This is _Stage 2_ transaction validation. 
3. **rate limits**: apply limits of number of transactions per _holder ID_ in the ledger time window.
4. **Attach transaction**: put transaction to the memDAG and ensure all it inputs, endorsements - the past cone - are defined in the DAG. Sequencer transactions are attached by `attacher` goroutine. Baseline branch defines _baseline ledger state_ (UTXO set), it is determined for each sequencer transaction during attachment
5. **Conflict Detection**: `attacher` checks if a UTXO is not spend twice in the past cone of any transaction in the DAG.
6. **Transaction validation**: execute all UTXO constraints of the attached transaction. It is _Stage 3_ of transaction validation
7. **Persist updated UTXO sets**: each branch transaction represents a UTXO set that is persisted in the trie, handled by `multistate` package.

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

- keep the code minimalist and as simple as possible 
- do not introduce new abstractions, concepts or functions unless they are resued several times or improve readability    
- directory `claude` serves for Claude tasks with contexts
- Only modify CLAUDE.md upon explicit user confirmation
- in case of suspected inconsistencies between instructions in .md, ask clarifying questions
- Never add "Generated by Claude Code" or co-authored lines in commit messages
- Do not add "Generated by Claude Code" comments to files
- Name all test files generated by Claude as `claude_<some_name>_test.go`
- Always add explanatory comments to newly generated tests
- Do not invent new KV store access interfaces. Use existing interfaces from `multistate/kvtypes.go` (e.g., `StateStore`, `StateStoreReader`). 
For read+write operations, use `StateStore` which includes `BatchedUpdatable`
- Always use `encoding/binary.BigEndian` for serialization/deserialization of multi-byte integers unless there's a documented special case
- When building binaries, always use names `proxima` for the node and `proxi` for the CLI-tool. Never rename
- Prefer anonymous (embedded) fields over unexported fields with getters when extending structs or sharing behavior 
- **Mind `ledger.TimeNow()` for timing issues**: In tests, avoid using `ledger.TimeNow()` to derive timestamps for chain origins or transactions. Instead, derive timestamps from actual output timestamps (e.g., `outs[0].ID.Timestamp().AddSlots(1)`) to avoid race conditions between wall-clock time and ledger state time.
- **Ask about backward compatibility**: When refactoring code or changing data formats, always ask whether backward compatibility with legacy code or formats is required before assuming it is needed. Do not add legacy support unless explicitly confirmed.

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


