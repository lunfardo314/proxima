# DAG semantics — authoritative model and constraint for core development

Status: living document. First draft 2026-06-16.

## What this document is

This is the **authoritative semantic model** of the Proxima transaction DAG and
its in-memory cache (the memDAG), and a **hard constraint on any change to the
core** — `core/memdag`, `core/attacher`, `core/vertex`, and the attachment /
coverage / pruning logic they contain. Code in those areas MUST be consistent
with the semantics described here. When a change appears to require violating a
statement below, that is a signal to stop and raise it with the user, not to
bend the model to the code.

Rules for this document:

- It is a **constraint for Claude**, first and foremost. Read it before touching
  the core; keep edits to the core consistent with it.
- It **leads on intended semantics**: where the code conflicts with the intended
  semantics, the code is wrong and must change. But the code is essentially
  consistent with those semantics already (except known drifts), so where this
  document has merely drifted from correct, established behaviour, the *document*
  is corrected instead — the code is never bent to a wrong doc.
- It is **general and implementation-independent**. It describes properties and
  functional behaviour, not data structures or algorithms, except where a named
  structure is itself part of the contract. It rarely descends to code detail.
- It is **evolving**. Semantic constraints will be added and refined over time —
  but **only after the user approves the change to this document**. Do not
  silently restate the code here; this document leads, the code follows.
- It is kept **reasonably short**. Detail, incident write-ups, and fix plans live
  in their own `claude/*.md` files and link here, not the other way round.

It has two perspectives, in order of authority:

1. **Transaction DAG (the tangle)** — implementation-independent. Defined by
   transactions alone. This is the protocol truth; everything else must agree
   with it.
2. **memDAG** — a dynamically maintained cache of the part of the tangle relevant
   to the current time window. The most complex part of the node. Its job is to
   serve the DAG semantics faithfully and cheaply; it may never contradict them.

---

## 1. Transaction DAG (the tangle) perspective

The tangle is a directed acyclic graph whose **vertices are UTXO transactions**
and whose **edges are output-consumption links and endorsements**. It is also
called the UTXO tangle or the transaction DAG. It is defined by transactions
only — independent of any node, cache, or wall-clock time.

### 1.1 Transactions and ledger states

- A transaction consumes outputs, deletes them, and produces new outputs.
- Each transaction `T` deterministically represents **one consistent ledger
  state `S_T`** — the result of applying `T`'s entire past cone to the genesis
  state. This one-vertex-one-state correspondence is the hallmark of the model.
- _ledger state_ and _UTXO set_ are synonyms.
- A transaction is **immutable and deterministic**: inputs and endorsements are
  pre-ordered by fixed indices, so every node derives the identical history. The
  transaction ID commits to the transaction essence.
- Each vertex in the tangle is a **valid** transaction. Validity is a local,
  deterministic property of the transaction: a predicate over the transaction
  bytes and the UTXOs it consumes. The only global input to validation is the set
  of validation rules (constraints), immutably defined mostly in EasyFL plus
  limited Go code.
- The validation rules are designed to support cooperative consensus and the
  efficiency of the memDAG (below).

### 1.2 Past cone and baseline

- The **past cone** of `T` is all transactions reachable from `T` backward along
  consumption and endorsement edges, down to a **baseline** ledger state.
- The tangle is constructed incrementally, transaction by transaction. Each newly
  added transaction becomes a tip of its past cone.
- `T` cryptographically commits to its past cone: **the past cone is deterministic
  and immutable**. The same completed past cone is identical for every participant,
  regardless of how or when they assembled it.
- Any past cone of the tangle is **conflict-free**: it does not contain two
  transactions that directly or indirectly consume the same output (a
  double-spend). This is the definition of ledger consistency and an essential
  property of the DAG maintained by the system. It is the node's responsibility to
  keep every past cone conflict-free; violating this is a major inconsistency and
  the system cannot proceed.
- Two transactions on the tangle may still conflict with each other; they simply
  cannot both belong to one past cone. The two conflicting transactions then
  belong to two different past cones with different tips.
- A past cone is a consistent ledger of transactions with a deterministically
  defined ledger state.
- In general, the cost of finding a conflict in a past cone of the DAG is
  _unbounded_: a double-spend may lie at any distance back to the genesis state,
  i.e. it grows with the tangle. This general property is very inconvenient for
  the implementation and can be abused for attacks.

### 1.3 Sequencer and branch transactions. Baseline

The ledger timeline is divided into **slots**, and slots into **ticks**.
Purposefully designed transaction validity constraints bound the conflict-search
traversal distance:

* Some transactions are marked as **sequencer transactions** (a sequencer
  transaction is also called a **milestone**). They form **chains** within a past
  cone. Only sequencer transactions can endorse others, and only within the same
  slot — cross-slot endorsements are not possible.
* Some sequencer transactions are **branch transactions**. A branch transaction
  sits on the _slot edge_.
* A branch transaction always consumes the **stem output** of the predecessor
  branch transaction on some previous slot edge. This makes all branch
  transactions on the same slot edge mutually conflicting — intentionally. Hence
  each past cone contains a single sequence of branch transactions back to
  genesis.
* Branch transactions form a tree along their stem links. Within any one past cone
  they reduce to a single linear chain, so any two branch transactions in that cone
  always belong to the same lineage.
* The _ledger state_ corresponding to a branch transaction is committed to the DB
  as a separate object identified by the branch transaction ID. Checking whether a
  given UTXO belongs to that state is ~O(1).
* Each valid sequencer transaction has exactly one deterministically defined
  **baseline** (or **baseline state**): a branch transaction. The local validity
  rules enforce this by requiring a defined **baseline direction**.
* The _baseline direction_ of a sequencer transaction `T` is one of: (a) a branch
  transaction consumed by `T`; (b) the sequencer transaction endorsed by `T` at
  index 0; or (c) an **explicit baseline** declared by the transaction. There are
  no other options — the baseline direction is always defined for a sequencer
  transaction.
* The **baseline state** is thus **implicitly defined** for any past cone of a
  sequencer transaction by the _baseline direction_ rule. It guarantees that in a
  bounded (finite) number of baseline-direction steps (usually 1–2) the walk
  reaches a branch transaction — the _baseline branch_ — with committed state.
* The baseline branch is usually in the same slot as the sequencer transaction;
  the explicit-baseline case is a rare exception. The genesis state is the
  baseline of the *full* past cone.

### 1.4 Rooted outputs and the baseline frontier

- Relative to a chosen baseline state, an output (or the transaction producing it)
  is **rooted** when it belongs to that baseline state — i.e. it is part of the
  committed UTXO set the baseline represents.
- Walking `T`'s past cone toward the baseline, the walk **terminates at rooted
  vertices**: a rooted vertex's own history is already folded into the baseline,
  so there is nothing further to descend. The set of rooted vertices forms the
  **baseline frontier** of the cone.
- The **attachment budget** is a global constant capping the number of
  transactions and UTXOs in a sequencer transaction's past cone down to its
  baseline frontier. It is computed deterministically from the tangle, so a
  transaction with too heavy a past cone cannot appear.
- The bounded distance from any sequencer transaction to its baseline, together
  with the attachment budget, caps the worst-case cost of traversing the past cone
  of any sequencer transaction down to the baseline.
- Rootedness of non-sequencer transactions is **baseline-relative and
  sequencer-subjective**: the same non-sequencer vertex may be rooted for one
  transaction's baseline and not-rooted (still in the live delta) for another's.
- The portion of the past cone **above** the frontier — the not-rooted vertices —
  is the transaction's **delta**: the new history `T` adds on top of its baseline.

### 1.5 Ledger coverage and consensus

- The **ledger coverage delta** of `T` is the sum of amounts of all baseline-state
  outputs consumed by `T`'s past cone — how broadly `T` covers its baseline. (The
  sequencer output of the baseline branch is always included in the sum.)
- The **ledger coverage** (or **coverage**) of `T` is computed across the unique
  branch chain as a slot-weighted sum, with weights decaying into the past.
- Coverage is **deterministic over the real DAG** for each sequencer transaction
  `T`. The termination and boundedness of the past-cone walk at the baseline
  frontier are exactly what make the computed coverage well-defined and identical
  for every node — *given the same transactions in the past cone*.
- Consensus is the **biggest ledger coverage rule**: token holders, via
  sequencers, gravitate to the ledger-state delta with the largest coverage.
  Conflicts are resolved implicitly — the conflicting branch/transaction with
  smaller coverage is **orphaned**. There is no separate ordering or voting step.

### 1.6 Invariants the implementation must preserve

These follow from §1.1–§1.5 and bind every core change:

- **D1 — Determinism.** Any value the protocol derives from a transaction (past
  cone membership, conflict status, coverage, mutations) depends only on the
  transactions and the chosen baseline, never on cache state, wall-clock time, or
  the order/fragment in which a node happened to learn the transactions. (The
  protocol value is always deterministic; a node's *local enforcement* of it is
  relaxed only in the documented snapshot-boundary window — §2.4.)
- **D2 — Walk terminates at the baseline frontier.** Attachment must descend the
  not-rooted delta until every branch of the walk reaches a rooted vertex, *for
  any baseline, at any depth, regardless of the vertices' timestamps*. Shrinking
  what is *retained* is allowed; shortening or skewing the *walk* is not.
- **D3 — Conflict-freeness.** A transaction is valid only if its past cone is
  conflict-free; the implementation must detect any double-spend within the cone.
- **D4 — Branch finality.** A branch is committed state. Its rootedness and its
  consolidated values (coverage delta, supply, frozen coverage, …) are authoritative
  in persisted state / the `branches` module, and must be read from there rather
  than re-derived by walking the branch's history.

---

## 2. memDAG perspective

The memDAG is the node's **in-memory cache of the part of the tangle relevant to
the current time window**. It is volatile, asynchronous, and per-node: different
nodes hold different fragments at different moments. Its purpose is to serve the
DAG semantics of §1 efficiently; it is never an alternative source of truth.

### 2.1 What the memDAG is

- A set of in-memory transaction representations (vertices), cached so the node can
  attach new transactions and compute coverage and mutations without re-reading the
  store. A vertex may be fully known or still **virtual** — referenced by others but
  with its own transaction not yet received.
- It is **continuously pruned** by time so it stays bounded. Pruning is a cache
  policy and is **invisible to the protocol** (D1): the backing transaction store
  (`txstore`) and the persisted branch states are the durable record, and anything
  the memDAG drops can be reconstructed from them when needed.
- Vertices are held through weak references plus targeted strong references, so a
  vertex with no live need is reclaimed automatically.

### 2.2 The past cone as a cache of the walk

- During attachment of a sequencer transaction, the **past cone** (`PastCone`) is
  the working, compressed cache of the §1.2 walk: the not-rooted **delta** plus the
  part of the **baseline frontier** (§1.4) that the delta consumes. It exists to
  make conflict detection (D3) and mutation generation cheap and deterministic (D1).
- The past cone must hold **exactly what those computations need**, no more:
  - **Must keep:** the not-rooted delta, and every rooted vertex with a UTXO
    **consumed by a not-rooted transaction** — the **consumed boundary**, which
    feeds coverage and mutations.
  - **May drop:** any vertex already rooted in the baseline that is **not** consumed
    by a not-rooted transaction. It contributes nothing to the delta, coverage, or
    mutations. Older baseline-ancestor branches are exactly this category.
- The criterion for "safe to drop" is **"rooted and not consumed by a not-rooted
  transaction"** — never "older than slot X" and never a merge-relative in-state
  flag. Dropping by timestamp or by a stale flag removes consumed-boundary vertices
  and corrupts coverage; this has caused real regressions (see the working docs in
  §2.7).
- **A vertex keeps its consumer information for its whole lifetime.** Each vertex
  records which transactions consume its outputs — information gathered as the DAG is
  built. The past cone relies on it to know what the not-rooted delta consumes, which
  is the basis for conflict detection (D3), mutation generation, and the "safe to
  drop" criterion above. It MUST NOT be discarded while the vertex exists — in
  particular **not when the vertex is detached**. A detached vertex left in a cone
  without its consumer information looks unconsumed, so cleanup wrongly drops it and a
  needed mutation is lost. This information can never be a leak source: it points
  *forward* to newer consumers, and pruning is oldest-first, so a vertex is always
  reclaimed no later than the consumers it points to.

### 2.3 Branches in the memDAG

- A branch vertex in the memDAG carries **no past cone**. Its history is committed
  state (§1.3, D4); retaining a clone of its cone serves nothing and pins the entire
  ancestor history in memory.
- A branch's consolidated values are read from the **`branches` module / persisted
  state**, never stored on or recomputed from the vertex.
- A finalized rooted branch sitting inside another cone is harmless dead weight: it
  carries no onward references, so it need not be force-removed for correctness —
  only cleaned when it contributes nothing.

### 2.4 Snapshot

A **snapshot** is a serialized **committed ledger state** of one branch — the full
UTXO set at that branch, plus the ledger identity and the upgrade-library chain.
It is the unit of state recovery, DB compaction, network bootstrap, and genesis
distribution. A node that has no DB (or a corrupted one) starts from the latest
snapshot instead of replaying from genesis.

Base facts and properties:

- A snapshot is identified by, and corresponds to, a single **branch transaction**
  (the _snapshot branch_, `branches.SnapshotBranchID`). Its slot is the
  _snapshot slot_. Everything in the snapshot is final, committed state.
- The snapshot is a **static floor**: the part of the tangle at and below the
  snapshot slot is fixed committed state; the memDAG above it is the dynamic,
  pruned cache. The two meet at the static/dynamic boundary (§2.5).
- **Rootedness against the snapshot is presence in the snapshot state, never age.**
  Ledger time is not a criterion: a transaction is rooted iff its output is present
  in the snapshot UTXO set (a finite trie lookup). An older (lower ledger-time)
  transaction that is *not* in the snapshot state is **not** rooted-via-snapshot.
- **TxID-TTL caveat.** A transaction's *txID record* may be garbage-collected from
  the trie once it is old enough relative to the snapshot, even though its effects
  were committed. So a presence lookup can return "absent" for a legitimately
  committed, very old transaction. The node closes this gap with a bounded
  heuristic: a transaction older than the snapshot by more than the txID TTL is
  treated as known-to-the-snapshot. This is **not** age becoming a rootedness
  criterion — it only covers the case where the txID *entry itself* expired; a
  forged old transaction is still rejected by constraint validation.
- A node may still receive (gossip/pull) transactions with timestamp **below** the
  snapshot slot. These do not break determinism — their effects are baked into the
  snapshot UTXO set — but they are a rich edge-case source at the boundary.
- **Edge case — pre-snapshot baseline.** A sequencer transaction that is not in the
  snapshot state yet is older than it has a baseline branch from *before* the
  snapshot. Solidifying *this particular* sequencer transaction terminates its walk
  at its baseline branch and never traverses beyond it — so the pre-snapshot
  baseline's own past cone is not needed here. (Solidifying branch past cones in
  recursive syncing is a separate process; the not-traversing-beyond-the-branch
  rule is about attaching a given sequencer transaction.)
- For the consistency check, all the node needs is the **branch ID** of the
  pre-snapshot baseline branch — to verify it belongs to the **same lineage as the
  snapshot**. It does not need that branch's record, and the check is decidable
  from the snapshot state alone.
- **Relaxed determinism near the snapshot.** Pre-snapshot branches may be absent
  from the DB entirely. Their absence limits the node's ability to compute certain
  global values: the **coverage delta** needs the balance of the sequencer output
  on the baseline branch, which is unavailable when that branch record is gone.
  The node must recognize this edge case and **relax enforcement of determinism
  (D1) for the single boundary slot** — milestones whose baseline branch is *older
  than the snapshot*. Since full coverage depends on the delta, full-coverage
  enforcement is relaxed the same way. It is one slot wide, not a counted window:
  the snapshot branch's own sequencer-output balance is part of the snapshot UTXO
  set, so every milestone from the next branch onward (baseline = the snapshot
  branch) has the data it needs and is enforced normally. In code the relaxation
  is keyed to baseline availability (baseline slot not older than the milestone /
  baseline branch record absent), not to a slot counter.

### 2.5 Pruning and the static/dynamic boundary

- Pruning removes vertices by **wall-clock age** and **ledger-time age**, and
  reclaims deep-confirmed (rooted) history. Because rootedness is baseline-relative
  (§1.4), age-based pruning can in principle evict a not-rooted fragment that some
  live transaction's walk still needs — the tension between a bounded cache and a
  possibly-deep not-rooted delta is the core difficulty of this subsystem.
- The boundary between the static snapshot (§2.4) and the dynamic cache — where
  rooting determination, eviction, and asynchronous arrival all meet — is the most
  edge-case-prone region.
- Constraint: pruning and snapshot handling must never make the node compute over
  an **incomplete or stale** fragment of a live delta. If the data a deterministic
  computation needs is not in the cache, the node must reconstruct it from the
  durable store / refuse to finalize — not silently compute a wrong value.

### 2.6 Standing constraints for memDAG / attacher changes

- **M1.** Pruning, GC, and past-cone trimming are cache policy and MUST be
  invisible to protocol-derived values (D1). If a change alters a coverage,
  conflict, or mutation result, it is wrong by construction.
- **M2.** Never shorten or reorder the attachment walk to bound memory. Bound what
  is *retained*, using the §2.2 "rooted and not consumed by a not-rooted tx"
  criterion. (Compare: D2.)
- **M3.** Branches hold no cone and no recomputed coverage; read consolidated
  branch values from the `branches` module / persisted state (D4, §2.3).
- **M4.** A vertex keeps its consumer information for its whole lifetime; never
  clear it, including on detach (§2.2). Conflict detection, mutation generation, and
  cone cleanup depend on it, and it cannot be a leak source (it points forward to
  newer consumers, reclaimed oldest-first). Past memDAG growth came from retaining
  *rooted, non-contributing* vertices in cones (§2.2–§2.3), not from consumer
  information.
- **M5.** When changing anything that feeds coverage / mutations / frozen
  computations, add a **temporary equality cross-check** (old way vs new way,
  assert equal) and validate on an access node before trusting it. Determinism
  violations here are fatal to consensus.
- **M6.** Do not introduce caches of external mutable state or reference counting
  to manage cache lifetime; prefer recomputing at the read site from authoritative
  state. (General project rule, load-bearing here.)

### 2.7 Known open tension

The cache is bounded by age, yet a transaction's not-rooted delta may legitimately
be deep — most acutely during catch-up against an old snapshot floor. Reconciling
the two is an unresolved policy decision, to be **made explicitly with the user**,
not patched silently.

The hard part is that two scenarios look alike but want opposite handling: a
whole-network cold start on **emptiness** versus catching up on an already **live**
network. So the prerequisite to any mitigation is **detecting which one it is** —
either an operator's decision, or the node **waiting for gossip a while** and
inferring liveness from whether fresh activity arrives. The two directions below
then map one to each scenario:

- **Cold start on emptiness — age the cache by wall-clock, not ledger time.** The
  gap between an old snapshot slot and the present is mostly empty, so few vertices
  fall in it. Aging a vertex by *when it entered the cache* (recently, during
  catch-up) rather than by its old ledger time keeps the freshly-received not-rooted
  delta alive while it is still needed.
- **Live network — do not cold-start from an old snapshot at all.** Prefer to
  **find a younger snapshot in the network and cold-start from that**; failing that,
  **refuse to sync from a snapshot older than a bound** (e.g. ~500 slots). To switch
  safely, require the younger snapshot's state to **contain the older snapshot's
  branch ID** — proving the two share a lineage.
