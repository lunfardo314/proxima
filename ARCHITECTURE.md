# Architecture and orientation

The developer's front door to this repository: what the system is made of, how
the pieces relate, and where everything is documented.

Three landing pages, one each for a different reader:

| Document | Reader | Organised around |
|----------|--------|------------------|
| [`README.md`](README.md) | Someone deciding whether Proxima is interesting | What it claims and why |
| **`ARCHITECTURE.md`** (this file) | A developer about to change something | The system |
| [`CLAUDE.md`](CLAUDE.md) | Claude Code | Working rules and conventions |

Concepts — what a tangle is, why coverage decides consensus, what a sequencer is
for — are on the [docs site](https://lunfardo314.github.io/#/overview/1-what-proxima-is)
and in the [whitepaper](https://arxiv.org/abs/2411.16456). This file assumes
them and describes the *implementation*.

Roughly 80,000 lines of Go, plus 42,000 of tests.

---

## 1. The shape of the system

A Proxima node does four things, and each is a layer:

```
   ┌──────────────────────────────────────────────────────────┐
   │  ledger/          THE RULES                              │
   │  What a valid transaction is. EasyFL constraints, the    │
   │  UTXO model, ledger time, the committed state (trie).    │
   │  Identical on every node — changing it is a hardfork.    │
   └──────────────────────────────────────────────────────────┘
                              ▲
   ┌──────────────────────────────────────────────────────────┐
   │  core/            THE DAG                                │
   │  Turns arriving bytes into a validated in-memory DAG,    │
   │  and at slot boundaries into committed ledger states.    │
   │  Node-local: a cache and a pipeline, not protocol.       │
   └──────────────────────────────────────────────────────────┘
          ▲                                        ▲
   ┌──────────────┐                        ┌───────────────────┐
   │ peering/     │                        │ sequencer/        │
   │ TRANSPORT    │                        │ ISSUANCE          │
   │ libp2p       │                        │ Optional. Decides │
   │ gossip+pull  │                        │ what this node    │
   │ One message  │                        │ puts on the       │
   │ type: a raw  │                        │ network           │
   │ transaction  │                        │                   │
   └──────────────┘                        └───────────────────┘
          ▲                                        ▲
   ┌──────────────────────────────────────────────────────────┐
   │  api/ + proxi/    ACCESS                                 │
   │  REST and WebSocket surface; the CLI wallet, which is    │
   │  modelled as an external wasm wallet and does not share  │
   │  the node's in-process ledger singleton.                 │
   └──────────────────────────────────────────────────────────┘

   node/ owns the lifecycle; global/ carries logging, metrics, context,
   shutdown; util/ is leaf utilities with no Proxima semantics.
```

The division that matters most: **`ledger/` is protocol, `core/` is not.** A
disagreement about `ledger/` splits the network. A disagreement about `core/` is
a local performance or liveness problem. Almost every "is this a hardfork?"
question resolves by asking which side of that line the change falls on.

The second division: **`sequencer/` is optional.** An access node is a full node
— it validates every transaction and holds a valid ledger state — but issues
nothing. A sequencer node is an access node plus the issuance add-on.

## 2. Package map

### `ledger/` — the rules (21 K lines)

| Package | What it is |
|---------|------------|
| `ledger` | The library: locks, constraints, storage deposit, inflation, genesis, the library singleton and its versioned upgrades |
| `ledger/base` | Base types with no dependencies: `TransactionID` (32 B), `OutputID` (33 B), `LedgerTime`, `ChainID`, genesis definitions |
| `ledger/def` | **The constraint layer itself**: `.easyfl` sources and the JSON constant/function definitions compiled into the library. This is where protocol rules are written |
| `ledger/transaction` | The transaction type, parsing (stage 1), the transaction context, validation (stages 2 and 3) |
| `ledger/txbuildercore` | Singleton-free transaction building and parsing. Shared by the node, `proxi` and the wasm wallet |
| `ledger/multistate` | Committed ledger states as overlapping Merkle tries over BadgerDB; branches, roots, snapshots, UTXO indexing |
| `ledger/utxodb` | In-memory ledger state that mimics `multistate`, for unit tests |
| `ledger/tests` | Tests for the ledger and the constraint library |

### `core/` — the DAG (14 K lines)

| Package | What it is |
|---------|------------|
| `core/vertex` | In-memory representations: `WrappedTx`, `Vertex`, `VirtualTx`, the past cone and its status flags |
| `core/memdag` | The in-memory DAG — a cache of the currently relevant part of the tangle. Not a mempool |
| `core/attacher` | Solidification, conflict detection and full validation. One goroutine per sequencer transaction |
| `core/workflow` | The engine: wires the modules together, owns the node-facing entry points |
| `core/core_modules` | The long-running processes (below) |
| `core/txmetadata` | Non-persistent data riding alongside a raw transaction for consistency checks in transit |

`core_modules`: `txinput_queue` (reception, dedup, rate control, gossip),
`txsolicit_queue` (the fast lane for solicited transactions), `pull_tx_server`
(answering peers' pulls), `txstore_writer` (write-behind persistence),
`branches` (branch commit lifecycle and the committed-state index), `tippool`,
`forward_sync`, `snapshot` and `snapshot_restore`, `poker`, `events`,
`txlogger`.

### `sequencer/` — issuance (7 K lines)

| Package | What it is |
|---------|------------|
| `sequencer` | The agent: strategy, slot loop, submission decisions, self-throttling |
| `sequencer/task` | Per-target-timestamp proposal building; the proposer variants (base, bootstrap) |
| `sequencer/backlog` | Tag-along outputs waiting to be consumed — the closest thing to a mempool in the system |
| `sequencer/delegationpool` | Delegations tracked for freezing |
| `sequencer/factory` | Milestone factory and own-milestone bookkeeping |
| `sequencer/txbuilder_seq` | Sequencer-side transaction construction |
| `sequencer/seqdata` | Sequencer chain data helpers |

### The rest

| Package | Lines | What it is |
|---------|-------|------------|
| `peering` | 2 K | libp2p host, Kademlia discovery, gossip and pull protocols, the connectivity map |
| `api` | 8 K | `server` (REST), `client`, `streaming` (WebSocket), `dagviz` and `dag_explorer` (DAG visualisation), `chain_explorer`, `monitor` |
| `proxi` | 17 K | The CLI wallet and node tool. Deliberately built as an **external wasm wallet** — see [§6](#6-proxi-is-not-part-of-the-node) |
| `node` | 1 K | Lifecycle: databases, startup order, API server, shutdown, pprof |
| `global` | 1 K | Logging, metrics, counters, context and shutdown, sync-target state, memory watchdog |
| `txstore` | — | Raw transaction persistence |
| `txlogger` | 0.5 K | Per-transaction event log for post-hoc analysis |
| `util` | 3 K | Leaf utilities: sets, queues, keystore, byte pools, lines, semaphores. No Proxima semantics |
| `tests` | 1 K | In-process multi-node tests, plus Docker network setups |
| `examples` | — | `chess_poc`: the in-tree reference for the typed builder and the singleton |

## 3. Two lifecycles

### A transaction

1. **Reception** — raw bytes arrive in `txinput_queue` from a peer or the API.
   Deduplicated, parsed (stage 1), sender identified and rate-limited, signature
   checked (stage 2).
2. **Persist and relay** — written to the txstore and gossiped onward, *before*
   the node decides whether it has capacity to attach it.
3. **Attachment** — into the memDAG; the past cone (inputs and endorsements) is
   solidified, pulling what is missing from peers. Sequencer transactions get
   their own attacher goroutine and a deterministic baseline branch, which
   defines the UTXO set they are validated against.
4. **Conflict detection** — the attacher checks that no output is spent twice
   anywhere in the past cone.
5. **Validation** — all UTXO constraints evaluated in full context (stage 3).
6. **Commit** — a branch transaction's ledger state is persisted through
   `ledger/multistate`.

Every point along that path where a transaction can be rejected, deferred,
dropped or slowed is in [`core/resilience.md`](core/resilience.md).

### A node

`main.go` → `node.New()` → `Start()`, in this order:

1. `startMetrics` — Prometheus registry
2. `checkAndRestoreOnStartup` — if the database is missing or corrupt, restore
   from the newest snapshot in `snapshot.directory`; refuse to start if there is
   none
3. `initMultiStateLedger` — open the state DB, initialise the ledger library
   from the genesis record and walk the upgrade chain
4. `initTxStore`, `initTxLogger`
5. `initPeering`
6. `startWorkflow` — the core modules
7. `startSequencer` — only if configured
8. `startAPIServer`, then optionally pprof and the memDAG debug API

Shutdown is graceful and ordered: work processes stop, then databases close.
`SIGINT`/`SIGTERM` are treated as intentional and do not write a crash log.

## 4. State: three databases

| Database | Holds | Package |
|----------|-------|---------|
| **multistate** | Committed ledger states as overlapping Merkle tries, one root per branch, plus the branch index | `ledger/multistate` |
| **txstore** | Raw transaction bytes, append-only | `txstore` |
| **txlogger** | Per-transaction event traces, for analysis | `txlogger` |

All three are BadgerDB. The txstore is append-only and a snapshot restore does
*not* touch it, so a restored node has state without the history behind it —
missing transactions were never received and are not back-filled.

The multistate is the only one that is protocol-relevant. The memDAG is a cache
in front of it and must never be load-bearing: anything derived from the
protocol — coverage, conflict status, mutations — must not depend on what the
cache happens to still hold.

## 5. Where the rules live, and how they change

Protocol rules are written in **EasyFL**, a non-Turing-complete functional
language of formulas, in `ledger/def/*.easyfl` plus the definitions in
`ledger/def/*.json`. EasyFL is also the serialisation format: a UTXO *is* a
tuple of byte slices, and its constraints are bytecode in that tuple.

The working rule is **enforce in EasyFL when possible**. Reach for embedded Go
only where EasyFL genuinely cannot do the job efficiently: aggregation across
arbitrary positions in many outputs, arithmetic needing Go-level overflow
handling, per-transaction context caching, or crypto primitives. And the
constraint layer is authoritative: **constraints enforce, transaction builders
follow.** A duplicate assertion in Go is not defence in depth, it is a second
place to get it wrong.

Three consequences worth internalising before touching `ledger/`:

- **Renaming any EasyFL symbol is a hardfork.** Symbol names are hashed into the
  library hash. Source text and comments are not.
- **The library hash gates peering.** It is embedded in the libp2p protocol
  name, so nodes on different ledger versions cannot connect at all — a clean
  partition rather than a stream of mutual validation failures.
- **Upgrades are a chain**, walked at startup. See
  [`ledger/upgrade.md`](ledger/upgrade.md).

## 6. `proxi` is not part of the node

`proxi` is modelled as an **external wasm wallet**. It does not use the
in-process `ledger.L()` singleton to build or display transactions; it fetches
what it needs over the API and holds it in per-process wallet state
(`glb.GetLedgerConstants()`, `glb.GetTxLibrary()`, `client.Eval`).

This is a deliberate architectural constraint, not a style preference: it is
what keeps a browser or mobile wallet possible. The rule for any new `proxi`
command is **never reach for `ledger.L()`**. The exceptions — `db_cmd`,
`chess_cmd`, `snapshot_cmd/check.go`, `util_cmd/inflation.go` — are listed in
CLAUDE.md with the reason each is exempt.

---

# Reference index

## Hard constraints — read before touching the core

These two are binding, not background. Where a change appears to require
contradicting one, that is a signal to stop and raise it.

| Document | Binds |
|----------|-------|
| [`claude/dag_semantics.md`](claude/dag_semantics.md) | The semantic model of the tangle and the memDAG. `core/memdag`, `core/attacher`, `core/vertex`, and all attachment, coverage and pruning logic |
| [`claude/sync_semantics.md`](claude/sync_semantics.md) | How a node catches up. `core/core_modules/forward_sync`, `core/attacher`, `core/workflow`, `sequencer`, `node` |

Both are evolved only with explicit approval from the maintainer.

## Developer documentation

A developer document lives in the package it documents. There is no `docs/`
directory.

**Cross-cutting**

| Document | When to read it |
|----------|-----------------|
| [`core/resilience.md`](core/resilience.md) | Before changing anything that rejects, drops, defers or rate-limits a transaction. Also when a node is shedding load and you need to know which gate is doing it |
| [`ledger/limits.md`](ledger/limits.md) | Any question of the form "how big / how many can this be" |

**Ledger**

| Document | When to read it |
|----------|-----------------|
| [`ledger/def/easyfl.md`](ledger/def/easyfl.md) | Writing or reading a constraint. Covers what is Proxima-specific; the language itself is on the docs site |
| [`ledger/upgrade.md`](ledger/upgrade.md) | Changing the constraint library or its constants |
| [`ledger/multistate/utxo_indexing.md`](ledger/multistate/utxo_indexing.md) | Adding an indexed lookup, or touching the UTXO tuple layout |
| [`ledger/multistate/snapshot_format.md`](ledger/multistate/snapshot_format.md) | Anything reading or writing snapshots |
| [`ledger/txbuildercore/wasm/README.md`](ledger/txbuildercore/wasm/README.md) | Building the wasm wallet target |

**Core**

| Document | When to read it |
|----------|-----------------|
| [`core/README.md`](core/README.md) | First stop in `core`. Package roles, the transaction path, and the traps |
| [`core/memdag/README.md`](core/memdag/README.md) | Working on the in-memory DAG |
| [`core/attacher/README.md`](core/attacher/README.md) | Attachment internals. **Opens "TODO needs review"** — treat it as notes, not as authority; `dag_semantics.md` is the authority |
| [`core/core_modules/forward_sync/sync.md`](core/core_modules/forward_sync/sync.md) | Before changing sync. It deliberately does *not* describe how forward sync works — it points at what does and lists the traps |
| [`core/core_modules/snapshot_restore/snapshot_restore.md`](core/core_modules/snapshot_restore/snapshot_restore.md) | Restore-on-startup and periodic state cleanup |

**Everything else**

| Document | When to read it |
|----------|-----------------|
| [`sequencer/README.md`](sequencer/README.md) | Working on issuance |
| [`peering/README.md`](peering/README.md) | Working on the P2P layer |
| [`peering/network_connectivity.md`](peering/network_connectivity.md) | The connectivity-gossip protocol and the connectivity map |
| [`api/api.md`](api/api.md) | The node API `/api/v1` and the WebSocket surface |
| [`api/txapi.md`](api/txapi.md) | `/txapi/v1` — transaction building and parsing for wallets |
| [`global/logging.md`](global/logging.md) | Logging configuration, levels and trace tags |
| [`txlogger/README.md`](txlogger/README.md) | The per-transaction event log |
| [`tests/README.md`](tests/README.md) | Running the in-process node tests and the Docker networks |
| [`tests/docker/docker-network.md`](tests/docker/docker-network.md) | A small testnet in Docker |
| [`examples/chess_poc/chess_poc.md`](examples/chess_poc/chess_poc.md) | The reference for the typed builder and singleton usage |

## The docs site

Public and user-facing, at <https://lunfardo314.github.io>, published from
`main` of `github.com/lunfardo314/lunfardo314.github.io`.

| Section | What it holds |
|---------|---------------|
| `overview/` | Concepts. A four-page ordered spine — what Proxima is, tokens and supply, taking part, how transactions work — with reference pages behind it on the UTXO ledger, consensus, safety and liveness, delegation and permissionlessness |
| `txdocs/` | The transaction model in technical detail: the tuple format, validation, chains, native tokens, redeemer scripts |
| `ledgerdocs/` | The EasyFL library reference: constraints, chains, locks |
| `multistate/` | Multi-ledger state and the trie |
| `participate/` | The operational guides — running standalone, access and sequencer nodes, node and wallet config, `proxi`, delegation, mining, joining the testnet |

Repository links point at these URLs; the guides do not live in this repo.

## The knowledge base — `claude/`

Design notes, plans, findings and session reports, maintained as an
incrementally-improved record. **Every document opens with a status
blockquote** — `HARD CONSTRAINT`, `LIVE`, `RESEARCH` or `META` — and that line
is more trustworthy than the document's own prose.

The current working set is thirteen documents, indexed with a one-line
description each in [`CLAUDE.md`](CLAUDE.md#claude-index). Beyond the two hard
constraints they cover delegation scalability and freeze distribution,
sequencer conflict resolution, branch fork convergence, inflation, the monitor
spec, and open research (credit tokens, tick duration, forced delegation), plus
two meta documents: `docs.md` and `kb_reorg.md`.

`claude/archive/` holds 87 documents that no longer describe current work, in
three buckets, each with a `README.md` indexing every file:

| Bucket | Holds | Read it for |
|--------|-------|-------------|
| `archive/incidents/` | One event, one date, resolved | Whether a symptom has been seen before. Its index also lists issues recorded and never closed |
| `archive/shipped/` | Specs for features now on `develop`. **The code is the truth** | Why an alternative was rejected — the one thing code cannot record |
| `archive/superseded/` | Overtaken, shelved, or never taken up. **Nothing here is a plan** | Why an approach was *not* taken |

## External

| Resource | What it is |
|----------|------------|
| [Whitepaper](https://arxiv.org/abs/2411.16456) | The cooperative consensus concept in full |
| [`lunfardo314/easyfl`](https://github.com/lunfardo314/easyfl) | The constraint language and its serialisation format |
| [`lunfardo314/unitrie`](https://github.com/lunfardo314/unitrie) | The trie and Merkle structures under `multistate` |
| libp2p, BadgerDB, cobra/viper | Networking, storage, CLI and configuration |

---

# Reading paths

Rather than reading top to bottom, start from what you are about to do.

| I want to… | Read, in order |
|------------|----------------|
| **Change a validity rule** | `ledger/def/easyfl.md` → the `.easyfl` source → `ledger/upgrade.md`. Assume hardfork; ask whether backward compatibility is required before adding any |
| **Touch attachment, the memDAG or coverage** | `claude/dag_semantics.md` (binding) → `core/README.md` → the package. Then run the tests **under `-race`** |
| **Change sync** | `claude/sync_semantics.md` (binding) → `core/core_modules/forward_sync/sync.md` → `core/resilience.md` §5 and §14 |
| **Change anything that drops or limits transactions** | `core/resilience.md` end to end |
| **Add or change an API endpoint** | `api/api.md` or `api/txapi.md` → `api/server/` |
| **Add a `proxi` command** | The wasm-wallet section of `CLAUDE.md` → the canonical templates it names in `proxi/node_cmd/` |
| **Work on the sequencer** | `sequencer/README.md` → `core/resilience.md` §13 for the throttling it applies to itself |
| **Add an indexed lookup or change the UTXO layout** | `ledger/multistate/utxo_indexing.md` → the UTXO tuple layout table in `CLAUDE.md` |
| **Investigate a live incident** | `claude/archive/incidents/README.md` first — check whether the symptom is known. Then Prometheus and the node logs |
| **Understand why something was built this way** | `claude/archive/shipped/README.md` — the rejected alternatives are recorded nowhere else |

# Traps

Things that have caused real bugs, collected so they are met before rather than
after. Each is expanded in the document named.

- **"Good ⇒ immutable."** Past-cone traversal is lock-free and only sound
  because a vertex that reached `Good` no longer changes. A functional test run
  passes with this assumption broken — the races are benign-by-monotonicity on
  x86. **Run `-race` after any core change.** (`core/README.md`)
- **Pruning is cache policy and must be invisible.** Never let a protocol-derived
  value depend on what the memDAG still holds. (`dag_semantics.md`)
- **Consumer information is never cleared**, including on detach. Conflict
  detection, mutation generation and cone cleanup all depend on it.
- **Branch values are read, not recomputed.** Coverage delta, supply and the
  other consolidated values of a committed branch are authoritative in
  `branches` and in persisted state. Re-deriving them by walking history is a
  bug.
- **Renaming an EasyFL symbol is a hardfork** — names are hashed into the
  library hash.
- **One signature per transaction** is a protocol invariant, not a limitation to
  work around. It is what makes holder-ID rate limiting and tag-along
  attribution possible.
- **Logs are not the DAG.** They show events in submission order, not the actual
  input, endorsement and chain relationships. Read the raw transaction from the
  txstore (`proxi db txstore get`, or the `dagviz` APIs) before concluding
  anything about topology. Inferring successor relationships from log ordering
  has produced wrong analyses before.
- **`ledger.TimeNow()` in tests** races the ledger state. Derive test timestamps
  from actual output timestamps instead.
