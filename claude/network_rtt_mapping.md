# Network RTT mapping, distance metric & visualization

Status: layers 1–2 **shipped**, layer 3 visualization (`/netviz`) **shipped**;
the offline Monte-Carlo simulator is the remaining piece. Companion to
`claude/tick_duration.md`, which needs a measured RTT metric graph `d(i,j)` and a
capital map `m_i` to compute the consolidation radius and per-slot success
probability `P_succ(T)`.

Goal: approximately map the **whole** Proxima network as an RTT-weighted metric
graph annotated with per-node capital (coverage = balance + frozen), derive the
pairwise distance metric `d(i,j)`, **visualize** it, and (later) **Monte-Carlo
simulate** consensus consolidation to pick a safe tick duration.

This is an **operational / analysis overlay, not a consensus input**: nodes may
lie about latency, so it informs human/parameter decisions, never validity.

---

## 1. Architecture — three layers, mapped to shipped components

```
 ┌──── L1 MEASURE (per node) ────────────────────────────────┐
 │ libp2p ping → Peer.lastRTTNs per direct neighbor (5s)      │  peering/peers.go
 └───────────────────────┬────────────────────────────────────┘
                         │ local RTT vector, masked-name keyed
 ┌──── L2 AGGREGATE (whole network) ─────────────────────────┐
 │ lppConnectivity overlay: each node gossips its PeerConnec- │  peering/connectivity.go
 │ tions record; every node assembles the global map.         │  GET /get_connectivity_map
 │ derive d(i,j): symmetric averaged RTT + metric closure.    │  peering/connectivity_matrix.go
 │                                                            │  GET /get_connectivity_matrix
 └───────────────────────┬────────────────────────────────────┘
                         │ d-matrix + masses
 ┌──── L3 CONSUME (browser / offline) ───────────────────────┐
 │ visualize: force-directed (d = springs, mass = radius)     │  GET /netviz  (shipped)
 │ simulate: round-diffusion Monte-Carlo → P_succ(T)          │  proxi util (later)
 └────────────────────────────────────────────────────────────┘
```

The measurement layer (L1), aggregation/metric layer (L2) and the `/netviz`
visualization (L3) are implemented. The offline simulator is the remaining piece.

---

## 2. Layer 1 — measurement (implemented)

Each node already measures ping RTT to every alive direct neighbor every 5s
(`measurePeerRTTs`, stored atomically in `Peer.lastRTTNs`). The connectivity
overlay reuses this directly — RTT in the published records is `lastRTTNs / 1000`
(microseconds). No separate measurement path. RTT is roughly symmetric but
**direction is kept** (a node reports its own outbound RTT to each neighbor);
disagreement between `d_ij` and `d_ji` is reconciled in L2 (§4).

---

## 3. Layer 2a — the connectivity overlay (implemented)

Full spec: `claude/network_connectivity.md`. Summary of what's on the wire and on
the API:

- **Protocol** `lppConnectivity` (`/proxima/connectivity/%d`). Enabled by default;
  opt out via `peering.connectivity.disable`.
- **Identity** — a node is a **masked name** = `blake2b256(IP:port)[:8]` (hex).
  IP **and** port, so co-located seq+access nodes on one machine stay distinct.
  Pseudonymous: exposes topology, not raw IPs.
- **Record** (`PeerConnections`, gossiped JSON): origin's own masked name,
  `consensusContribution` (sequencer mass `m_i` = `tokenBalance + frozenCoverage[0]`,
  omitted/0 for access nodes — sourced ledger-free via the node-global
  `ConsensusContribution()` method), `byPeer` (peer masked name → RTT µs),
  `timestamp`, `seq`.
- **Propagation** — each node emits every 15s and floods to all peers; on receipt
  it stores the freshest record per origin and re-gossips subject to a 10s
  per-origin forward gate (anti-cycle). A 1-min TTL evicts silent origins.
- **API** `GET /api/v1/get_connectivity_map` → `{self, captured_at, records[]}`,
  each record carrying `name`, `consensusContribution`, `byPeer`, `timestamp`,
  `seq`, `age_ms`. Raw masked-name hex; no IPs.

**Validated live** (2026-06-20, hboot/hloc0 testnet): masked names are globally
consistent (the name a peer uses for X equals X's own `name`), so a map pulled
from any single node stitches the whole network; sequencer mass is reported
identically from every vantage point; nodes whose own API is unreachable still
appear via gossip.

### Why this is the trustless half it needs to be

- **Mass is trustless** — `consensusContribution` is on-chain (LRB) and verifiable;
  it is self-reported here for convenience, but a value disagreeing with the
  ledger is a liar signal. The map attaches mass to a **pseudonymous vertex**, not
  a peerID↔seqID binding.
- **Latency is self-reported** — advisory. Averaging both directions and the
  metric closure (§4) absorb honest noise; gross lies show up as triangle
  violations. Good enough for an analysis overlay.

---

## 4. Layer 2b — the distance metric `d(i,j)` (implemented)

`GET /api/v1/get_connectivity_matrix` serves the derived metric, computed
server-side from the connectivity map (`peering/connectivity_matrix.go`):

1. **Node set** = union of all masked names appearing as a record origin or a
   `byPeer` key (sorted → stable index).
2. **Reconcile direction disagreement by averaging.** For each unordered pair
   `{a,b}`, the direct distance is the mean of whichever directions are present
   (`a→b` and/or `b→a` RTT). This is the "interpolation" between perspectives.
3. **Metric closure (Floyd–Warshall).** Shortest-path over the averaged direct
   edges fills pairs with no direct sample and enforces the triangle inequality —
   so `d(a,b) ≤ d(a,c)+d(c,b)` holds throughout, and a missing `a–c` edge is taken
   as the best `a…c` path (the transitive RTT the consensus model assumes). A
   direct edge that violates the triangle is shortened to the better path.

Output (`ConnectivityMatrix`): `nodes[]` (index space), `contribution[]` (parallel
to `nodes`, mass per node, 0 for access), and `matrix[][]` — the **packed upper
triangle**: `matrix[i][k] = d(nodes[i], nodes[i+1+k])` in microseconds; diagonal
0; an off-diagonal 0 means no path (disconnected component). Symmetric, so only
half is sent.

**Cost & caching.** Floyd–Warshall is `O(N^3)`; for the current network it is
sub-millisecond, but it is **computed server-side and cached, lazily recomputed at
most once per ~25s** (`connMatrixRefreshInterval`). Putting it on the server (vs.
the browser) keeps the heavy step off arbitrary clients and lets a single node
serve a ready-to-render matrix. If `N` grows large enough that `O(N^3)` every 25s
is a concern, switch to Johnson/Dijkstra-per-source or move the closure to the
consumer — the raw map (§3) already carries everything needed to recompute.

---

## 5. Layer 3 — visualization & simulation

- **`/netviz` (shipped).** A self-contained page (`api/server/netviz.html`,
  served at `/netviz`) that fetches `/api/v1/get_connectivity_matrix` and renders
  a force-directed graph on a canvas: **all-pairs springs with rest length `∝
  d(i,j)`** (stress-majorization of the metric — latency-close nodes cluster) plus
  mild repulsion and centroid recentering. Node radius `∝ √capital`, sequencers
  vs access colored, self ringed, edge brightness `∝ proximity (1 − d/maxD)`.
  Drag-to-pin, hover tooltip (capital + nearest-peer ms), auto-refresh 15s. No
  external JS deps, mirroring `dagviz`/`peers_dashboard`.
- **Monte-Carlo simulator (later).** Turns the `d` matrix + masses into an
  empirical `P_succ(T)` vs tick curve (per `tick_duration.md` §4–8): sample
  per-edge latency, run `K` rounds of endorsement diffusion from candidate roots,
  succeed iff some root consolidates `≥ 2/3` of mass within the slot. Offline,
  node-free (like `inflation_emulation`), also runnable on synthetic graphs.
  Mass comes straight from the matrix's `contribution[]`.

---

## 6. Open questions

- Which percentile of RTT should drive the tail-sensitive `ρ_C` — the overlay
  currently publishes a single latest RTT, not p50/p90/p99. If tail sensitivity
  matters for the sim, extend `byPeer` to carry a small distribution.
- Effective vs raw latency: ping measures the network leg; real propagation
  includes queue + validation. Passive protocol-round-trip sampling (pull/gossip)
  would capture effective `d` empirically — a possible L1 enrichment.
- Edge-latency correlation (shared bottlenecks) for honest `P_succ` tails.
- Refresh cadence (15s emit / 25s matrix) vs topology/capital drift.
- Liar handling beyond triangle-violation flagging, if the overlay is ever made
  more than advisory.
