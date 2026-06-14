# Network RTT mapping, visualization & Monte-Carlo — protocol sketch

Status: design sketch. Companion to `claude/tick_duration.md`, which needs a measured
RTT metric graph $d(i,j)$ and a capital map $m_i$ to compute the consolidation radius
$\rho_{2/3}$ and the per-slot success probability $P_{\text{succ}}(T)$.

Goal: approximately map the **whole** Proxima network as an RTT-weighted metric graph
annotated with per-node capital (coverage = balance + frozen), **visualize** it, and
**Monte-Carlo simulate** consensus consolidation to pick a safe tick duration.

---

## 1. Goals / non-goals

**Goals**
- A reasonably current, network-wide estimate of pairwise effective message latency.
- Per-node mass (capital) annotation, derived **trustlessly** from ledger state.
- A force-directed visualization (latency = geometry, capital = node size).
- An offline simulator that turns the graph into $P_{\text{succ}}(T)$ vs tick curves.

**Non-goals**
- Not a consensus input. This is an **operational / analysis** overlay; nodes may lie
  about latency, so it informs human/parameter decisions, never validity.
- Not precise per-edge timing. Order-of-magnitude RTT + tail percentiles suffice (the
  model only needs the radius enclosing 2/3 of mass and its tail).

---

## 2. Existing building blocks (reuse, don't reinvent)

| Component | Where | Use |
|---|---|---|
| libp2p ping service `/ipfs/ping/1.0.0` | `peering/types.go:65-67` | active RTT to direct peers |
| `peer_rtt_loop` → `lastRTTNs` (atomic) | `peering/peers.go:218`, `types.go:113-115` | per-peer latest RTT |
| `GetPeersInfo()` → `api.PeersInfo` | `peering/peers.go:622` | local adjacency + RTT, already serialized |
| `/api/v1/peers_info` endpoint + `/peers` dashboard | `api/api.go:30,64`, `api/server/dashboard.go` | crawl source + a UI to extend |
| Kademlia DHT autopeering | `peering/autopeering.go` | enumerate the peer set for a crawl |
| Custom streams `/proxima/gossip/%d`, `/proxima/pull/%d` | `peering/types.go:125-126`, `pull.go`, `txbytes.go` | passive RTT from real round-trips |
| LRB state (coverage per sequencer) | `multistate`, `/api/v1` | trustless mass $m_i$ |

The **measurement layer already exists** for direct neighbors. The gap is (i) turning
"latest RTT" into a distribution, (ii) **aggregating** every node's local view into one
graph, (iii) binding peerID → sequencerID → mass, (iv) visualization, (v) simulation.

---

## 3. Architecture — three layers

```
 ┌───────────── MEASURE (per node, online) ─────────────┐
 │ active: libp2p ping  →  RTT samples per direct peer   │
 │ passive: pull req→resp & gossip ack round-trips       │   effective latency
 │ aggregate locally → EWMA + p50/p90/p99 per neighbor   │   (incl. processing)
 └───────────────────────┬───────────────────────────────┘
                         │ local adjacency record (signed)
 ┌───────────── AGGREGATE (whole network) ───────────────┐
 │ A) crawl: walk DHT peer set, GET /peers_info from each │
 │ B) gossip: flood signed RTT-vector records (TTL)       │
 │ → global directed RTT matrix Ĝ (sparse, per-edge dist) │
 │ → metric closure: all-pairs shortest-path = d(i,j)     │
 └───────────────────────┬───────────────────────────────┘
                         │ Ĝ + masses
 ┌───────────── CONSUME (offline) ───────────────────────┐
 │ mass annotation: peerID↔seqID↔coverage from LRB        │
 │ visualize: force-directed (RTT springs, mass = radius) │
 │ simulate: round-diffusion Monte-Carlo → P_succ(T)      │
 └────────────────────────────────────────────────────────┘
```

---

## 4. Layer 1 — measurement (effective latency, with tails)

The model in `tick_duration.md` needs **effective propagation latency** (network + queue +
validation), not raw ICMP. Measure two ways and keep both:

- **Active (ping).** Extend `peer_rtt_loop` to keep a reservoir/EWMA per neighbor and emit
  `p50/p90/p99` + sample count instead of a single `lastRTTNs`. Cheap, isolates the
  network leg.
- **Passive (protocol round-trips).** Time `pull` request→response and gossip
  send→first-relay-back on the existing `/proxima/pull` and `/proxima/gossip` streams.
  This captures **real** propagation incl. processing — exactly the $d$ the model wants.
  Tag each sample with payload size to separate fixed latency from bandwidth.

Output per node $i$: a local vector $\{(j, \hat d_{ij}^{p50}, \hat d^{p90}, \hat d^{p99},
n_{ij}, t_{\text{last}})\}$ over its direct neighbors $j$. RTT is roughly symmetric but
**keep direction** — asymmetry and disagreement between $\hat d_{ij}$ and $\hat d_{ji}$
is a useful liar/quality signal (§10).

Publication of this vector (and the §6 identity binding) is gated by the node's opt-out /
disclosure setting (§11): a node always measures for its own routing, but may decline to
**disclose**.

---

## 5. Layer 2 — aggregation into a global metric graph

Each node only sees direct neighbors; we need the union. Two interchangeable transports:

- **(A) Crawl (pull, simplest first).** An offline collector seeds from a known node,
  walks the DHT / `peers_info` neighbor lists breadth-first, and `GET /api/v1/peers_info`
  (extended with the §4 distribution) from every reachable node. Snapshots the whole
  adjacency in one pass. No protocol change beyond enriching `peers_info`. Best for a
  first cut and for the testnet.
- **(B) Gossip (push, scales / decentralizes).** Each node periodically floods a **signed**
  `RTTVector` record (its local vector, a timestamp, a sequence number, TTL) over a new
  `/proxima/netmap/%d` stream or gossipsub topic. Any node assembles the global graph from
  the freshest record per origin. Self-healing, no central crawler, but adds a protocol.

Either yields a sparse **directed** edge set $\hat G=\{(i,j)\mapsto \hat d_{ij}\}$. Then:

- **Metric closure.** The model's $d(i,j)$ is the gossip-path latency = **shortest-path**
  over $\hat G$ (Floyd–Warshall for small $N$, Johnson/Dijkstra-per-source for large).
  Use the chosen percentile (p90 for tail-aware $\rho_C$) as edge weight.
- **Freshness / staleness.** Records carry timestamps; weight or drop stale edges. The
  graph is a slowly-drifting estimate, refreshed on a cadence (minutes), not real-time.
- **Missing edges.** Non-adjacent pairs have no direct sample by construction — that's
  fine, the shortest-path closure fills them (which is exactly the transitive RTT the
  model assumes).

---

## 6. Layer 3a — mass annotation (trustless)

The vis/sim need $m_i = \text{balance}_i + \text{frozenCoverage}_i$ per node. Mass must be
**trustless** (a node must not be able to inflate its own importance):

- **Source of mass:** the LRB ledger state. Sequencer coverage per chain is already
  computable (`balance + frozenCoverage[epoch0]`, cf. `CurrentCoverageContribution` and
  the chain explorer). This is consensus data — unforgeable.
- **peerID ↔ sequencerID binding (the missing link).** Today there's no explicit binding
  in `peering/`. Options, cheapest first:
  1. **Self-announce, signed.** A sequencer node publishes `sign(seqControllerKey, peerID)`
     in its §5 record; verify against the on-chain sequencer controller. Trustless binding.
  2. **Heuristic.** Correlate the source peer of a sequencer's milestones (gossip origin)
     with its chain ID. No new protocol, weaker.
  3. **Out-of-band.** Operator-supplied mapping for the known testnet (4 machines) — fine
     for the first analysis pass.
- Access nodes (no sequencer) have mass 0; they still appear as **relay vertices** that
  shape the shortest-path metric.

---

## 7. Layer 3b — visualization

A force-directed graph where geometry ≈ latency and size ≈ capital:

- **Layout:** spring-embed with rest length $\propto d(i,j)$ (or MDS / `stress majorization`
  on the $d$ matrix so 2-D distance approximates RTT). Nodes that are latency-close cluster.
- **Encoding:** node radius $\propto \sqrt{m_i}$; color by region/AS or by k-means cluster
  in latency space; edge opacity $\propto$ freshness, width $\propto 1/d$.
- **Overlays:** the capital **center of mass** $c^\star$ and the $\rho_{2/3}$ ball; the set
  reachable within $R(T)$ for a chosen tick (the §tick_duration safety picture, made visual).
- **Delivery:** reuse the existing `/peers` dashboard + `dagviz` frontend stack; export a
  static `network.json` (schema below) that a d3/vis.js force view or an offline script
  renders. Keep a CLI path (`proxi util netmap`) that writes JSON + an SVG/PNG so it works
  headless, like `inflation_emulation --chart`.

---

## 8. Layer 3c — Monte-Carlo simulator

Turns the closed form of `tick_duration.md` §4–6 into an empirical $P_{\text{succ}}(T)$.

**Inputs:** the $d$ matrix (with per-edge latency *distributions*, not just point
estimates), masses $m_i$, total $M$, params $\{K_t=128,\ p=12,\ E=8,\ C=2/3\}$, and a
candidate tick $\tau$ (⇒ slot $T=128\tau$, rounds $K=\lfloor K_t/p\rfloor$).

**Per trial (one slot):**
1. Sample per-edge latency from its distribution (capture tails + optional shared-link
   correlation).
2. For each candidate center/root $c$ (or a sampled subset), run $K$ rounds of the
   endorsement diffusion: in round $r$, node $u$ adopts any neighbor's coverage whose
   milestone arrives before $u$'s next emission (latency gate $\approx p\tau$); coverage
   merges with fan-in $\le E$. Track which masses are in $c$'s past cone by the boundary.
3. Consolidated mass $S_c=\sum_{j\in\text{cone}(c)} m_j$; slot succeeds if
   $\max_c S_c \ge CM$.

**Output:** $\hat P_{\text{succ}}(T)=\frac{\#\text{success}}{\#\text{trials}}$ and the
empirical $\rho_C$ distribution, swept over $\tau\in\{80,100,120\}$ ms (and finer). Pick the
smallest $\tau$ with $\hat P_{\text{succ}}\ge 1-\varepsilon$ at the target tail. This is the
honest version of $\tau \ge (\rho_C+T_{\text{ovh}})/128$.

**Form:** offline `proxi util netmap_sim <network.json> [--ticks 80,100,120] [--trials N]`,
node-free (like `inflation_emulation`). Also runnable on **synthetic** graphs (N sequencers,
capital distribution, RTT model with tails) to study the *target* network, not just today's.

---

## 9. Data model (export schema)

```json
{
  "captured_at": "<unix nanos, passed in — clock not available in sim>",
  "nodes": [
    {"peer_id": "...", "seq_id": "<hex|null>", "mass": 0,
     "region": "...", "is_sequencer": false,
     "disclosure": "full|coarse|none", "pseudonymous": false}
  ],
  "edges": [
    {"from": "peerA", "to": "peerB",
     "rtt_ns": {"p50": 0, "p90": 0, "p99": 0}, "samples": 0, "age_s": 0}
  ],
  "params": {"ticks_per_slot": 128, "pace": 12, "max_endorsements": 8,
             "consensus_fraction": [2, 3]}
}
```

`d(i,j)` (shortest-path closure) is derived, not stored. Masses come from LRB at capture
time. Timestamps are passed in (sim/reproducibility code must avoid wall-clock).

---

## 10. Trust & security

- **Mass is trustless** (ledger-derived) — the manipulation-sensitive quantity is safe.
- **Latency is self-reported.** A node can under-report to look central or over-report to
  look isolated. Mitigations: require **both directions** and flag $|\hat d_{ij}-\hat
  d_{ji}|$ outliers; cross-check a claimed edge against third-party paths (triangle
  inequality violations expose lies); prefer **passive** protocol-round-trip samples (a
  liar would have to actually be fast). Since the map is advisory, "good enough + flag the
  liars" suffices.
- **Sybil / privacy:** the map exposes topology and capital concentration (a targeting
  aid). Consider coarse-graining (regions, not exact RTTs) in any public view.

---

## 11. Opt-out / consent

Mapping is **opt-out per node**. A node always measures latency for its own routing, but
controls how much it **discloses**, via config `netmap.disclosure`:

| Setting | Self-RTT vector | Identity (peerID↔seqID) | Rendered as |
|---|---|---|---|
| `full` (default) | published | published (signed) | labeled node, size = mass |
| `coarse` | region/p50 only, no tails | region only, no seqID | labeled by region, no exact location |
| `none` (opt-out) | withheld | withheld | anonymous relay vertex |

When a node sets `none`: it does not serve its enriched RTT vector via `peers_info`, does
not emit `/proxima/netmap` records, and withholds its signed peerID↔seqID binding. Its
record may carry an honor-based **do-not-map** flag asking cooperating collectors to omit
edges incident to it.

**Residual visibility (be honest about the limit).** Opt-out suppresses *self-disclosure*,
not third-party observation:

- **Inbound edges** — neighbors still actively/passively measure RTT *to* the node, so it
  can still appear as an edge endpoint unless those neighbors also honor the do-not-map
  flag. Represent it as a **pseudonymous relay** (stable hashed handle, no `seq_id`, no
  mass label) rather than erasing it, since deleting it would distort the shortest-path
  metric for everyone behind it.
- **Mass is public** — sequencer coverage is consensus data on the LRB and cannot be
  hidden. Opt-out removes the *attribution* (which peerID holds it), not the fact that the
  capital exists. For aggregate analysis, an opted-out sequencer's mass can still be
  counted at its pseudonymous handle (the simulator needs mass + position, not identity);
  for the public view it is shown unattributed.

Because the overlay is **advisory, non-consensus**, opt-out is honored by well-behaved
collectors and gossipers; it is a privacy/consent control, not an enforced guarantee. The
default is `full` so the map is useful out of the box; operators of sensitive nodes choose
`coarse`/`none`.

---

## 12. Phased delivery

1. **Crawl + JSON** (no protocol change): enrich `peers_info` with RTT percentiles; offline
   collector walks the testnet and writes `network.json`. Mass via out-of-band mapping.
2. **Visualize:** `proxi util netmap` → force-directed SVG/PNG + JSON; optional `/peers`
   dashboard graph view.
3. **Simulate:** `proxi util netmap_sim` → $P_{\text{succ}}(T)$ curves; synthetic-graph mode.
4. **Trustless binding:** signed peerID↔seqID announce; mass straight from LRB.
5. **Decentralize:** gossip `/proxima/netmap` records (drop the central crawler).

Phases 1–3 already answer the tick-duration question for the current network; 4–5 harden it.

---

## 13. Open questions

- Which percentile drives $\rho_C$ — p90 or p99? (tail sensitivity of $P_{\text{succ}}$).
- Effective vs raw latency: how much processing/queueing to fold into $d$? Passive sampling
  answers empirically.
- Center model: single best root vs multi-root coverage race (the real protocol is
  multi-root; the sim should support both).
- Edge-latency correlation (shared bottlenecks) — i.i.d. sampling underestimates bad-slot
  tails; needs a correlation model for honest $P_{\text{succ}}$.
- Refresh cadence vs capital/topology drift.
