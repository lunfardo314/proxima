# Peering — pre-refactor assessment, findings and plan

> **SUPERSEDED** — archived from `peering/README.md` on 2026-08-26. It was a
> pre-refactor assessment that had become the package README, and it describes a
> package that no longer exists in that form: **the heartbeat protocol it builds
> on has since been removed** (liveness is now libp2p `Connectedness` via
> `Notifiee`), so §1.1 and §1.4 are wrong about the code as it stands, and parts
> of the refactor plan in §3 have landed while others were overtaken.
>
> Read it for the *reasoning*, which the code cannot record — in particular why
> per-message outbound streams were tried and rejected (~1 RTT of
> multistream-select negotiation per message, fatal to gossip latency on
> transcontinental links). The current package is described in
> `peering/README.md`.


Scope: the `peering/` package (2.4 K lines, 11 files). Goal: reduce code
complexity by leveraging libp2p primitives the package currently
reinvents, simplify the heartbeat/liveness model, and tighten the
logging surface. Prepared as a pre-refactor assessment.

## 1. Current architecture at a glance

### 1.1 Transport and protocols

- Transport: libp2p QUIC-v1 on UDP, no TLS (`libp2p.NoSecurity`), no
  relay (`libp2p.DisableRelay`). QUIC reuse enabled by default.
- Three application protocols, each with ledger-hash-derived name suffix
  so nodes of different ledger versions ignore one another:
  - `lppProtocolGossip` — tx broadcast (`txbytes.go`).
  - `lppProtocolPull` — tx pull by id (`pull.go`).
  - `lppProtocolHeartbeat` — periodic liveness + capability flags + a
    wall-clock timestamp (`heartbeat.go`).
- Wire format: handwritten 4-byte BE length prefix + payload
  (`misc.go:readFrame`/`writeFrame`), max 64 KB − 4. Protocol-specific
  payloads handwritten (no protobuf / framing helper from libp2p).

### 1.2 Peer state

Maintained in `peering/types.go` `Peers` struct:

- `peers map[peer.ID]*Peer` — all currently-connected peers (static +
  dynamic).
- `staticPeers map[peer.ID]*staticPeerInfo` — preconfigured peers, kept
  separately so they can be re-dialed when they cycle through
  blacklist/cooloff.
- `blacklist map[peer.ID]_deadlineWithReason` — TTL-bound banned peers.
- `cooloffList map[peer.ID]time.Time` — TTL-bound "don't redial yet".
- `connectList set.Set[peer.ID]` — in-progress dial markers to avoid
  reentrant dials.

Each `Peer` caches one long-lived `network.Stream` per protocol in
`p.streams` (gossip, pull, heartbeat). The current code reuses the
cached stream for every outbound message, with a redial-and-retry layer
around write failures (`sendMsgBytesOut` + `ensurePeerStream` +
`writeFrameToPeerStream`, added in commit `5231dece`). Inbound streams
are handled by protocol-registered handlers which loop on `readFrame`.

### 1.3 Discovery

- Kademlia DHT in `dht.ModeAutoServer` with bootstrap = the static peer
  list. Rendezvous string = `uint64` derived from first 8 bytes of the
  genesis ledger library hash.
- `peering_autopeering_loop` runs every 3 s, calls
  `routingDiscovery.FindPeers(rendezvous, limit=20)`, filters out peers
  already known / blacklisted / cooloff / in-progress, shuffles, dials
  up to the remaining `MaxDynamicPeers` cap.
- `dropExcessPeersIfNeeded` manually trims dynamic peers above the cap
  (one at a time, skipping those younger than `gracePeriodAfterAdded`).

### 1.4 Liveness via heartbeat

- Every peer is sent one HB message every `heartbeatRate = 2 s`. Payload:
  `{flags (1B), clock.UnixNano (8B), counter (4B)}`.
- Receiver updates `lastHeartbeatReceived`; a peer is alive while
  `time.Since(lastHeartbeatReceived) < aliveDuration = 10 × HBrate =
  20 s`.
- Only the `flagRespondsToPullRequests` flag is used by any logic. The
  clock is recorded into a 10-slot ring buffer, quartiles computed,
  and logged as a warning when median > 4 s — **nothing else acts on
  it**.
- Send-side drop: if HB send fails `hbSendErrThreshold = 10` times **and**
  the peer has not sent incoming HBs for `aliveDuration`, the dynamic
  peer is dropped (symmetric-unreachability rule from `5231dece`).

### 1.5 How peering is consumed

- `GossipTxBytesToPeers` — called by workflow to fan-out received tx.
- `PullTransactionsFromPeers` — called to request a tx by id.
- Handlers register callbacks via `OnReceiveTxBytes` /
  `OnReceivePullTxRequest`. That's the entire external API used by the
  rest of the node.

## 2. Findings

### 2.1 libp2p primitives that the package reinvents

None of the following libp2p facilities are used anywhere in the
codebase (verified by grep: `Notifiee|ConnectionGater|Peerstore.Protect|
Watermark|network.Connected|network.Disconnected` returns 0 matches):

| libp2p primitive | What we do instead today |
|---|---|
| `network.Notifiee` (Connected/Disconnected callbacks) | Reimplemented via HB timeout + `_isAlive` bookkeeping |
| `ConnectionGater` (InterceptAccept / InterceptSecured / …) | Reimplemented via `blacklist` + `cooloffList` checks in each stream handler |
| `Peerstore.Protect`/`Unprotect` | Reimplemented via the `staticPeers` map + re-dial on cooloff expiry |
| `ConnManager` watermarks (low/high + grace) | Partially used (connmgr constructed with `MaxDynamicPeers`, `MaxDynamicPeers+5`) but overridden by `dropExcessPeersIfNeeded` which re-trims manually |
| Ping protocol `/ipfs/ping/1.0.0` | Not used — HB serves the liveness role |

Net effect: the package maintains several parallel peer-state
structures (peers / staticPeers / blacklist / cooloffList / connectList)
with explicit cleanup goroutines and mutex dances, while libp2p has
canonical hooks for each of these concerns.

### 2.2 Long-lived cached streams — correct design, imperfect plumbing

**Long-lived streams are the right design for Proxima's gossip path**
and must stay. History note: the codebase used per-message streams
earlier; it was deliberately moved to cached long-lived streams because
per-message adds roughly **one RTT per message** through libp2p's
multistream-select protocol negotiation on every new stream. QUIC makes
*stream creation* cheap (shared connection, no new handshake), but
libp2p's per-stream protocol negotiation on top of that costs a full
round-trip. For a transcontinental link (~100-150 ms RTT), that would
cap gossip at a handful of transactions per second per peer pair before
stream-open latency dominates. Gossip of every transaction is the
latency-critical path here; pay for stream setup once, amortise over
thousands of messages.

So the refactor direction is *not* to flip to per-message streams.
The actual issue is plumbing around the long-lived model.

Symptoms observed in logs:

- `error while sending message to peer … err=failed to write payload:
  stream reset …` at `Warn` (loc0-acc 15:59/16:00 log), fires at the
  2-second HB cadence whenever one side's cached stream has been
  reset and the sender hasn't redialed yet.
- `error sending heartbeat. Drop peer.` + `dropped dynamic peer` +
  `added dynamic peer …` cycle: one-way transient resets trip the
  drop condition even though the peer is fine. `5231dece` raised the
  threshold and required symmetric unreachability, which helps, but
  the log spam remains.

Root cause is that stream-reset handling is reactive (discover on write
failure, redial, retry, log) and runs on the per-send path. Libp2p has
connection-level events (`network.Notifiee.Disconnected`) that fire
when the *connection* dies, which is the only time we actually need to
rebuild cached streams. A stream reset inside a healthy connection is
routine — transparent redial, no log, no drop.

What to change (refined):

- Treat stream reset inside a live connection as routine: silently
  rebuild the cached stream on next send, no log at Error/Warn level.
- Treat `Notifiee.Disconnected` as the authoritative "peer gone" signal;
  drop all cached streams for that peer, then let reconnection logic
  re-establish.
- Demote the remaining `stream reset` log lines (`gossip:`, `pull:`
  streamHandler exits, write-failure warnings) to `Tracef` uniformly —
  step 1 below.
- Keep the redial-retry layer from `5231dece`; it is the right recovery
  mechanism, just noisier than necessary.
- Optionally: drop the eager triple-stream pre-open in `dialPeer` (one
  stream for each of gossip/pull/HB at peer-add time). Lazy-open on
  first send per protocol is simpler and the stream-cost difference is
  one-time anyway.

### 2.3 Heartbeat protocol — concrete audit

Purposes currently served, ranked by actual use:

1. **Liveness tracking** (`p.lastHeartbeatReceived`, `_isAlive`,
   `_isDead`). Used to gate `NumAlive`, `peerIDsAlive`, to trigger drops
   of dynamic peers, and to emit the per-10s connection-summary log.
   Could be replaced by `network.Notifiee` Connected/Disconnected
   events and/or libp2p's ping protocol.
2. **`respondsToPullRequests` flag.** Advertises that the sender won't
   reply to pull requests (policy: `IgnoreAllPullRequests`,
   `AcceptPullRequestsFromStaticPeersOnly`). Used to compute pull target
   lists.
   Replacement options: (a) send this once at connection via a tiny
   capabilities protocol; (b) handle it server-side — just ignore a pull
   request instead of advertising inability ahead of time.
3. **Clock timestamp** (`hbInfo.clock`). Collected into a 10-slot ring,
   quartiles computed, median-above-tolerance logged as a warning.
   **Nothing else consumes it.** The author's own TODO in `peers.go:31`
   reads "get rid of clock in hb, probably remove heartbeat protocol
   altogether."
4. **Counter** (`hbCounter`). Used only in trace-tag debug lines.
5. **Receipt-side "message from peer" watermark** (`evidenceMessage()`
   updates `lastMsgReceived`, checked by `disconn_log_loop` which warns
   `node is DISCONNECTED from the network`). Any incoming frame
   (gossip, pull, hb) ticks this, so HB isn't strictly necessary for
   this — it just guarantees regular ticks.

If HB is removed or drastically reduced, liveness (1) and the
network-disconnect watchdog (5) need a different signal. The ping
protocol and/or `Notifiee` cover (1); (5) becomes less interesting once
libp2p disconnect events are wired in.

### 2.4 Static vs dynamic peer sets

The two sets the user wants already exist — but with two sources of
truth: static peers appear both in `peers` (general list) and
`staticPeers` (maddr cache for redial), plus they must never be dropped,
which is enforced by the `cleanCoolofflist` loop re-adding them via
`addStaticPeer`.

libp2p has direct support for this with less code:

- `host.ConnManager().Protect(id, tag)` makes the connection exempt from
  ConnManager trimming for a named tag. Set once per static peer at
  startup; ConnManager's watermarks then handle dynamic churn naturally.
- `host.Network().Notify(notifiee)` gives Connected/Disconnected
  callbacks. On Disconnected of a static peer, kick off a reconnect
  attempt (with exponential backoff), without needing the cooloff/
  redial machinery.
- `ConnectionGater.InterceptSecured` is the right place for the
  blacklist check — enforced once at connection establishment, not at
  every stream handler entry.

The staticPeers map can shrink to `map[peer.ID]multiaddr.Multiaddr`
(just for redial targets); the rest is libp2p state.

### 2.5 Logging noise and informativeness

Concrete noise sources, with severity calls:

- `gossip: streamHandler exit` / `pull: streamHandler exit` — fire on
  every stream close, which is every normal message turnaround under
  the cached-stream model. Already demoted to `Tracef` in `5231dece`
  for HB, not yet for gossip/pull. **Demote all three uniformly.**
- `error while reading message from peer … transport error: Application
  error 0x0 (local)` at `Error` — this is what a peer graceful disconnect
  looks like from the reader's side. **Demote to Debug or Trace.**
- `[peering] node is DISCONNECTED from the network for …` at `Warn` —
  fires whenever no frame has arrived for >6 s, which under stalled
  peering can fire every few seconds. **Rate-limit or move to
  Notifiee-driven events (fires once on state change).**
- `[peering] incoming peer request. Add new dynamic peer <id>` —
  emitted from every protocol handler (gossip, pull, hb) when the peer
  is not yet known, so a single new peer produces three identical INFO
  lines (observed in loc0-acc log: "incoming peer request" × 3 on the
  same peer at 16:00:28). **Emit once per peer-add transition, not per
  stream opening.** Best place: `Notifiee.Connected`.
- `[peering] dropped dynamic peer` / `added dynamic peer` cycle on
  long-lived cached streams resetting — goes away once streams are
  per-message.
- `CONNECTED to dynamic peer` / `LOST CONNECTION with dynamic peer`
  guarded by `lastLoggedConnected` — good, keep but drive from
  Notifiee instead of HB polling.

Informativeness improvements:

- Every peering log line should carry the peer's short id **and** its
  configured name when static. Currently some lines show only the id,
  some show only the name, some show neither.
- The 10-s connection summary line (`node is connected to N peer(s)…`)
  is the single most useful status line — keep it, consider adding
  incoming-per-peer tx-rate and pull-hit-rate at the same cadence.

## 3. Refactor plan (incremental, each step independently deployable)

**Step 1 — Logging cleanup** (smallest, no behavior change).
- Demote `gossip:` and `pull: streamHandler exit` + transient stream
  read errors from Error/Warn/Info to `Tracef`/`Debug`.
- De-duplicate "incoming peer request" by emitting only from the HB
  handler (or, after step 3, from `Notifiee.Connected`).
- Rate-limit `node is DISCONNECTED` to once-per-state-change.
- Include peer short id + name in every peering log line.

**Step 2 — Long-lived stream hygiene** (NOT per-message).

Per-message outbound streams were tried earlier and rejected — they add
~1 RTT of multistream-select negotiation per message, which kills
gossip latency on transcontinental links. Long-lived cached streams
stay. The work here is to make their maintenance quiet and correct.

- Silent redial on stream reset inside a live connection: on write
  failure, clear the cached stream, redial once, retry, and **do not
  log** at Error/Warn (Trace only). The stream reset-while-connection-up
  event is routine, not an error.
- Connection-level truth from `Notifiee.Disconnected` (step 3 below):
  that event is when we drop cached streams and stop retrying. A
  stream reset alone does not warrant dropping the peer.
- Keep the redial-retry layer from `5231dece` — it is the right
  recovery mechanism; just strip its logging to Trace.
- Lazy per-protocol stream open: drop the eager three-stream pre-open
  in `dialPeer`. Open each stream on first send over that protocol.
  One-time difference, simpler failure semantics at dial time.
- Keep `peerStream` struct and its write-mutex serialisation — required
  because libp2p streams need serialised writes.
- Expected effect: the stream-reset-log class disappears from
  normal-operation logs, the drop-and-readd peer cycle under one-way
  transient resets is eliminated once `Notifiee.Disconnected` is the
  drop trigger (step 3), and per-message latency is unchanged from
  today (still zero extra RTT per message).

**Step 3 — libp2p Notifiee + Peerstore.Protect + ConnectionGater.**
- Register a `network.Notifiee` on `host.Network()`:
  - `Connected` → "CONNECTED to peer X" log, emit-once peer-add,
    `ConnManager.Protect` if static.
  - `Disconnected` → "LOST CONNECTION with peer X" log, on-static schedule
    a reconnect with backoff.
- Register a `ConnectionGater` with `InterceptSecured` checking the
  blacklist. Delete per-handler blacklist checks.
- Static peer set becomes `map[peer.ID]multiaddr.Multiaddr` only. No
  connectList/cooloffList/staticPeers mirror; all that state lives in
  libp2p.

**Step 4 — Remove clock from heartbeat.**
- Drop `hbInfo.clock`, `clockDifferences*`, `logBigClockDiffs`,
  `clockTolerance`, `peering_clock_tolerance_loop`.
- Drop `ClockDifferencesQuartiles` from `api.PeerInfo` (coordinated API
  change, but nothing consumes it).

**Step 5 — Reduce or remove the heartbeat protocol.**

Two options — choose one after step 3 ships:

- *Option A (remove entirely):* drop the HB protocol. Liveness comes
  from `Notifiee` + `/ipfs/ping/1.0.0` (with configurable interval, say
  15 s). Pull-readiness flag becomes a lazy capabilities exchange sent
  once at connection over a tiny handshake protocol, or (cheaper) the
  pull server just ignores inbound pulls when policy forbids.
  Logging of `nothing received for N seconds` from peer becomes
  Notifiee-driven.

- *Option B (minimise):* keep the HB protocol but send one message
  only when the capability flags change, plus one at connection
  establishment. No periodic HBs. Advantages: still one dedicated channel
  for node-to-node metadata if we want to extend later (e.g. sync hint,
  coverage level, mempool fill). Disadvantages: more code than A,
  small benefit.

Recommendation: **option A**. Ping is built in and maintained; any
future node-to-node metadata can be added as its own small protocol
with a per-connection handshake pattern (same as libp2p's own identify
protocol uses).

**Step 6 — Lean on ConnManager watermarks for dynamic churn.**
- Constructor already uses `connmgr.NewConnManager(Max, Max+5)`; extend
  it with a real grace period.
- Delete `dropExcessPeersIfNeeded` — ConnManager handles this once
  static peers are protected (step 3).
- Keep autopeering discovery loop as-is — it only triggers connects,
  doesn't manage churn.

## 4. What to keep unchanged

- QUIC-v1 transport, `NoSecurity`, `DisableRelay` — correct for this
  network.
- Rendezvous = genesis ledger-library hash — correctly segregates
  ledger-version peers.
- Gossip and pull protocol framing — fine as-is; length-prefix framing
  is cheap and explicit.
- Per-protocol stream handlers pattern — stays, only the outbound
  side changes (step 2).
- The public API of `peering.Peers` seen by `node/` and `core_modules/`
  — only two functions (`GossipTxBytesToPeers`,
  `PullTransactionsFromPeers`) plus the two `OnReceive…` setters. None
  of the above steps touch that surface.

## 5. Rough complexity reduction estimate

| Area | Current lines | Expected after refactor |
|---|---|---|
| `peers.go` | 785 | ≈ 400 (drop stream-caching layer, peer-list reconciliation loops, manual dial) |
| `heartbeat.go` | 298 | 0 (option A) or ≈ 60 (option B) |
| `autopeering.go` | 84 | ≈ 60 (no `dropExcessPeersIfNeeded`) |
| new `notifiee.go` | — | ≈ 100 |
| new `gater.go` | — | ≈ 40 |
| `types.go` | 265 | ≈ 180 (drop clock ring, hb counters, complex peer-state) |

Net: ~2.4 K → ~1.2 K lines, with most of the subtraction in peer-state
bookkeeping and heartbeat plumbing.

## 6. Open questions

- **Do we need a ping-driven liveness at all?** If every real
  application message (gossip, pull) already keeps `lastMsgReceived`
  fresh, and Notifiee tells us when a peer disconnects, then explicit
  ping is redundant. Worth measuring on the current testnet before
  committing to adding it.
- **Pull-readiness flag transport.** Only used today by policy knobs
  (`IgnoreAllPullRequests`, `AcceptPullRequestsFromStaticPeersOnly`).
  Does anyone actually toggle these, or are they set once in config and
  never changed? If never changed, the simplest transport is "read
  peer's policy from its config file" — never advertised, never
  needed. The server just doesn't reply.
- **Symmetric unreachability drop** currently implemented via
  `numHBSendErr ≥ 10 && !_isAlive`. Once HB is gone, the equivalent is
  Notifiee's Disconnected (libp2p's internal stack decides). Verify
  libp2p gives us the drop signal promptly enough under QUIC's failure
  modes.
- **Autopeering rendezvous limit=20.** The `FindPeers` call caps at
  20 new candidates per discovery cycle. Is this enough for networks
  with hundreds of nodes? Might need tuning separately from this
  refactor.
