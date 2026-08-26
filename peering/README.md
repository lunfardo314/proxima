# peering

The node's P2P layer: how transactions reach other nodes and how missing ones
are fetched back. Built on libp2p.

The whole external surface used by the rest of the node is four calls —
`GossipTxBytesToPeers`, `PullTransactionsFromPeers`, and the two callback
registrations `OnReceiveTxBytes` / `OnReceivePullTxRequest`. Everything else in
here is in service of those.

## Transport and protocols

libp2p QUIC-v1 over UDP, with no TLS (`libp2p.NoSecurity`) and no relay
(`libp2p.DisableRelay`).

Three application protocols, each with a name suffix derived from the ledger
library hash:

| Protocol | Carries | File |
|----------|---------|------|
| `/proxima/gossip/<hash>` | Transaction broadcast | `txbytes.go` |
| `/proxima/pull/<hash>` | Transaction request by ID | `pull.go` |
| `/proxima/connectivity/<hash>` | Peer-connectivity gossip | `connectivity.go` |

**The hash suffix is a gate, not decoration.** Nodes running different ledger
versions have no protocol in common, so they cannot talk at all — an upgrade
mismatch is a clean partition rather than a stream of mutual validation
failures.

Wire format is a hand-written 4-byte big-endian length prefix plus payload
(`misc.go`, `readFrame` / `writeFrame`), capped at `MaxPayloadSize` = 65,531
bytes. That cap is the reason a transaction larger than ~64 KB cannot be
gossiped at all.

Each peer holds one long-lived cached stream per protocol. Per-message outbound
streams were tried and rejected: multistream-select negotiation costs about one
round trip per message, which is fatal to gossip latency on transcontinental
links. Write failures redial once and retry rather than tearing the peer down.

## Peers

`Peers` (`types.go`) tracks all currently connected peers, with preconfigured
**static** peers held separately so they can be re-dialled after they cycle
through the blacklist or cool-off lists. A blacklist and a cool-off list, both
TTL-bound, keep the node from hammering peers that misbehaved or just went away;
an in-progress dial set prevents reentrant dials.

**Liveness is libp2p's**, not a protocol of ours: a peer is alive when
`host.Network().Connectedness(id) == network.Connected`. The node counts itself
connected to the network while at least one peer is alive. There was once a
heartbeat protocol carrying liveness, capability flags and a clock sample; it
was removed, and the traffic-timestamp proxy that briefly replaced it proved too
quiet on a low-load network — two just-restarted nodes with nothing to gossip
looked disconnected.

## Discovery

Kademlia DHT in `dht.ModeAutoServer`, bootstrapped from the static peer list,
with a rendezvous string derived from the first 8 bytes of the genesis ledger
library hash. An autopeering loop runs every 3 seconds, asks for up to 20 peers,
filters out those already known, blacklisted, cooling off or being dialled, and
dials up to the `peering.max_dynamic_peers` cap. Excess dynamic peers are
trimmed one at a time, skipping any inside their grace period.

Autopeering is off when `max_dynamic_peers` is at or below the number of
preconfigured peers, in which case the node accepts no incoming dynamic peers
either.

## Configuration

| Key | Effect |
|-----|--------|
| `peering.host.port` | UDP port |
| `peering.peers` | Preconfigured (static) peers, also the DHT bootstrap set |
| `peering.max_dynamic_peers` | Autopeering cap; ≤ the static count disables autopeering |
| `peering.ignore_all_pull_requests` | Serve no pull requests at all |
| `peering.accept_pull_requests_from_static_peers_only` | Serve pulls only to static peers |

## Also here

- [`network_connectivity.md`](network_connectivity.md) — the connectivity-gossip
  protocol and the connectivity map it builds.
- [`../core/resilience.md`](../core/resilience.md) — the peering-level gates in
  the context of every other gate on the transaction path.
- [`claude/archive/superseded/peering_refactor.md`](../claude/archive/superseded/peering_refactor.md)
  — the 2026 pre-refactor assessment. Superseded (it predates the heartbeat
  removal), but it records why several designs were rejected.
