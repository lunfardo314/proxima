# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Proxima is a DAG-based cooperative distributed ledger written in Go (~52K lines). It uses UTXO transactions as DAG vertices (no blocks, no mempool). Consensus is achieved through the **biggest ledger coverage rule** - similar to Bitcoin's longest chain but based on token coverage in the ledger state rather than proof of work. The principle is called _cooperative consensus_, where token holder's themselves converge to probabilistic consensus by cooperating and thus gravitating together towards the ledger state delta with the biggest coverage.

The multi-ledger DAG-based structure made of UTXO transactions as vertices is called the `tangle`.  

Key dependencies (part of Proxima ecosystem):
- `github.com/lunfardo314/easyfl` - EasyFL scripting language for UTXO constraints (covenants)
- `github.com/lunfardo314/unitrie` - Trie data structure and Merkle tree for multi-ledger state
- `github.com/lunfardo314/lunfrado314.github.io` - Contains all relevant documentation of Proxima

## Knowledge base
Main reference point is `CLAUDE.md`.

Directories `docs` and `claude` contain proper user documentation, task prompts, plans, findings and session reports.
Claude should use the content of these directories as a persistent and incrementally-improved knowledge base about the Project.
Claude should maintain index of the knowledge-base here in CLAUDE.md

### `docs/` index (user-facing documentation)

**Only `docs/run_standalone.md` is known up to date.** Every other entry below is
**OUTDATED — needs revisiting** before being relied on or quoted to the user.

| Doc | Status | Topic |
|-----|--------|-------|
| `docs/run_standalone.md` | up to date | Run a throwaway single-node network with a bootstrap sequencer (for frontend/wallet/browser developers). Companion to the WASM wallet at `ledger/txbuildercore/wasm/README.md`. |
| `docs/node_config.md` | up to date | Reference for all `proxima.yaml` node config tags (defaults + semantics derived from the `viper.Get*` call sites), with examples. |
| `docs/run_access.md` | OUTDATED | Run an access node and sync it with the testnet. |
| `docs/run_sequencer.md` | OUTDATED | Run a sequencer node. |
| `docs/api.md` | OUTDATED | REST/WebSocket API endpoint reference. |
| `docs/proxi.md` | OUTDATED | `proxi` CLI wallet/tool usage. |
| `docs/delegate.md` | OUTDATED | Delegation concepts and commands. |
| `docs/snapshot_format.md` | OUTDATED | Multi-state snapshot file format. |
| `docs/upgrade.md` | OUTDATED | Ledger library upgrade mechanism. |
| `docs/logging.md` | OUTDATED | Logging and tracing configuration. |
| `docs/testnet.md` | OUTDATED | Testnet topology and operations. |

## Architecture

### Core Packages

| Package              | Purpose                                                                                                                                   |
|----------------------|-------------------------------------------------------------------------------------------------------------------------------------------|
| `ledger`             | Ledger model, transaction validity rules, library of UTXO covenants, including locks and other constraints.                               |
| `ledger/base`        | base data types: transaction ID, UTXO/outputs ID, timestamp. Genesis definitions                                                          |
| `ledger/multistate`  | Multiple ledger states (branches) in overlapping Merkle trees (based on `unitrie`). BadgerDB-backed store                                 |
| `ledger/transaction` | transaction, transaction context and related code                                                                                         |
| `ledger/txbuilder`   | various utility functions for transaction building                                                                                        |
| `ledger/utxodb`      | in-memory storage for the ledger state. Fully mimics multistate. Intended for unit tests                                                  |
| `ledger/tests`       | unit tests for the `ledger` package. Mostly uses `utxodb` for transaction settlement                                                      |
| `core/workflow`      | Main transaction processing engine, coordinates all core modules                                                                          |
| `core/memdag`        | In-memory transaction DAG cache, with weak pointer caching                                                                                |
| `core/attacher`      | Validates and solidifies transactions, constructs UTXO tangle. One attacher goroutine per sequencer transaction                           |
| `core/vertex`        | In-memory transaction representations (`WrappedTx`, `Vertex`, `VirtualTx`)                                                                |
| `core/core_modules`  | permanent transaction workflow processes that handles incoming and outgoing flow of transactions, initiates attachers                     |
| `core/txmetadata`    | Optional data structure that can be attached to each raw transaction for consistency checking                                             |
| `sequencer`          | An optional process on the node, representing a token holder on the network that does _sequencing_ by pro-actively issuing transactions   |
| `peering`            | P2P networking via libp2p, Kademlia DHT discovery                                                                                         |
| `api`                | REST and WebSocket API endpoints                                                                                                          |
| `proxi`              | CLI wallet and node management tool                                                                                                       |
| `node`               | Node orchestration, lifecycle management                                                                                                  |
| `global`             | Shared infrastructure, logging, metrics, context                                                                                          |

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

### UTXO tuple layout

A UTXO is a tuple of byte-slices. The first three positions are framework
slots; positions 4+ are freeform per-lock extras.

| Index | Content | Notes |
|-------|---------|-------|
| 0     | amounts vector       | token balance, inflation, frozen-coverage |
| 1     | index-value tuple    | controllers / target / sender hashes used for trie indexing; iterated by the indexer. Each non-empty element produces one trie entry under `TriePartitionControllers`. Empty entries skipped. |
| 2     | lock bytecode        | EasyFL bytecode validating the unlock policy. For sig/chain/tag this is a per-kind constant (0-arg public symbol like `sigLock`); for delegate it carries 2 policy args (maxFrozenEpochs, inflationShare); for stem it carries the 9 stem aggregates. |
| 3     | chain constraint     | optional; present iff the output is a chain output |
| 4..   | extras               | per-lock state (e.g. `delegateLockState` at 4 for delegations), sequencer constraint (4) + milestone data (5) for sequencer outputs, etc. |

Design rationale and migration history: `claude/utxo-indexing.md`.

## Entry Points

- `main.go` - Node entry point, creates `ProximaNode` via `node.New()`
- `proxi/` - CLI commands (init, db, node, wallet, snapshot, util)

## Node Initialization Sequence

1. `startMetrics()` - Prometheus metrics
2. `CheckAndRestoreOnStartup()` - If DB missing/corrupted, restore from latest snapshot in `snapshot.directory` (refuses to start if none found)
3. `initMultiStateLedger()` - Initialize UTXO state
4. `initTxStore()` - Initialize transaction store
5. `initPeering()` - Set up P2P network
6. `startWorkflow()` - Start transaction processing (includes snapshot and snapshot_restore modules)
7. `startSequencer()` - Optional sequencer
8. `startAPIServer()` - REST API

## proxi CLI: wasm-style wallet architecture

`proxi` is modeled as an **external wasm wallet**: it does NOT depend
on the in-process `ledger.L()` singleton for tx construction or display.
Everything it needs is fetched over the API and held in per-process
wallet state.

**Per-process wallet state** (in `proxi/glb/`):

| Helper | What it gives you |
|--------|-------------------|
| `glb.GetLedgerConstants()` | `*txbuildercore.Constants` — slot/tick math, clock conversion, epoch limits, pace, etc. Fetched from `/api/v1/ledger_constants`. |
| `glb.GetTxLibrary()` | `*txbuildercore.Library` — compile / parse-bytecode-one-level / decompile bytecode + the wallet helper methods (`ParseChainConstraint`, `ParseDelegationOutput`, `ParseFoundryBytecode`, `ParseTokenAmountBytecode`, `ParseDelegationParams`). Fetched via `client.GetLibrary` (walks the upgrade chain). |
| `glb.SubmitAndDisplay(txBytes, consumedUTXOBytes…)` | Submits via `/api/v1/submit_tx`; on failure prints the failing tx pretty-form using the wallet library. |
| `client.Eval` / `client.EvalU64` | Batched closed-formula evaluator for things the wallet can't compute locally (e.g. `chainInflationMultiStep`). |

**Compose recipes** live in `ledger/txbuildercore/helpers_*.go`:
`NewSigLockOutput`, `NewChainLockOutput`, `NewTagAlongOutput`,
`NewChainOrigin`, `NewChainTransition`, `NewDelegateLockBytecode` +
`NewDelegateLockState` + `NewDelegationParams`, `NewFoundryBytecode` +
`TokenFoundry` + `TokenSentinel` + `NewTokenAmountBytecode` +
`AppendTokenAmountToOutput`, `NewSequencerRequestOutput` +
`NewEnsureStopDelegationConstraint`, `NewRedeemScriptConstraint`.
Wallet-side parsers return `*View` value types (`ChainConstraintView`,
`DelegationOutputView`, `DelegationParamsView`, `FoundryView`,
`TokenAmountView`) — pure byte parses, no eval, no singleton.

**Canonical templates** to copy when writing a new site:
- write path: `proxi/node_cmd/{send,compact,mkchain,killchain,fund}.go`
- read-only display: `proxi/node_cmd/{balance,chain,utxos,allchains}.go`
  + `proxi/node_cmd/seq_cmd/info.go`
- delegation / foundry / sequencer write paths:
  `proxi/node_cmd/delegate/`, `proxi/node_cmd/foundry/`,
  `proxi/node_cmd/seq_cmd/`

**Intentionally singleton-dependent** (NOT refactor candidates):
- `proxi/db_cmd/*` — operate on the local BadgerDB directly, no node
  API available. Singleton-dependent by design.
- `proxi/node_cmd/chess_cmd/*` + `examples/chess_poc/*` — kept as the
  in-tree typed-builder + singleton reference. `chess_poc` itself uses
  `ledger.L()` + `*txbuilder.TxBuilder`.
- `proxi/util_cmd/inflation.go` — eval-bound
  `ChainInflationMultiStep`. Could route through `client.EvalU64`
  but left on the singleton for now.
- `proxi/snapshot_cmd/check.go` — typed multistate snapshot parsers.

**Disabled bundle** (commented off; revive together when the faucet
is ported to txbuildercore):
- `proxi/glb/wallet_recipes.go` — legacy
  `TransferFromED25519Wallet` / `MakeSendOutputTransaction` /
  `MakeTransferTransaction` recipes.
- `proxi/node_cmd/faucet_srv.go` — long-running faucet server.
- `proxi/node_cmd/faucet_get.go` — `proxi node getfunds` client.

**`InitLedgerFromNode`** still exists in `proxi/glb/node.go` for the
chess/inflation/snapshot trio; the docstring lists the surviving
callers. Most proxi commands should never call it.

Key working rule for any new proxi site: **never reach for
`ledger.L()` from a CLI command**. Take what you need from
`glb.GetTxLibrary()` / `glb.GetLedgerConstants()` / `client.Eval*`.
If something genuinely cannot be expressed wallet-side (e.g. a new
eval-bound formula), add an entry to the closed-formula list of
`/api/v1/eval` rather than reaching for the singleton.

## Working Rules

- keep the code minimalist and as simple as possible 
- do not introduce new abstractions, concepts or functions unless they are resued several times or improve readability    
- **Enforce constraints in EasyFL when possible; reach for embedded Go only when the rule cannot be expressed in EasyFL.** UTXO and transaction invariants — immutability across transit, cross-slot equality, structural shape, signature/lock policies — live inside the constraint's own EasyFL body, the same way `chain()` enforces ChainID preservation or `delegateLock` enforces inflation share. Use Go (`evalXxx` builtins registered via `ledger/def/def_embed0.json` and `ledger/def_embed.go`'s resolver map) only for things EasyFL genuinely cannot do efficiently: aggregation across arbitrary slot positions in many outputs (e.g. `redeemScript`, `token(...)`, `tokenAmount(...)`), arithmetic that needs Go-level overflow handling, interaction with the per-tx context cache, or crypto primitives. Crypto primitives — `blake2b(...)` and `validSignatureED25519(...)` — live in Proxima at `ledger/crypto_builtins.go`; they used to be base-easyfl builtins (funCodes 73/74) but were moved here on 2026-05-18 since easyfl had no other consumer needing them.
- directory `claude` serves for Claude tasks with contexts
- Only modify CLAUDE.md upon explicit user confirmation
- in case of suspected inconsistencies between instructions in .md, ask clarifying questions
- Never add "Generated by Claude Code" or co-authored lines in commit messages
- Do not add "Generated by Claude Code" comments to files
- Name test files using their natural topic name (e.g. `utxo_indexing_test.go`); do not prefix with `claude_`
- Always add explanatory comments to newly generated tests
- Do not invent new KV store access interfaces. Use existing interfaces from `multistate/kvtypes.go` (e.g., `StateStore`, `StateStoreReader`). 
For read+write operations, use `StateStore` which includes `BatchedUpdatable`
- Always use `encoding/binary.BigEndian` for serialization/deserialization of multi-byte integers unless there's a documented special case
- When building binaries, always use names `proxima` for the node and `proxi` for the CLI-tool. Never rename
- Prefer anonymous (embedded) fields over unexported fields with getters when extending structs or sharing behavior 
- **Mind `ledger.TimeNow()` for timing issues**: In tests, avoid using `ledger.TimeNow()` to derive timestamps for chain origins or transactions. Instead, derive timestamps from actual output timestamps (e.g., `outs[0].ID.Timestamp().AddSlots(1)`) to avoid race conditions between wall-clock time and ledger state time.
- **Ask about backward compatibility**: When refactoring code or changing data formats, always ask whether backward compatibility with legacy code or formats is required before assuming it is needed. Do not add legacy support unless explicitly confirmed.
- **Never commit or push without asking**: Always ask the user before running `git commit` or `git push`. Do not combine them into a single action unless explicitly told to.
- for tracing during debugging: use globally available `Tracef()` tooling whenever possible. I.e. enable trace tags right in the code or ask user to enable them in node config.
- **Always use `proxi db txstore get` or the APIs exposed by `proxi db txstore dagviz` (`/api/tx_detail`, `/api/past_cone`, `/api/slot`, `/api/find_tx`) to analyze DAG topology before drawing any conclusions.** Logs are not a reliable source of the DAG — they show fragmentary events in submission/attachment order, not the actual input/endorsement/chain relationships. Inferring successor relationships from submit-time ordering has produced incorrect analyses before; always read the raw transaction (inputs, endorsements, chain constraint) from the DB to confirm.

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

## Testnet

Testnet is running on the following 4 machines:
- `boot`: `113.30.191.219`
- `loc0`: `63.250.56.190`
- `seq1`: `83.229.84.197`
- `loc1`: `5.180.181.103`

Sudo user `lunfardo` is used to do all operations on each machine.

On each machine there are 2 nodes configured:
- full/access node on directory `/home/nodes/<machine name>-acc`  
- sequencer node on directory `/home/nodes/<machine name>`

Both nodes are configured as `systemd` services.

### Prometheus monitoring

Prometheus runs on `boot` (`113.30.191.219`), scraping all 8 nodes every 15s. Retention: 10 days / 10 GB.

**Access**: `ssh lunfardo@113.30.191.219`, then `curl -s 'http://localhost:9090/api/v1/query?query=<METRIC>'`

**Grafana**: `http://113.30.191.219:3000`

**Instance mapping** (port 14000 = sequencer, port 14001 = access node):

| Instance | Node |
|----------|------|
| `113.30.191.219:14000` | boot |
| `113.30.191.219:14001` | boot-acc |
| `63.250.56.190:14000` | loc0 |
| `63.250.56.190:14001` | loc0-acc |
| `83.229.84.197:14000` | seq1 |
| `83.229.84.197:14001` | seq1-acc |
| `5.180.181.103:14000` | loc1 |
| `5.180.181.103:14001` | loc1-acc |

**Scrape interval**: 15s is sufficient. Memory spikes take ~60s (4 data points), steady-state analysis doesn't need higher resolution. 5s would double storage for marginal benefit.

Claude should proactively query Prometheus when analyzing node behavior, comparing seq vs access nodes, or investigating crashes.

#### Proxima application metrics

**MemDAG & pipeline:**

| Metric | Type | Description |
|--------|------|-------------|
| `proxima_memDAG_numVerticesGauge` | gauge | Vertices in the memDAG |
| `proxima_general_gauge_att` | gauge | Active attacher goroutines |
| `proxima_general_gauge_nonseq` | gauge | Non-seq vertices in memDAG |
| `proxima_general_gauge_nonseq_drop` | gauge | Dropped non-seq transactions (cumulative counter exposed as gauge) |
| `proxima_general_gauge_wait` | gauge | Txs waiting for clock alignment |
| `proxima_general_gauge_prop` | gauge | Active proposers |
| `proxima_general_gauge_store` | gauge | Store operations |
| `proxima_general_gauge_call` | gauge | Misc call counter |
| `proxima_general_gauge_close` | gauge | Close operations |
| `proxima_past_cone_size` | gauge | Transactions in past cone delta of last sequencer tx |

**Transaction input:**

| Metric | Type | Description |
|--------|------|-------------|
| `proxima_txInputQueue_in` | counter | Total incoming transactions |
| `proxima_txInputQueue_gossiped` | counter | Transactions gossiped to peers |
| `proxima_txInputQueue_pulled` | counter | Pulled (solicited) transactions |
| `proxima_txInputQueue_repeating` | counter | Dedup filter hits (bloom filter) |
| `proxima_txInputQueue_nonSequencer` | counter | Non-sequencer transactions received |
| `proxima_txInputQueue_txBytesSize` | gauge | Size of last received transaction bytes |

**Peering:**

| Metric | Type | Description |
|--------|------|-------------|
| `proxima_peering_txReceived` | counter | Transaction messages received from peers |
| `proxima_peering_txBytesReceived` | counter | Transaction bytes received from peers |
| `proxima_peering_inMsgCounter` | counter | Total incoming peer messages |
| `proxima_peering_outMsgCounter` | counter | Total outgoing peer messages |
| `proxima_peering_pullRequestsIn` | counter | Pull requests received |
| `proxima_peering_pullRequestsOut` | counter | Pull requests sent |
| `proxima_peers_alive` | gauge | Alive peers |
| `proxima_peers_all` | gauge | Total known peers |
| `proxima_peers_dead` | gauge | Dead peers |
| `proxima_peers_static` | gauge | Static (configured) peers |
| `proxima_response_to_pull_counter` | counter | Responses to pull requests served |

**LRB (Latest Reliable Branch):**

| Metric | Type | Description |
|--------|------|-------------|
| `proxima_lrb_coverage` | gauge | Ledger coverage of LRB |
| `proxima_lrb_supply` | gauge | Total supply on LRB |
| `proxima_lrb_slots_behind` | gauge | LRB slots behind current slot |

**Sequencer (only on sequencer nodes):**

| Metric | Type | Description |
|--------|------|-------------|
| `proxima_seq_milestones` | counter | Sequencer transactions submitted (incl. branches) |
| `proxima_seq_branches` | counter | Branch transactions submitted |
| `proxima_seq_targets` | counter | Sequencer target timestamps generated |
| `proxima_seq_backlog_size` | gauge | Tag-along outputs in sequencer backlog |
| `proxima_seq_own_milestones` | gauge | Own milestones in tippool |
| `proxima_seq_endorsements_N` | counter | Txs with N endorsements (N=0..8) |

**Validation & storage:**

| Metric | Type | Description |
|--------|------|-------------|
| `proxima_tx_validation_time_ns` | gauge | Last transaction validation time (ns) |
| `proxima_tx_validation_num_utxo` | gauge | Inputs + outputs in last validated tx |
| `proxima_tx_validated_total` | counter | Cumulative transactions that passed Stage-3 constraint validation on this node (one increment per tx). Use `rate()` for raw-processing TPS. Includes orphans/conflicted txs that validate but never settle. |
| `proxima_tx_confirmed_total` | counter | Cumulative transactions confirmed in the LRB. Bumped from `goLoggingSync` (10s LRB poll): each time the LRB slot has advanced, `lrb.NumConfirmedTransactions` (per-branch slot delta) is added. Approximate during forking/lineage switches but those windows are rare. Use `rate()` over a few minutes for settled TPS. |
| `proxima_glb_attachmentDurationMs` | gauge | Last attachment duration (ms) |
| `proxima_glb_attachments_counter` | counter | Total attachments |
| `proxima_txStore_txCounter` | counter | Transactions stored |
| `proxima_txStore_txBytesCounter` | counter | Cumulative bytes stored |
| `proxima_txStore_hit` | counter | TxStore lookup hits |
| `proxima_txStore_txBytesSizeHistogram` | histogram | Raw transaction size distribution |
| `proxima_txStore_txBytesSeqNonBranchSizeHistogram` | histogram | Seq non-branch tx size distribution |
| `proxima_branch_mutations` | counter | Cumulative mutation commands in branch commits |
| `proxima_branch_inflation_bonus` | gauge | Branch inflation bonus of last attached branch |
| `proxima_num_tx_dependencies` | gauge | Inputs + endorsements in last transaction |
| `proxima_counter_tx_dependencies` | counter | Cumulative inputs + endorsements |
| `proxima_disk_space` | gauge | Available disk space (MB) |
| `proxima_api_totalRequests` | counter | Total REST API requests |

#### Go runtime metrics (auto-collected)

| Metric | Description |
|--------|-------------|
| `go_goroutines` | Current goroutine count |
| `go_memstats_alloc_bytes` | Allocated heap bytes |
| `go_memstats_heap_alloc_bytes` | Heap allocation bytes |
| `go_memstats_heap_inuse_bytes` | Heap in-use bytes |
| `go_memstats_heap_sys_bytes` | Heap system bytes |
| `go_memstats_heap_objects` | Heap object count |
| `go_gc_cycles_total_gc_cycles_total` | Total GC cycles |
| `go_gc_cycles_forced_gc_cycles_total` | Forced GC cycles |
| `go_gc_heap_live_bytes` | Live heap bytes after GC |
| `go_gc_heap_goal_bytes` | GC target heap size |
| `go_gc_gomemlimit_bytes` | Configured GOMEMLIMIT |
| `go_gc_duration_seconds` | GC pause duration summary |
| `go_gc_pauses_seconds` | GC pause histogram |
| `go_threads` | OS threads |
| `go_sched_goroutines_goroutines` | Goroutine count (scheduler) |
| `process_resident_memory_bytes` | RSS (resident set size) |
| `process_virtual_memory_bytes` | Virtual memory |
| `process_cpu_seconds_total` | Cumulative CPU time |
| `process_open_fds` | Open file descriptors |

#### Useful PromQL queries

```promql
# Compare GC cycles between seq and access node on same machine
go_gc_cycles_total_gc_cycles_total{instance=~"63.250.56.190:.*"}

# Memory allocation rate (bytes/sec)
rate(go_memstats_alloc_bytes_total[1m])

# Goroutine count across all nodes
go_goroutines

# Attacher goroutines on access nodes only
proxima_general_gauge_att{instance=~".*:14001"}

# Non-seq drop rate
rate(proxima_general_gauge_nonseq_drop[1m])

# TPS (transactions received per second)
rate(proxima_peering_txReceived[1m])

# Branch commit rate
rate(proxima_seq_branches[1m])

# Raw TPS (transactions validated by this node per second; includes orphans)
rate(proxima_tx_validated_total{instance="$instance"}[1m])

# Settled TPS (transactions confirmed in the LRB per second; smooth over a few minutes)
rate(proxima_tx_confirmed_total{instance="$instance"}[5m])

# Branch mutations rate (state changes per second, scaled)
rate(proxima_branch_mutations{instance="$instance"}[1m]) * 10
```


