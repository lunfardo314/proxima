package peering

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net"
	"strconv"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/api"
	"github.com/multiformats/go-multiaddr"
	"golang.org/x/crypto/blake2b"
)

// lppConnectivity is an operational network-mapping overlay. Every enabled node
// periodically publishes, and floods to the rest of the network, its local view
// of peer round-trip times keyed by privacy-preserving masked names. Any node can
// then serve the whole network's adjacency via /get_connectivity_map.
//
// It is NOT a consensus input: masked names and RTTs are self-reported in gossiped
// records and a node can lie; the map informs human/parameter decisions only.
// Spec: claude/network_connectivity.md.

const (
	connectivityEmitInterval = 15 * time.Second // how often a node emits its own record
	connectivityForwardGap   = 10 * time.Second // min interval between re-forwards of one origin (anti-cycle)
	connectivityEntryTTL     = 1 * time.Minute  // evict records not refreshed within this window
)

type (
	// PeerConnections is one node's local connectivity view, gossiped verbatim.
	PeerConnections struct {
		Name                  string            `json:"name"`                            // origin masked name, 16-hex
		ConsensusContribution uint64            `json:"consensusContribution,omitempty"` // sequencer mass; 0/omitted for access nodes
		ByPeer                map[string]uint64 `json:"byPeer"`                          // peer masked name (16-hex) -> RTT microseconds
		Timestamp             int64             `json:"timestamp"`                       // unix nanos at origin
		Seq                   uint64            `json:"seq"`                             // origin-monotone sequence number
	}

	// connEntry is the latest received record for one origin plus local receipt time.
	connEntry struct {
		rec          PeerConnections
		whenReceived time.Time
	}
)

// maskedName is the 8-byte blake2b of the node's externally-reachable IP:port,
// hex-encoded. Stable per running node; distinguishes co-located nodes by port.
func maskedName(ip net.IP, port uint16) string {
	var buf [18]byte
	copy(buf[:16], ip.To16())
	binary.BigEndian.PutUint16(buf[16:], port)
	h := blake2b.Sum256(buf[:])
	return hex.EncodeToString(h[:8])
}

// extractIPPort picks the externally-reachable IP:port from a libp2p multiaddr
// set: prefers a public (global-unicast, non-private) IPv4/IPv6 + UDP address;
// falls back to a local/private one only when allowLocal (autopeering on local
// IPs, dev nets). Returns ok=false when no usable address is present yet.
func extractIPPort(addrs []multiaddr.Multiaddr, allowLocal bool) (net.IP, uint16, bool) {
	var fbIP net.IP
	var fbPort uint16
	haveFb := false
	for _, a := range addrs {
		ipStr, err := a.ValueForProtocol(multiaddr.P_IP4)
		if err != nil {
			if ipStr, err = a.ValueForProtocol(multiaddr.P_IP6); err != nil {
				continue
			}
		}
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		portStr, err := a.ValueForProtocol(multiaddr.P_UDP)
		if err != nil {
			continue
		}
		p, err := strconv.Atoi(portStr)
		if err != nil {
			continue
		}
		port := uint16(p)
		if ip.IsGlobalUnicast() && !ip.IsPrivate() {
			return ip, port, true
		}
		if !haveFb {
			fbIP, fbPort, haveFb = ip, port, true
		}
	}
	if allowLocal && haveFb {
		return fbIP, fbPort, true
	}
	return nil, 0, false
}

// ownMaskedName derives this node's masked name from its observed/listen
// addresses. Returns "" until libp2p knows a usable external address.
func (ps *Peers) ownMaskedName() string {
	if ip, port, ok := extractIPPort(ps.host.Addrs(), ps.cfg.AllowLocalIPs); ok {
		return maskedName(ip, port)
	}
	return ""
}

// emitConnectivity builds and sends this node's own PeerConnections record to all
// alive peers, and stores it as the local "self" entry. Skips emission until the
// node's own external address (hence masked name) is known.
func (ps *Peers) emitConnectivity() {
	self := ps.ownMaskedName()
	if self == "" {
		ps.Log().Debugf("[connectivity] own external address not known yet; skip emit")
		return
	}

	byPeer := make(map[string]uint64)
	ps.forEachPeerRLock(func(p *Peer) bool {
		if !ps._isAlive(p) {
			return true
		}
		rtt := p.lastRTTNs.Load()
		if rtt <= 0 {
			return true
		}
		if ip, port, ok := extractIPPort(ps.host.Peerstore().Addrs(p.id), ps.cfg.AllowLocalIPs); ok {
			byPeer[maskedName(ip, port)] = uint64(rtt) / 1000 // ns -> microseconds
		}
		return true
	})

	contribution := ps.environment.ConsensusContribution()
	now := time.Now()

	ps.connMutex.Lock()
	ps.ownConnSeq++
	rec := PeerConnections{
		Name:                  self,
		ConsensusContribution: contribution,
		ByPeer:                byPeer,
		Timestamp:             now.UnixNano(),
		Seq:                   ps.ownConnSeq,
	}
	ps.connMap[self] = connEntry{rec: rec, whenReceived: now}
	ps.connMutex.Unlock()

	ps.sendConnectivity(ps.peerIDsAlive(), rec)
}

// evictStaleConnEntries drops records (including this node's own) not refreshed
// within connectivityEntryTTL. A live origin re-emits every connectivityEmitInterval,
// well inside the TTL, so only genuinely silent origins age out.
func (ps *Peers) evictStaleConnEntries() {
	ps.connMutex.Lock()
	defer ps.connMutex.Unlock()
	for name, e := range ps.connMap {
		if time.Since(e.whenReceived) > connectivityEntryTTL {
			delete(ps.connMap, name)
		}
	}
}

// connectivityStreamHandler receives PeerConnections records from a peer.
func (ps *Peers) connectivityStreamHandler(stream network.Stream) {
	defer func() { _ = stream.Close() }()

	src := stream.Conn().RemotePeer()
	known, _ := ps.knownPeer(src, nil)
	if !known && !ps.isAutopeeringEnabled() {
		// node does not take any incoming dynamic peers
		return
	}

	// receive start frame
	if _, err := readFrame(stream); err != nil {
		return
	}

	for {
		msg, err := readFrame(stream)
		if err != nil {
			return
		}
		ps.inMsgCounter.Inc()

		var rec PeerConnections
		if err = json.Unmarshal(msg, &rec); err != nil {
			ps.Log().Warnf("[connectivity] bad record from %s: %v", ShortPeerIDString(src), err)
			continue
		}
		if rec.Name == "" {
			continue
		}
		ps.evidenceMessage()
		ps.handleConnectivityRecord(src, rec)
	}
}

// handleConnectivityRecord stores the latest record for its origin and, subject to
// the per-origin forward gate, re-gossips it to all alive peers except the source.
func (ps *Peers) handleConnectivityRecord(src peer.ID, rec PeerConnections) {
	ps.connMutex.Lock()
	prev, existed := ps.connMap[rec.Name]
	if existed && rec.Timestamp <= prev.rec.Timestamp {
		// stale or duplicate (older/equal copy arriving via another gossip path)
		ps.connMutex.Unlock()
		return
	}
	// forward only if first time seen or the previous forward for this origin was
	// at least connectivityForwardGap ago — bounds re-forwarding, breaks cycles.
	forward := !existed || time.Since(prev.whenReceived) >= connectivityForwardGap
	ps.connMap[rec.Name] = connEntry{rec: rec, whenReceived: time.Now()}
	ps.connMutex.Unlock()

	if forward {
		ps.sendConnectivity(ps.peerIDsAlive(src), rec)
	}
}

// sendConnectivity marshals the record to JSON and sends it to the given peers.
func (ps *Peers) sendConnectivity(ids []peer.ID, rec PeerConnections) {
	if len(ids) == 0 {
		return
	}
	data, err := json.Marshal(rec)
	if err != nil {
		ps.Log().Warnf("[connectivity] marshal record: %v", err)
		return
	}
	ps.sendMsgBytesOutMulti(ids, ps.lppProtocolConnectivity, data)
}

// GetConnectivityMap returns the whole stored connectivity map for the API.
func (ps *Peers) GetConnectivityMap() *api.ConnectivityMap {
	now := time.Now()
	ret := &api.ConnectivityMap{
		Self:       ps.ownMaskedName(),
		CapturedAt: now.UnixNano(),
		Records:    make([]api.ConnectivityRecord, 0),
	}

	ps.connMutex.RLock()
	defer ps.connMutex.RUnlock()

	for _, e := range ps.connMap {
		ret.Records = append(ret.Records, api.ConnectivityRecord{
			Name:                  e.rec.Name,
			ConsensusContribution: e.rec.ConsensusContribution,
			ByPeer:                e.rec.ByPeer,
			Timestamp:             e.rec.Timestamp,
			Seq:                   e.rec.Seq,
			AgeMs:                 now.Sub(e.whenReceived).Milliseconds(),
		})
	}
	return ret
}
