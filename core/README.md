# core

`core` turns raw transaction bytes arriving from peers and the API into a
validated in-memory DAG, and — at slot boundaries — into committed ledger
states.

## Read this first

Two documents are **hard constraints** on anything in here, not background
reading. Code in `core` must be consistent with them; where a change appears to
require contradicting one, that is a signal to stop and raise it, not to bend
the model to the code.

* [`claude/dag_semantics.md`](../claude/dag_semantics.md) — the semantic model of
  the transaction DAG (the tangle) and the memDAG. Binds `memdag`, `attacher`,
  `vertex`, and all attachment, coverage and pruning logic.
* [`claude/sync_semantics.md`](../claude/sync_semantics.md) — how a node catches
  up with the network. Binds `core_modules/forward_sync`, `attacher`,
  `workflow`.

Both are evolved only with explicit user approval.

## The packages

| Package | What it is |
|---------|------------|
| `vertex` | In-memory representations of a transaction: `WrappedTx`, `Vertex`, `VirtualTx`. Also the past cone and its status flags. |
| `memdag` | The in-memory DAG itself — a cache of the part of the tangle relevant to the current time window. Not a mempool: there is no block proposer to feed. |
| `attacher` | Solidifies and validates. One attacher goroutine per sequencer transaction. |
| `workflow` | The engine: wires the modules together and owns the node-facing entry points. |
| `core_modules` | The permanent processes — see below. |
| `txmetadata` | Optional data attached to a raw transaction for consistency checking in transit. |

`core_modules` holds the long-running processes: `txinput_queue` (reception,
deduplication, rate control, gossip), `txsolicit_queue` and `pull_tx_server`
(asking peers for transactions, and answering), `txstore_writer`, `branches`
(branch commit lifecycle and the committed-state index), `tippool`,
`forward_sync`, `snapshot` and `snapshot_restore`, `poker`, `events`, and
`txlogger`.

## How a transaction gets in

1. **Reception.** Raw bytes arrive in `txinput_queue` from a peer or the API. A
   TTL'd map of seen transaction IDs drops repeats; the transaction ID is parsed. This is where a
   data blob either becomes a transaction or is discarded.
2. **Sender and rate control.** The signature is parsed and checked, giving the
   **holder ID** — and because every transaction has exactly one signature, that
   identifies the sender unambiguously. `txinput_queue` keeps per-holder
   timestamps (`txSenders`) and applies rate limits against them.
3. **Attachment.** The transaction goes into the memDAG, and its past cone —
   inputs and endorsements — is solidified. Sequencer transactions get their own
   attacher goroutine. Every sequencer transaction has a deterministically
   defined baseline branch, and that baseline is what defines the UTXO set the
   transaction is validated against.
4. **Conflict detection.** The attacher checks that no output is spent twice
   anywhere in the past cone. What exactly is tested, and the flag-monotonicity
   contract it rests on, is in `dag_semantics.md`.
5. **Validation.** All UTXO constraints of the transaction are evaluated against
   the full context.
6. **Commit.** A branch transaction's ledger state is persisted through
   `ledger/multistate`.

How the node survives hostile or excessive load along that path — the threat
model, the layers that answer it, the order in which the node gives things up as
pressure rises, how it recovers afterwards, and a reference inventory of every
gate — is in [`resilience.md`](resilience.md). It is also the place to start when
a node is shedding load and you need to know which gate is doing it.

## Things that bite

**"Good ⇒ immutable."** Past-cone traversal is lock-free, and that is only sound
because a vertex that has reached `Good` no longer changes. A functional test
run will pass with this assumption broken — the races are benign-by-monotonicity
on x86 — so **run the relevant tests under `-race` after any change here**. A
clean functional run is not evidence.

**Pruning is cache policy, and must be invisible.** Anything derived from the
protocol — coverage, conflict status, mutations — must not depend on what the
cache happens to still hold. The criterion for dropping a vertex is "rooted and
not consumed by a not-rooted transaction", never an age or a TTL.

**Consumer information is never cleared**, including on detach. Conflict
detection, mutation generation and cone cleanup all depend on it.

**Branch values are read, not recomputed.** Coverage delta, supply and the other
consolidated values of a committed branch are authoritative in the `branches`
module and persisted state. Re-deriving them by walking history is a bug.

**Cross-check before trusting a change** to anything feeding coverage,
mutations, or frozen computations: compute it both ways, assert equality, and
validate on an access node under live load before removing the check.
