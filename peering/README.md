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
dials up to the `peering.max_dynamic_peers` cap. A dial in flight reserves its
slot before it completes, so a tick landing while earlier dials are still
connecting cannot start a second full batch.

A connection alone admits no one. A node that comes to us without being dialled
(it lists us as a static peer, or found us through the DHT) is admitted as a
dynamic peer when it opens a gossip, pull or connectivity stream to us, which a
node does only to peers it counts. The DHT keeps connections of its own and
never opens one of those streams, so its connections take no slot. Admitting on
connection let them take slots on one side only: links that carried nothing in
either direction and could fill a small node's slots. Admission is what makes a
newcomer hear anything, since gossip and connectivity records go to registered
peers only.

A quarter of the `max_dynamic_peers` slots (rounded down) is kept for peers the
node dials; inbound peers may take the rest, so they can never be all of its
links. A positive cap below 4 is raised to 4. When the inbound share is full,
one existing inbound dynamic peer is churned out to admit the newcomer.
Outbound (autopeered) peers are protected: the node chose to dial
them, so a stream of fresh inbound identities cannot replace them. Among inbound
peers the least active goes first. Activity is measured on transactions that
parsed and passed the signature check plus pull requests, not on raw gossip
frames: a peer on an incompatible transaction version cannot look active by
flooding transactions that are refused at parsing. Churn is limited to one
eviction per minute, and a peer admitted within the grace period (2 min) is
never the victim, so a newcomer cannot displace the peer it just replaced. A
peer that was evicted for churn, the newcomer that caused the eviction, and a
peer that disappeared within 5 s of admission are all barred from triggering
another churn; the first two for 10 minutes, the last for 1 hour. Either way an
inbound connection is still admitted when a slot is free, but autopeering does
not redial the peer until the cooldown expires, and it cannot evict a working
peer on its next redial. Without that bar a small set of redialling peers keeps
the set rotating, and a peer that connects and immediately vanishes (for example
one on another ledger that closes before the protocol check) keeps displacing
healthy peers. A peer that keeps sending transactions from another version is
dropped after 10 mismatches in a minute and receives the same 1-hour cooldown.
If every inbound peer is still inside its grace period, no inbound victim exists,
or the newcomer is in churn cooldown, its stream is reset and the connection is
kept: the newcomer keeps its discovery DHT through us and finds peers with free
slots elsewhere. The connection manager's high watermark (static + cap + 5)
remains as a backstop.

Gossip, pull requests and connectivity records are accepted only from a
registered peer, from one admitted by that stream, or from one whose outbound
dial is still in flight. A connection alone does not admit a peer, so gossip over
a DHT connection never bypasses `max_dynamic_peers`.

A dynamic peer that sends nothing for 5 minutes is dropped, with the usual
10-minute cooldown. A peer that counts us sends connectivity records every 15 s
and relays every slot's sequencer transactions, so that silence means the other
side does not count us: it refused our streams or never admitted us, and the link
would otherwise hold a slot on our side forever. The margin covers a peer that
sends no connectivity records while the network is quiet. Static peers are never
dropped.

A refused stream is reset, and the sender only finds out on its next write, so
without a limit it would open a new stream for every message. A stream to one peer
and protocol reopened more than 3 times within 10 s is paused instead: 1 s at
first, doubling while the refusals go on, up to 30 s, and back to no pause after
a quiet 10 s. Messages in a pause are dropped and counted
(`proxima_peering_outMsgPaused`); the refusing side counts what it refused
(`proxima_peering_streamsRefused`). This is what a node sees when a static peer of
its own does not count it back.

Autopeering is off when `max_dynamic_peers` is at or below zero, in which case
the node accepts no incoming dynamic peers either.

## Configuration

| Key | Effect |
|-----|--------|
| `peering.host.port` | UDP port |
| `peering.peers` | Preconfigured (static) peers, also the DHT bootstrap set |
| `peering.max_dynamic_peers` | Cap on dynamic peers (static peers do not count); ≤ 0 disables autopeering and refuses all inbound dynamic peers |
| `peering.ignore_all_pull_requests` | Serve no pull requests at all |
| `peering.accept_pull_requests_from_static_peers_only` | Serve pulls only to static peers |

## Also here

- [`network_connectivity.md`](network_connectivity.md) — the connectivity-gossip
  protocol and the connectivity map it builds.
- [`../core/resilience.md`](../core/resilience.md) — the peering-level gates in
  the context of every other gate on the transaction path.
- [`kb/archive/superseded/peering_refactor.md`](../kb/archive/superseded/peering_refactor.md)
  — the 2026 pre-refactor assessment. Superseded (it predates the heartbeat
  removal), but it records why several designs were rejected.
