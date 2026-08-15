# Mining transaction streaming — plan and spec

Status: **IMPLEMENTED.** Written 2026-07-19 as the response to the
winner-take-all bias documented in [`mining-bias.md`](mining-bias.md).
Shipped: `api/streaming/mining_tx_server.go` (node-side stream, with
`mining_tx_server_test.go` covering delivery, fan-out and capacity refusal),
`proxi/node_cmd/mine_stream.go` (miner subscription), `mine_tree.go` +
`mine_verify.go` (speculative transit tree, every entry re-verified from raw
bytes against its predecessor), and the `--stream` / `--no-stream` flags on
`proxi node mine` — the stream is on by default and accepts several endpoints,
so withholding by any single node is ineffective.

Goal: give every miner the same low-latency view of mine-chain transits, so a
height is decided by work rather than by who produced the predecessor. **No
ledger constraint changes.**

## 1. Two corrections to the original premise

The plan assumed mining txs are validated and streamed only when a sequencer
picks them up via tag-along, costing "up to one ledger tick". Both halves are
wrong, one favourably and one unfavourably.

### 1a. Arrival is observable immediately — no tag-along wait (good)

`processValidated` (`core/core_modules/txinput_queue/txinput_queue.go:236`)
runs, in this order, on **every** node that receives the bytes:

1. sender pace control — *mining txs are exempt* (`txinput_queue.go:446`)
2. timestamp upper bound
3. `tx.ValidatePartialContext(true)` — structure + signature (stage 1+2)
4. `MustPersistTxBytes(tx)` — **txstore write**
5. `GossipTxBytesToPeers(...)` — **relayed onward**
6. `shouldAttach` gate → attach to memDAG

Steps 4 and 5 happen *before* the attach gate. So the earliest universal
observation point is right after step 3, at gossip speed — no sequencer
involvement at all.

### 1b. The dagviz hook does NOT fire for mining txs on an access node (bad)

The obvious reuse — `EventNewVertex`, which `api/streaming/dag_vertex_server.go:235`
subscribes to — is posted from `core/attacher/attach.go:268`, i.e. *after* the
attach gate. And `shouldAttachNonSeq` (`txinput_queue.go:~355`) reads:

```go
seqID := q.GetOwnSequencerID()
if seqID == nil {
    q.IncCounter("nonseq_drop")   // access node: drop ALL unsolicited non-seq
    return false
}
if tx.HasOutputForSequencer(*seqID) { return true }
q.IncCounter("nonseq_drop")
return false
```

An access node runs no sequencer, so **every unsolicited mining tx is dropped
before attachment** and `EventNewVertex` never fires. A sequencer node attaches
it only if it tag-alongs to *that* sequencer. The testnet miners all point at
access nodes (`api.endpoint: …:8001`), so reusing the dagviz hook as-is would
stream them nothing.

**Consequence:** the hook must be a new event posted from `processValidated`,
not `EventNewVertex`.

## 2. The trust decision: raw bytes, not JSON

The plan leaned toward parsed JSON on the grounds that it needs trust in the
node but is much simpler. The first half is right and is disqualifying.

**A mining tx is never stage-3 validated by the streaming node.** Constraint
execution happens only in `finalTouchNonSequencer`
(`core/attacher/attacher.go:346`), reached exclusively from a *sequencer's*
attacher walking its past cone. So at stream time the node has checked
structure and signature and nothing else. In particular **the PoW is
unverified** — `_minePaceAndPoW` is in the consumed arm of `mineLock`, which is
stage-3 only.

So "trust the node" does not mean trusting a validator. It means trusting an
unvalidated relay, and it opens attack **A1** below, which is free to mount.

**Decision: stream raw transaction bytes; the miner verifies locally.**

This is cheaper than it sounds, because everything `mineLock` enforces is
checkable client-side from the raw bytes plus the predecessor output the miner
already tracks. The miner computes all of it today in `buildTemplate`, in the
forward direction; the verifier is the same arithmetic in the check direction.

### Client-side verification (complete)

Given candidate `txBytes` and a known predecessor tip (`oid`, output bytes,
`MineLockView`, `ChainConstraintView`, balance):

| # | Check | Source |
|---|-------|--------|
| 1 | parses; `NumInputs()==1`, `NumProducedOutputs()==3` | `transaction.ParseLibraryAgnostic` |
| 2 | `InputAt(0) == pred.oid` | builds on the tip we know — rejects all forgeries not on the real chain |
| 3 | input commitment `== blake2b(pred output bytes)` | we hold the predecessor bytes |
| 4 | produced[0] chain: `ChainID==MineChainID`, `TransitionCounter==pred+1`, `CumInflation==pred+A` | `lib.ParseChainConstraint` |
| 5 | produced[0] mineLock: `R==pred.R-A`; ring `s1'==pred.slot, s2'==pred.S1, s3'==pred.S2` | `lib.ParseMineLock` |
| 6 | `B' == MineAdjustedB(pred.B, pred.S3, succSlot)` | existing `Constants.MineAdjustedB` |
| 7 | amounts: balance `== pred.balance`, inflation `== A` | `DecodeTokenBalance` |
| 8 | pace: `succSlot - pred.slot >= MineMinPace` | |
| 9 | **PoW: `trailingZeroBits(blake2b(txBytes)) >= pred.B`** | existing `trailingZeroBits` |
| 10 | produced[1] sigLock, produced[2] tagAlong, `fee*100 <= A` | optional, cheap |
| 11 | signature valid | ed25519 verify, ~50 µs; node already did it |

That is full local verification of mine-chain semantics. The miner then trusts
the node for **liveness only** (not withholding), which is the correct light-client
trust model and is addressed by subscribing to several nodes.

JSON stays useful as a *debug/observer* format — but the miner must not steer on it.

## 3. Attack surface

| id | Attack | Cost | Mitigation |
|----|--------|------|------------|
| **A1** | **Fake-PoW flood.** Forge txs that satisfy `IsMiningTransaction()` (non-seq, 1 input, 3 outputs, produced[0] chain constraint carrying `MineChainID` — all attacker-chosen data, no need to consume the real mine UTXO). They pass stage 1+2, are persisted and **gossiped network-wide**, and are **exempt from sender pace control**. Every JSON-trusting miner is steered onto a fabricated chain. | ~zero | Checks 2+9. Decisive reason for raw bytes. |
| **A2** | Valid-PoW spam at many heights, widening the tree. | real PoW per transit | Bounded tree + TTL (§4). Attacker gains nothing beyond normal mining. |
| **A3** | **Eclipse / withholding.** The miner's node delays or withholds a competitor's transit, restoring the asymmetry. Undetectable from inside. | requires controlling the miner's node | Subscribe to N independent nodes; union the streams. LRB cross-check bounds the damage. |
| **A4** | **Tie-break grinding.** Any deterministic content-based tie-break is grindable: at low difficulty a miner finds many solutions per pace and picks the most favourable. | proportional to excess hashrate | Use a *work-weighted* tie-break (§4) so winning ties costs work. |
| **A5** | Stream DoS on the node (many connections, slow readers). | low | Reuse the dagviz pattern: connection cap, per-write deadline, TTL, origin check. |
| **A6** | **Selfish mining** — withhold a solved transit, mine privately, release a longer branch. Streaming does not prevent this. | needs large hashrate share | Weak here: the ledger's conflict resolution favours what sequencers saw first, so a privately-held branch usually loses. Accept as residual. |

**Pre-existing issue surfaced by this work (independent of streaming):**
`IsMiningTransaction()` grants a sender-pace-control exemption based on
unvalidated, trivially forgeable structure. That is an unmetered gossip channel
for anyone. Worth a separate look; it is what makes A1 free.

## 4. Miner-side design

### Tree

- Nodes keyed by txid; each holds parsed tip data + parent txid + height
  (`chain.TransitionCounter`) + PoW zero-count.
- Root = the LRB-confirmed mine chain tip.
- Insert only after full verification (§2) against an *already-present* parent.
  Out-of-order arrivals go to a small buffer, retried on each insert; dropped
  after a short TTL.
- The miner inserts **its own** submitted transit through the same path.

### Best-chain selection

"Longest wrt chain steps" = highest `TransitionCounter`, since the chain
constraint forces exactly +1 per transit. Ties need a rule, and the choice
matters more than it looks:

- **First-seen is wrong here.** You always see your own transit first, so
  first-seen means always preferring your own branch — which is precisely the
  ratchet, reintroduced client-side.
- **Lowest-txid is grindable** (A4).
- **Shipped: most trailing zero bits, then bigger tag-along fee, then lowest
  txid.** Winning a tie on work costs *work* (each extra bit doubles it) rather
  than being free or a latency artifact. It converges — all honest miners pick
  the same branch — and degrades gracefully: excess hashrate wins ties in
  proportion to work, the fairness property we want. The tag-along fee is
  inserted ahead of the txid fallback: among equal-work transits, prefer the one
  a sequencer is more likely to confirm (bigger fee → more chances of inclusion),
  which is following the branch most likely to reach the LRB. It sits *after*
  work on purpose — otherwise a miner could buy a tie cheaply (the fee is capped
  at 1% of A), undoing the work-based fairness; after work it only steers the
  rare equal-work tie toward confirmation. Implemented in `betterThan`
  (`proxi/node_cmd/mine_tree.go`).

This is a **client convention, not consensus** — the ledger still decides via
the sequencers. A defector who always builds on its own branch is simply more
likely to be orphaned.

### Reorg, pruning, bounds

- LRB monitor (as today) is ground truth: on a confirmed tip, prune everything
  not descended from it, re-root, and abort the current round if the mining
  target is no longer on the best chain.
- TTL: drop heights below `confirmed - K` (K ≈ 8) and any buffered orphan older
  than a few slots. Hard cap on total nodes; drop lowest-height first.

### Loop integration

Replaces the current `ourChain` map and single speculative branch. The abort
signal already exists (`miner.abort`) — the stream becomes a second, much
faster source for it. Speculation still works: the miner's own transit enters
the tree at t=0, but a competitor's transit at the same height now arrives in
milliseconds instead of ~8 slots.

## 5. Node-side spec

**Event.** `EventNewMiningTx = eventtype.RegisterNew[[]byte]("new mining tx")`
in `core/workflow/events.go`, with `PostEventNewMiningTx(txBytes []byte)`.
Posted from `processValidated` immediately after `MustPersistTxBytes`, gated on
`tx.IsMiningTransaction()`. Posting after persist (not before) guarantees a
subscriber can always re-fetch the bytes. Events dispatch asynchronously off
the queue, so this does not add latency to the gossip path.

**Listener.** `OnNewMiningTx(fun func(txBytes []byte) bool)` in
`core/workflow/listen.go`, mirroring `OnNewVertex`.

**Endpoint.** `api.PathMiningTxStream = PrefixAPIV1 + "/mining_stream"`, served
by a new `api/streaming/mining_tx_server.go` modeled directly on
`dag_vertex_server.go`: origin check, connection cap with oldest-eviction,
per-write deadline, reader goroutine for disconnect detection.

**Message.** One JSON object per tx:
```json
{"tx_bytes": "<hex>", "txid": "<hex>"}
```
`txid` is a convenience only — the client recomputes it from the bytes.

**Config.** `api.mining_streaming.{max_connections, connection_ttl_minutes}`
via the existing `ConfigKey` helper pattern. Defaults: `max_connections` 10,
`connection_ttl_minutes` 60 (the dagviz default of 5 is far too short for a
long-running miner; the client must auto-reconnect regardless).

**Optional (phase 2).** Keep a ring buffer of the last ~64 mining txs and
replay it on connect, so a reconnecting miner bootstraps its tree without
waiting for the next transit.

## 6. Residual asymmetry — assessment

Measured/derived parameters: tick 80 ms, slot 10.24 s, min pace 3 (30.7 s),
target pace 4 (41 s). Observed: K=11, 2k–7k attempts per solve at ~110 H/s
→ **18–64 s per solve**.

- Producer of transit N knows it at t=0.
- Every other miner learns at t = one gossip hop. European datacenters,
  ~10–50 ms RTT plus queue handling — call it **well under one 80 ms tick**.
- Ratio to solve time: **~0.1 %**. Negligible.

For comparison, today's LRB-confirmation detection is ~8 slots ≈ 80 s, i.e.
*longer than a solve* — which is exactly why the advantage compounds into a
permanent ratchet.

So the original guess ("some milliseconds, up to one ledger tick, hopefully not
critical") is right, and the margin is three orders of magnitude. **This works.**

### The honest caveat

Streaming removes the *information* asymmetry. It does not by itself fix the
degenerate difficulty regime described in `mining-bias.md`: if solve time falls
far below the pace floor, every miner sits solved and waiting for the earliest
legal slot, and the height is then decided by network proximity to the
tag-along sequencer rather than by work.

Right now solve time (18–64 s) is comparable to the pace (41 s), so the race is
genuinely work-decided and streaming should be sufficient. It stays sufficient
only while that holds.

**There is a client-side lever that keeps it holding, with no ledger change.**
The miner currently stamps at `MineTargetPace`, which lands `span = 16 =
4×targetPace` exactly in the retarget dead band and **freezes B** — which is why
K has been pinned at 11. Stamping at `MineMinPace` (floored by the wall clock)
gives `span = 12`, below `target-2`, so B hardens until solve time rises to meet
the pace. The retarget does work; the current client is holding it still.

Note the strategic asymmetry this exposes, which remains a genuine ledger-level
hole: easing the difficulty benefits whoever profits from a *latency* race,
i.e. exactly the miner that is already winning. Streaming removes the motive by
removing the latency edge, but a defector can still stamp long to drag B down.
That is the `constMineMaxPace` question in `mining-bias.md`, still open.

## 7. Phasing

1. **Node:** event + listener + WS endpoint + config. **DONE** (`f28f6f75`) —
   `EventNewMiningTx` posted from `processValidated`, `/wsapi/v1/mining_tx_stream`.
2. **Miner:** verifier (§2). **DONE** — `proxi/node_cmd/mine_verify.go`, tested
   against a genuinely mined transit plus one mutation per rule.
3. **Miner:** tree, best-chain selection, pruning. **DONE** —
   `proxi/node_cmd/mine_tree.go` + `mine_stream.go`; replaces `ourChain`.
   Tie-break is most-work-then-bigger-fee-then-lowest-txid, never first-seen (§4).
4. **Miner:** stamp at `MineMinPace`. **DONE** — `successorSlot`.
5. **Miner:** multi-endpoint subscription for A3. **DONE** — `--stream` may be
   repeated; the configured `api.endpoint` is always included, `--no-stream`
   opts out.
6. Optional, NOT done: replay buffer on connect.

The fleet needs a coordinated node redeploy before miners gain anything: a
miner only sees what its own node relays, so until every node runs the phase-1
build, subscribing to one that does not is silently equivalent to `--no-stream`.

### Not yet validated in production

Everything above is unit-tested but has never run against a live network. Open
questions for the first deployment:

- Does the tie-break actually converge under real propagation, or do miners
  oscillate between branches at the same height?
- Does stamping at `MineMinPace` move K off the floor, and how fast?
- What is the real observed stream latency versus the ~0.1 % of solve time
  estimated in §6?
- Does the orphan/pending buffer see meaningful traffic, i.e. do frames really
  arrive out of order?

## 8. Open questions

- Does any node in the fleet need to stream mining txs it did *not* attach?
  (Yes under this design — that is the point — but confirm no accounting or
  metric assumes attach-then-stream.)
- Replay buffer: worth it, or is the LRB re-anchor enough on reconnect?
- Should the node apply a cheap PoW pre-filter before streaming to cut A2 noise?
  It would need the current tip's B, adding state coupling. Prefer keeping the
  node dumb and filtering client-side.
- Multi-endpoint: union the streams, or treat disagreement between nodes as a
  signal in its own right?
