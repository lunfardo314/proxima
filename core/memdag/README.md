# memdag

`MemDAG` is the in-memory part of the UTXO tangle: the vertices the node is
currently constructing, validating and building on.

It is **not a mempool.** Proxima has no block proposer, so there is nothing to
select transactions for. The memDAG is a working set, and everything in it is
either already valid or on its way to being decided.

**It is a cache, and it must stay invisible.** Anything derived from the
protocol — coverage, conflict status, mutations — must never depend on what the
memDAG happens to still hold. Vertices are detached and collected continuously
in the background once they are confirmed deep enough, past their TTL, or when
the size backstop fires.

Ordering worth being precise about: a transaction is persisted to the txstore in
`txinput_queue`, **before** it reaches the memDAG — not after validation in the
DAG. That is what makes load shedding safe, since a transaction the node
declines to attach is still on disk and can be pulled back later.

## Read before changing anything here

- [`claude/dag_semantics.md`](../../claude/dag_semantics.md) — **hard
  constraint.** The semantic model of the tangle and the memDAG, including the
  pruning criterion and the flag-monotonicity contract.
- [`../README.md`](../README.md) — how the memDAG sits in the rest of `core`.
- [`../resilience.md`](../resilience.md) — the GC, the TTLs and the size
  backstop as load-shedding gates, with their current values.

Past-cone traversal is lock-free and sound only because a vertex that has
reached `Good` no longer changes. A functional test run passes with that
assumption broken, so **run the relevant tests under `-race`** after any change.
