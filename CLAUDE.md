# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Proxima is a DAG-based cooperative distributed ledger written in Go (~80K lines,
plus ~42K of tests). UTXO transactions are the vertices of a DAG called the
**tangle** — no blocks, no mempool. Consensus comes from the **biggest ledger
coverage rule**: like Bitcoin's longest chain, but measured in token coverage of
the ledger state rather than proof of work. Token holders converge on it by
cooperating, which is also the most profitable thing each can do — hence
_cooperative consensus_.

**[`ARCHITECTURE.md`](ARCHITECTURE.md) is the system reference** and carries the
complete index of every document in and around the repository. Start there for
anything structural; this file is working rules.

Ecosystem dependencies:
- `github.com/lunfardo314/easyfl` — EasyFL scripting language for UTXO constraints (covenants)
- `github.com/lunfardo314/unitrie` — trie and Merkle structures for the multi-ledger state
- `github.com/lunfardo314/lunfardo314.github.io` — the public documentation site

## Knowledge base
Main reference point is `CLAUDE.md`.

Directory `claude` holds design notes, plans, findings and session reports.
Claude should use it as a persistent and incrementally-improved knowledge base
about the Project, and should maintain its index here in CLAUDE.md.

**IMPORTANT — read before touching the core.** Two documents are **hard
constraints**, not notes:

- [`claude/dag_semantics.md`](claude/dag_semantics.md) — the authoritative
  semantic model of the transaction DAG (the tangle) and the memDAG. Binds any
  change to `core/memdag`, `core/attacher`, `core/vertex`, and the attachment /
  coverage / pruning logic.
- [`claude/sync_semantics.md`](claude/sync_semantics.md) — the authoritative
  model of how a node catches up with the network. Binds
  `core/core_modules/forward_sync`, `core/attacher`, `core/workflow`,
  `sequencer`, `node`.

Keep core changes consistent with both; each is evolved only with explicit user
approval.

### Developer documentation

**`ARCHITECTURE.md`** (repo root) is the developer front door and holds the
**complete index of every document** in and around the repository, each with a
line on when to read it. It is not duplicated here: keep the index there, and
this file for working rules.

**A developer document lives in the package it documents.** There is no `docs/`
directory. Two span packages rather than sitting in one, and are the ones worth
knowing about before you touch anything:

- `core/resilience.md` — spam and DDoS protection, survivability and recovery,
  plus every gate on the transaction path. Read before changing anything that
  rejects, drops, defers or rate-limits a transaction, and when a node is
  shedding load.
- `ledger/limits.md` — size and count ceilings, and which of the four layers
  enforces each.

The user-facing operational guides (`run_standalone`, `run_access`,
`run_sequencer`, `node_config`, `wallet_config`, `proxi`, `delegate`,
`testnet`) live on the public docs site under `participate/`
(`github.com/lunfardo314/lunfardo314.github.io`,
`https://lunfardo314.github.io/#/participate/...`); repo links point at those
URLs.

Documentation review progress is tracked in `claude/docs.md`; the
reorganization of this knowledge base is planned and tracked in
`claude/kb_reorg.md`.

### `claude/` index

Every document opens with a status blockquote — `HARD CONSTRAINT`, `LIVE`,
`RESEARCH` or `META`. Trust that line over the
document's own prose: during the 2026-08-24 reorganization, fourteen documents
were found describing themselves wrongly, usually calling a shipped feature
unimplemented.

**The working set.** These twelve describe what is *running*, plus the two hard
constraints and the two meta documents. Nothing is queued for migration.

| Doc | Status | Topic |
|-----|--------|-------|
| `dag_semantics.md` | constraint | Semantic model of the tangle and the memDAG. Read before touching the core. |
| `sync_semantics.md` | constraint | Semantic model of how a node catches up. Read before touching sync. |
| `delegation_scalability.md` | live | Delegation count drives permanent state growth; the fixed freeze grid is the answer. §8–§9 implemented. |
| `delegation_freeze_distribution.md` | live | Amount-weighted balancer spreading freeze epochs across the reachable window. Implemented; the load-vector model is still open. |
| `delegation_freeze_stall.md` | live | Why consensus health decays while capital participation stays full: the delegation pool went blind to transitions authored by the delegation's master, and delegations silently left the freeze rotation. Fixed `3ecdb614`, awaiting validation under live load. Read before touching `sequencer/delegationpool`. |
| `sequencer_conflict_resolution.md` | live | How sequencers resolve conflicting tag-alongs. `numSeq` determines branch coverage exactly; the branch deferral is implemented but not yet validated under live load. Records three reverted search attempts. |
| `inflation.md` | live | The two components of inflation: the arithmetic, closed forms, overflow analysis and the `proxi util inflation_emulation` tool. The user-facing half is on the docs site. **Two of its claims reached the site wrong before being caught** — verify against `ledger/def/{inflation,chain}.easyfl` before quoting it. |
| `monitor.md` | live | Spec 0 for the monitor page: what it shows, where each number comes from. **Prototyped** — `api/monitor/` (`9996b0f2`); the spec's "awaiting approval before prototyping" line was stale and is corrected. |
| `compact.md` | live | Spec for the enhanced `proxi node compact`: scan by category, category-selective and parallel multi-round compaction, auto mode. **Not built yet.** Read before issuing several transactions from one wallet: the per-sender pace gate drops timestamps closer than `TransactionPace` **silently**, since API submit is async. |
| `state_scan_paging.md` | live | Spec for reading more state than one API call returns: a stateless cursor over a controller's UTXOs, IDs-only listing with paged fetch, and pinned-snapshot sessions (deferred). **Not built yet.** Companion to `compact.md`, which works without it. |
| `kb_reorg.md` | meta | Plan, classification and progress for the knowledge-base reorganization. |
| `docs.md` | meta | Documentation effort: plan, status, progress. Absorbed the docs-site audit. Its "What is left" is the **pending queue, worked in order** — final consolidation, then maintenance guidelines. |

**`claude/research/`.** Four documents that were investigated and **not
built** — `tick_duration.md`, `branch_fork_convergence.md`, `credit_tokens.md`,
`forced_delegation.md`. Kept separate because a design note sitting beside live
documents gets read as a description of how things work. In that directory the
assumption is inverted: if it is there, the code does not do it. Index and the
reason each is unbuilt: [`claude/research/README.md`](claude/research/README.md).

**Nothing is queued.** The `QUEUED → <destination>` convention is retired: the
reorganization finished on 2026-08-25 and every document that was waiting to be
rewritten onto the docs site has been. If you meet a `QUEUED` header, it is a
leftover — treat the document by its content, not its header.

**`claude/archive/`.** Ninety documents that no longer describe current
work, in three buckets, each with a `README.md` indexing every file in it:

| Bucket | What it holds | Read it for |
|--------|---------------|-------------|
| `archive/incidents/` | One event, one date, resolved. | Whether a recurring symptom has been seen before. Its index also lists three issues recorded and never closed. |
| `archive/shipped/` | Specs for features now on `develop`. **The code is the truth.** | Why an alternative was rejected — the one thing code cannot record. |
| `archive/superseded/` | Overtaken, shelved, or never taken up. **Nothing here is a plan.** | Why an approach was *not* taken. |

## The system

**Described in [`ARCHITECTURE.md`](ARCHITECTURE.md), not here.** The package
map, the transaction and UTXO model (including the single-signature rationale,
the key data structures and the UTXO tuple layout), the transaction and node
lifecycles, the three databases, where the ledger rules live and what makes a
change a hardfork, and the vocabulary — all of it is there, in one place. Read
it before changing anything structural.

What stays in this file is what ARCHITECTURE.md deliberately does not carry:
working rules, the knowledge-base index, build and test commands, and the
testnet and metrics reference below.

## proxi CLI: wasm-style wallet architecture

`proxi` is modeled as an **external wasm wallet**: it does NOT depend
on the in-process `ledger.L()` singleton for tx construction or display.
Everything it needs is fetched over the API and held in per-process
wallet state.

**Per-process wallet state** (in `proxi/glb/`):

| Helper | What it gives you |
|--------|-------------------|
| `glb.GetLedgerConstants()` | `*txbuildercore.Constants` — slot/tick math, clock conversion, epoch limits, pace, etc. Fetched from `/api/v1/ledger_constants`. |
| `glb.GetTxLibrary()` | `*txbuildercore.Library` — compile / parse-bytecode-one-level / decompile bytecode + the wallet helper methods (`ParseChainConstraint`, `ParseDelegationOutput`, `ParseFoundryBytecode`, `ParseTokenAmountBytecode`, `ParseDelegationParams`, `ParseSequencerConstraint`, `ClassifyChain`). Fetched via `client.GetLibrary` (walks the upgrade chain). `ClassifyChain(o, oid) ChainKind` is the singleton-free chain classifier (none/other/sequencer/foundry/delegation/mine) — classify by the output's own constraints, never by `oid.IsSequencerTransaction()` (a delegation transition rides inside its target sequencer's tx, setting the output ID's sequencer bit). Mirrors server-side `api/chain_explorer.makeRow`. |
| `glb.SubmitAndDisplay(txBytes, consumedUTXOBytes…)` | Submits via `/api/v1/submit_tx`; on failure prints the failing tx pretty-form using the wallet library. |
| `client.Eval` / `client.EvalU64` | Batched closed-formula evaluator for things the wallet can't compute locally (e.g. `chainInflationMultiStep`). |
| `glb.NodeAPIURL()` | The URL of the node API this proxi talks to: wallet-profile key **`api.node_url`**, falling back to the legacy name `api.endpoint`. Never read either key directly — a command that does will miss the alias. The `--api.node_url` / `--api.endpoint` flags are bound per running command by `glb.BindNodeAPIFlags` in `PersistentPreRun`; binding them at registration time silently loses the flag, because several command trees register the same keys. |

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
- **Code comments: concise, explanatory, self-contained.** Explain the *why*, in one or two sentences; cut anything that is really commit-message material. Avoid fragile references (`§3-§4`, paragraph/line numbers) — they drift; if pointing at a spec doc, name the doc, not a paragraph. Do not bake concrete runtime/config specifics (node names, env names) into comments. Mention a regression/bug only when it still carries explanatory value for the code as it stands, not as changelog.
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
- **After changes in the core (`core/memdag`, `core/attacher`, `core/vertex`, `core/workflow`, `sequencer`), always run the relevant tests under the race detector (`go test -race ...`).** The lock-free past-cone traversal relies on the "Good ⇒ immutable" assumption; functional runs pass without `-race` because the flag races are benign-by-monotonicity on x86 TSO, so only `-race` surfaces a broken assumption. A clean functional run is not sufficient evidence that a core change is concurrency-safe.
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

Testnet is running on 5 machines, each with a sequencer and an access node:
`hboot`, `hloc0`, `oseq1`, `oloc1`, `oloc2`.

Three of them are **public nodes** - the only addresses advertised anywhere
(docs site, `proxi` config templates, sync/snapshot sources):

| Machine | Access node API |
|---------|-----------------|
| `hloc0` | `http://65.21.170.230:8001` |
| `oseq1` | `http://79.137.70.25:8001` |
| `oloc2` | `http://51.254.47.76:8001` |

`hboot` (bootstrap sequencer) and `oloc1` stay reachable but are **not**
advertised. Sequencer APIs are disabled or firewalled on every box, so `:8000`
is not usable from outside.

The public-node list is a single table in `proxi/config_cmd/public_nodes.go`,
rendered into the `peers` / `sources` entries of `proxima.yaml` and the
`api.node_url` hints of `proxi.yaml`. Editing what is public means editing that table and rebuilding.

Machines with no Proxima nodes: `boot` (Prometheus and Grafana only), `loc0`,
`seq1`, `loc1` (spammers and miners).

**Addresses of the non-public machines are deliberately not in this repo.** The
full machine to IP map, together with the box/node setup instruction and the
launch runbook, is in `.internal/operating.md` (gitignored). Never copy a
non-public address into a tracked file.

Sudo user `lunfardo` is used to do all operations on each machine. Claude has
ssh as `lunfardo` and can read logs and query APIs, but has **no sudo**: node
configs and keys are unreadable to it, and starting/stopping services, `ufw` and
backups are the operator's.

On each machine there are 2 nodes configured, each named: 
- `<machine name>` for sequencer node
- `<machine name>-acc` for access node

Logs are accessible to `lunfardo` in `/home/nodes/logs` directory. 
Respective logs are prefixed with the node's name. 
Crash logs are never erased and are prefixed with `crash-`.

Both nodes are configured as `systemd` services.

### Prometheus monitoring

Prometheus runs on `boot`, scraping all 10 nodes every 15s. Retention: 10 days / 10 GB.
Scrape config: `/etc/prometheus/prometheus.yml`, job `proxima`.

**Access**: ssh to `boot` as `lunfardo` (address in `.internal/operating.md`),
then `curl -s 'http://localhost:9090/api/v1/query?query=<METRIC>'`

**Grafana**: port `3000` on `boot`

**Instance mapping** (port 14000 = sequencer, port 14001 = access node):

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
| `proxima_txInputQueue_repeating` | counter | Dedup gate hits: transactions already seen. Exact txid map with TTL, not a bloom filter |
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
| `proxima_lrb_num_seq` | gauge | Distinct sequencers in the past cone of the LRB, read off its stem. Consolidation quality: the sequencer count is the maximum, 1 is a branch which folded in nobody. Not to be confused with `NumSeqTransactions`, the per-branch count of sequencer transactions. |
| `proxima_lrb_chain_inflation_total` | counter | Cumulative chain inflation: LRB supply growth between two samples minus the branch inflation bonus and the mined amount. Bumped from `goLoggingSync` (10s LRB poll) on each advance of the LRB slot. |
| `proxima_lrb_branch_inflation_bonus_total` | counter | Cumulative branch inflation bonus of the branches observed as LRB. Distinct from the `proxima_branch_inflation_bonus` gauge, which is the last branch attached on this node, any lineage. |

Supply growth and the mined amount are exact (absolute values read off the
branch); the branch bonus is per-branch data, so a poll that skips a slot moves
that slot's bonus onto chain inflation.

**Fair-launch mine chain (from the mine chain output in the LRB state):**

| Metric | Type | Description |
|--------|------|-------------|
| `proxima_mine_remaining` | gauge | Remaining mintable amount R on the mine chain |
| `proxima_mine_amount_total` | counter | Cumulative amount mined: the decrease of R observed between LRB samples |
| `proxima_mine_difficulty` | gauge | Difficulty B in bits carried by the mine chain |

Unset on a ledger with no mine chain.

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
go_gc_cycles_total_gc_cycles_total{instance=~"<box IP>:.*"}

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


