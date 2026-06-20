# lppConnectivity — peer-connectivity gossip protocol & connectivity map

Status: design spec, no code yet. Companion to and concrete implementation of
**transport (B) "Gossip"** from `claude/network_rtt_mapping.md` §5 (aggregation
into a global metric graph). This protocol is the on-network data-collection
layer; `network_rtt_mapping.md` remains the consumer (visualization +
Monte-Carlo) and the broader trust/opt-out discussion.

Goal: every node continuously publishes, and floods to the rest of the network,
its **local view of peer round-trip times**, keyed by privacy-preserving masked
names, so any single node can serve the **whole** network's adjacency as one
`connectivity map` over an API. A browser / offline tool downloads that map and
stitches it into the RTT-weighted graph the simulator needs.

This is an **operational / analysis overlay, not a consensus input** (§ same
caveat as `network_rtt_mapping.md` §1, §10): masked names and RTTs are
self-reported in gossiped records and a node can lie; the map informs human /
parameter decisions, never ledger validity.

---

## 1. Identity — masked names

A running node is one vertex in the map. Its identity is a **masked name**:

```
maskedName(node) = blake2b256( ip16 || portBE ) [:8]
    ip16   = net.IP.To16()           // 16 bytes, v4-mapped for IPv4
    portBE = uint16 big-endian        // the node's libp2p (QUIC/UDP) listen port
```

- **8 bytes**, rendered as a 16-char lowercase hex string in JSON.
- **Why IP *and* port (not IP alone).** The testnet runs two nodes per machine
  on the same IP (sequencer `:4000`/`:14000`, access `:4001`/`:14001`). Hashing
  IP alone would collapse them into a single vertex. IP:port keeps each running
  node distinct. (Confirmed decision, 2026-06-20.)
- **Why hashed (masked).** The map exposes pseudonymous topology, not raw IPs.
  A direct neighbor already knows a peer's IP, so it can recompute that peer's
  masked name; an outside map consumer sees only the 8-byte handle. (IPv4 is
  brute-forceable from the hash — this is obfuscation, not secrecy; consistent
  with `network_rtt_mapping.md` §10 "coarse-graining".)
- BigEndian per project convention (`CLAUDE.md` working rules).

### 1a. Which IP:port to hash

The masked name must be **globally consistent**: the value a node publishes as
its own (`name`) must equal the value every neighbor computes for it (a key in
their `byPeer`). Both sides therefore key off the node's **externally-reachable
listen endpoint**, never an ephemeral per-connection source port.

| Whose name | How the node obtains the IP:port |
|---|---|
| **A peer** `P` | From the peerstore multiaddr / connection remote addr for `P` — i.e. `P`'s advertised listen IP:port. Authoritative for *directly connected* peers. |
| **Self** | From **libp2p observed addresses** (the identify protocol — the address peers report seeing us at). Works behind NAT. (Confirmed decision, 2026-06-20.) |

Notes / dependencies:
- **QUICReuse** (on by default unless `peering.disable_quicreuse`) makes a node
  dial *and* listen on the same UDP port, so a neighbor's connection-remote port
  equals the neighbor's listen port — the two derivations agree. If quicreuse is
  disabled the connection source port is ephemeral; prefer the **peerstore
  multiaddr** (advertised listen addr) over the live connection addr to stay
  robust.
- **Self bootstrap.** Until identify has produced a stable observed address, the
  node does not yet know its own masked name. During that window it **skips
  emitting** its own record (it still relays others'). Log once at debug.
- **Optional cross-check (hardening, not required).** When a record is received
  *directly* from its origin, the receiver can recompute `blake2b(connRemoteIP:port)`
  and compare to `record.name`; a mismatch flags a misreporting/relayed-as-direct
  node. For multi-hop gossiped records this check is impossible — names there are
  self-asserted (advisory overlay).

---

## 2. Wire structure — `PeerConnections`

One JSON object per node's local view, framed as a single message on the
connectivity stream (and gossiped verbatim):

```jsonc
{
  "name":                 "a1b2c3d4e5f60718",  // origin's own masked name (hex, 8 bytes)
  "consensusContribution": 123456789,           // sequencer mass; omitted/0 for access nodes
  "byPeer": {                                    // origin's direct neighbors
    "00ff00ff00ff00ff": 1850,                    //   peerMaskedName(hex) -> RTT microseconds
    "1122334455667788": 42000
  },
  "timestamp": 1718900000000000000,              // unix nanos, set by origin when produced
  "seq":       12345                             // origin-monotone counter (dedup / staleness)
}
```

```go
// peering/connectivity.go
type PeerConnections struct {
    Name                  string            `json:"name"`                            // origin masked name, 16-hex
    ConsensusContribution uint64            `json:"consensusContribution,omitempty"` // sequencer mass; 0/omitted for access nodes
    ByPeer                map[string]uint64 `json:"byPeer"`                          // peer masked name (16-hex) -> RTT microseconds
    Timestamp             int64             `json:"timestamp"`                       // unix nanos at origin
    Seq                   uint64            `json:"seq"`                              // origin-monotone sequence number
}
```

### 2a. `ConsensusContribution()` on the node-global interface

`peering` has no ledger/sequencer dependency and must keep it that way (its
`environment` interface is just `global.NodeGlobal`). The mass is therefore
exposed as a method on the node-global interface, implemented by the node:

```go
// global/types.go — add to the NodeGlobal interface (e.g. in StartStop or a
// small dedicated sub-interface):
//   ConsensusContribution returns this node's own consensus mass in tokens:
//   0 if the node runs no sequencer, otherwise tokenBalance + frozenCoverage(0)
//   of the sequencer's own latest milestone chain output.
ConsensusContribution() uint64
```

- **Node implementation:** returns `0` when no sequencer is running; otherwise
  reads the sequencer's latest own milestone chain output and returns
  `co.TokenBalance() + co.FrozenCoverage(0)` (the same `balance +
  frozenCoverage[epoch0]` used as coverage contribution; a node runs one
  sequencer chain, so it is a single value). Cheap, read-only, no recomputation
  in `peering`.
- Keeps `peering` ledger-free: the emit loop calls
  `ps.environment.ConsensusContribution()` and nothing more.

- **RTT source:** reuse the existing libp2p-ping measurement
  `Peer.lastRTTNs` (`peering/types.go:115`, refreshed every 5 s by
  `measurePeerRTTs`, `peers.go:704`). `byPeer` value = `lastRTTNs / 1000`
  (ns → µs). (Confirmed decision, 2026-06-20.) No new measurement path.
- **Which peers go in `byPeer`:** every *alive* peer with a measured RTT
  (`lastRTTNs > 0`). Peers not yet measured are **omitted** (absence ≠ zero
  latency). This is direction-specific: it is the origin's *outbound* RTT to each
  neighbor (`network_rtt_mapping.md` §4 "keep direction" — the consumer may
  compare `d_ij` vs `d_ji` as a liar signal).
- **`consensusContribution`** is the origin's **mass** `m_i` from
  `network_rtt_mapping.md` §6, carried inline so the consumer gets adjacency,
  RTT *and* mass from one download. The value is **`0` for an access node**
  (omitted via `omitempty`) and the sequencer mass for a sequencer node;
  `peering` obtains it through a single node-global accessor (§2a) and never
  computes it itself.
  - **Trust note:** unlike RTT (only a node can measure its own neighbor
    latency), mass is **on-chain and verifiable**. Here it is *self-reported* for
    convenience, but a consumer that wants the trustless value can recompute it
    from LRB state (`network_rtt_mapping.md` §6); a self-reported figure that
    disagrees with the ledger is a liar signal. The masked name is IP-based and
    anonymous, so this does **not** publish a peerID↔seqID binding — it attaches
    a mass *quantity* to a pseudonymous vertex, nothing more.
- `timestamp` / `seq` exist for staleness weighting and gossip dedup (below);
  `timestamp` is set from wall-clock at emit time on the live node (fine here —
  unlike the sim, this is not reproducibility-sensitive code).

---

## 3. Protocol mechanics

A new libp2p stream protocol alongside gossip/pull, same framing and
lazy-stream machinery already in the package.

```
lppProtocolConnectivity = "/proxima/connectivity/%d"   // %d = rendezvous number
                                                        // (ledger library hash[:8]), like gossip/pull
```

Wiring mirrors the existing protocols:
- `types.go`: add `lppProtocolConnectivity protocol.ID` to `Peers`; format it in
  `New()` next to `lppProtocolGossip`/`lppProtocolPull` (`peers.go:100`).
- `_addPeer` / `dialPeer`: add the protocol to the per-peer `streams` map
  (`peers.go:330`, `:357`) so `sendMsgBytesOut` can use the cached-stream path.
- `Run()`: register the handler **only when enabled**
  (`SetStreamHandler(ps.lppProtocolConnectivity, ps.connectivityStreamHandler)`)
  and start the 15 s emit loop (below).
- New file `peering/connectivity.go`: handler, emit loop, gossip-on-receive,
  the stored map, and `GetConnectivityMap()`.

### 3a. Emit loop (every 15 s)

`RepeatInBackground("connectivity_emit_loop", 15*time.Second, ...)`:
1. Resolve own masked name from observed addresses (§1a). If unknown yet, skip.
2. Build `byPeer` from alive peers with `lastRTTNs > 0`.
3. Resolve own `consensusContribution` via the node-global accessor
   `ps.environment.ConsensusContribution()` (§2a) — `0` on an access node.
4. Build `PeerConnections{Name, ConsensusContribution, ByPeer, Timestamp: now, Seq: ++}`.
5. Store it as the **own** entry in the local map (so the API serves it too).
5. Send (framed JSON) to **all alive peers** via `sendMsgBytesOutMulti(...,
   lppProtocolConnectivity, ...)`.

### 3b. Receive + gossip (anti-flood / anti-cycle)

The stored map is keyed by origin masked name:

```go
type connEntry struct {
    rec          PeerConnections
    whenReceived time.Time        // local receipt time
}
// guarded by its own mutex (or the Peers mutex)
connMap map[string]connEntry      // origin masked name -> latest
```

On receiving a `PeerConnections` `R` from direct peer `src` (`R.Name` = origin `O`):

1. **Freshness gate (dedup, primary).** If an entry for `O` exists and
   `R.Seq <= stored.Seq` (equivalently `R.Timestamp <= stored.Timestamp`),
   **drop** `R` — it is a duplicate or out-of-order copy arriving via another
   gossip path. Do nothing else.
2. **Store latest.** Replace `connMap[O]` with `{R, now}`.
3. **Forward gate (anti-flood, the 10 s rule).** If `O` was previously unseen,
   **or** `now - previousEntry.whenReceived >= 10 s`, **gossip** `R` to all alive
   peers **except `src`** (`sendMsgBytesOutMulti` with `src` excluded). Otherwise
   do **not** forward — a record for `O` was relayed less than 10 s ago, so
   re-forwarding would amplify a cycle.

Rationale: step 1 stops same-record loops immediately (the common case, since an
origin emits a *new* `seq` only every 15 s); step 3 is the spec's belt-and-braces
rate cap on per-origin forwarding so even malformed/seq-reused records can't
flood. The combination converges: a fresh record fans out once per origin per
~15 s cycle; duplicates die at the first hop.

> Note: the draft phrasing "if time since previous **received** ≥ 10 s, gossip"
> is interpreted as time since we last **stored/forwarded** a record for that
> origin (`whenReceived`). Without the §step-1 freshness check, a steady stream
> of duplicates arriving every <10 s could keep the timer hot and suppress a
> genuinely new record; the `seq`/`timestamp` check removes that hazard, so it is
> part of the spec, not optional.

**TTL eviction.** A dedicated background loop (`connectivity_evict_loop`, period
`connectivityEntryTTL = 1 min`) drops any entry — including this node's own —
whose `whenReceived` is older than the TTL. A live origin re-emits every 15 s,
well inside the minute, so only genuinely silent origins age out; this bounds
`connMap` to currently-active nodes. Runs only when the protocol is enabled.

### 3c. Disabled nodes

When `peering.connectivity.disable = true`:
- the stream handler is **not registered** and the emit loop does not run;
- a peer that opens `/proxima/connectivity/%d` to us gets libp2p
  "protocol not supported"; the sender's `NewStream` fails and
  `sendMsgBytesOut` returns false silently — no drop, no error spam. This is the
  "protocol is just disabled, message ignored" behavior from the draft.
- The node still appears in *other* nodes' `byPeer` (they measure RTT to it via
  ping regardless) — i.e. it shows up as an inbound edge / pseudonymous relay,
  exactly the "residual visibility" of `network_rtt_mapping.md` §11. Disabling is
  a *don't-disclose-my-view* switch, not full invisibility.

---

## 4. Config

`peering` config section, new sub-block:

```yaml
peering:
  # ...existing host / peers / max_dynamic_peers ...
  connectivity:
    # lppConnectivity protocol: publish & gossip this node's peer-RTT view for
    # network-mapping. Operational overlay only, never a consensus input.
    # Enabled by default; set to true to opt out.
    disable: false      # default false (= enabled)
```

- **`disable` key (not `enable`)** so that a missing/zero value means *enabled* —
  the intended default — with no special-casing. `viper.GetBool` returns `false`
  for a missing key, which is exactly "not disabled".
- `Config.ConnectivityDisabled bool = viper.GetBool("peering.connectivity.disable")`
  in `readPeeringConfig()`. Everything downstream gates on
  `!cfg.ConnectivityDisabled`. Log the resolved state at startup like the other
  peering flags (`peers.go:144`).

---

## 5. API — `/get_connectivity_map`

A new read-only endpoint (`api/api.go` path const
`PathGetConnectivityMap = PrefixAPIV1 + "/get_connectivity_map"`) serving the
whole stored map as JSON. Implemented via a `Peers.GetConnectivityMap()` accessor
(mirrors `GetPeersInfo`, `peers.go:626`).

```jsonc
{
  "self":        "a1b2c3d4e5f60718",          // this node's own masked name ("" if not yet known)
  "captured_at": 1718900000000000000,          // unix nanos, server clock at response time
  "records": [
    {
      "name":                  "a1b2c3d4e5f60718",
      "consensusContribution": 123456789,         // omitted/0 for access nodes
      "byPeer":    { "00ff00ff00ff00ff": 1850, "1122334455667788": 42000 },
      "timestamp": 1718900000000000000,         // origin emit time
      "seq":       12345,
      "age_ms":    420                           // captured_at - whenReceived; freshness for the consumer
    }
    // ... one per known origin, including "self"
  ]
}
```

- Raw masked-name hex only — **no IPs, no display decoration** (consistent with
  `feedback_api_raw_ids`: formatting is the UI's job).
- `age_ms` lets the consumer weight/drop stale edges (`network_rtt_mapping.md`
  §5 "freshness / staleness").
- Disabled node: endpoint still serves whatever it has (its own `self` empty,
  `records` may be empty). Optionally return `503`/empty when disabled — decide
  at implementation; serving empty is simpler.

---

## 6. Consumer (out of scope here, see `network_rtt_mapping.md`)

A browser / `proxi util netmap` downloads `/get_connectivity_map` from any one
node and stitches the global directed RTT graph by matching `byPeer` keys to
`records[].name` (both are the same masked-name space — §1's consistency
property is what makes this work). From there it is exactly
`network_rtt_mapping.md` §5–§9: metric closure, mass annotation, force-directed
layout, Monte-Carlo `P_succ(T)`.

This protocol provides **adjacency + RTT + self-reported mass**
(`consensusContribution`) in one download, so the basic vis/sim need no second
data source. For a *trustless* mass the consumer recomputes `balance +
frozenCoverage` from LRB ledger state (`network_rtt_mapping.md` §6); the
peerID↔seqID binding is **not** part of lppConnectivity (masked names are
IP-based, anonymous by design — the mass is attached to a pseudonymous vertex).

---

## 7. Files to touch (implementation checklist)

| File | Change |
|---|---|
| `global/types.go` | add `ConsensusContribution() uint64` to the `NodeGlobal` interface (§2a). |
| node global impl (`global/` / `node/`) | implement `ConsensusContribution()`: 0 if no sequencer, else `tokenBalance + frozenCoverage(0)` of the sequencer's own latest milestone. |
| `peering/types.go` | `Config.ConnectivityDisabled`; `Peers.lppProtocolConnectivity`; `connMap` + its mutex; `lppProtocolConnectivity` template const. |
| `peering/peers.go` | format protocol ID in `New()`; add to `streams` maps in `_addPeer`/`dialPeer`; register handler + start emit loop in `Run()` (guarded by `!ConnectivityDisabled`); `GetConnectivityMap()`; read+log config in `readPeeringConfig()`. |
| `peering/connectivity.go` (new) | `PeerConnections`, `connectivityStreamHandler`, emit loop (calls `ps.environment.ConsensusContribution()`), receive+gossip logic, masked-name helpers (`maskedName(ip,port)`, own-name-from-observed-addrs). |
| `api/api.go` | `PathGetConnectivityMap` const + response struct. |
| `api/server/*` | register handler calling `peers.GetConnectivityMap()`. |
| config docs / sample YAMLs | `peering.connectivity.disable`. |

---

## 8. Decisions locked (2026-06-20)

1. Masked name = `blake2b256(ip16 || portBE)[:8]` — **IP:port**, not IP alone.
2. Record `name` = the origin's **own masked name** (not `blake2b(masked name)`;
   the draft's extra hash would break stitching).
3. Own IP:port for the self masked name = **libp2p observed addresses**.
4. RTT value = **existing libp2p-ping `lastRTTNs`**, converted to microseconds.
5. Config key = **`peering.connectivity.disable`** (default false ⇒ enabled), so
   missing/zero means enabled with no special-casing.
6. `consensusContribution` is sourced via a **`ConsensusContribution() uint64`
   method on the node-global interface** (0 for access nodes), keeping `peering`
   ledger-free.

## 9. Open questions

- Forward gate keyed on `whenReceived` (local) vs origin `timestamp` — local is
  used here; revisit if clock skew across nodes distorts the 10 s window.
- Should a disabled node return `503` from `/get_connectivity_map`, or serve its
  (relayed-only) records? Leaning serve-what-we-have.
- ~~Bound on `connMap` size / eviction of long-silent origins.~~ **Done:** TTL
  eviction (`connectivityEntryTTL = 1 min`) via the `connectivity_evict_loop`
  background loop (§3b).
- Whether to fold a coarse/none disclosure level (`network_rtt_mapping.md` §11)
  into the same `connectivity` config block later, vs the binary enable here.
