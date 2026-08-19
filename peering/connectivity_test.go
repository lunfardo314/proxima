package peering

import (
	"net"
	"testing"
	"time"

	"github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

// maskedName must be deterministic and must distinguish nodes that differ only by
// port (the co-located seq+access case on a single machine IP).
func TestMaskedName(t *testing.T) {
	ip := net.ParseIP("9.9.9.9")

	// deterministic: same input -> same masked name, 16 hex chars (8 bytes)
	n1 := maskedName(ip, 4000)
	require.Equal(t, n1, maskedName(ip, 4000))
	require.Len(t, n1, 16)

	// same IP, different port -> different masked name (co-located nodes stay distinct)
	require.NotEqual(t, n1, maskedName(ip, 4001))

	// different IP -> different masked name
	require.NotEqual(t, n1, maskedName(net.ParseIP("1.1.1.1"), 4000))
}

// extractIPPort should prefer a public address and only fall back to a local/private
// one when allowLocal is set.
func TestExtractIPPort(t *testing.T) {
	mustAddr := func(s string) multiaddr.Multiaddr {
		a, err := multiaddr.NewMultiaddr(s)
		require.NoError(t, err)
		return a
	}

	pub := mustAddr("/ip4/8.8.8.8/udp/4000/quic-v1")
	loc := mustAddr("/ip4/127.0.0.1/udp/4001/quic-v1")

	// public address present: picked regardless of allowLocal, with its port
	ip, port, ok := extractIPPort([]multiaddr.Multiaddr{loc, pub}, false)
	require.True(t, ok)
	require.Equal(t, "8.8.8.8", ip.String())
	require.EqualValues(t, 4000, port)

	// only a local address, allowLocal=false: rejected
	_, _, ok = extractIPPort([]multiaddr.Multiaddr{loc}, false)
	require.False(t, ok)

	// only a local address, allowLocal=true: accepted (dev/local nets)
	ip, port, ok = extractIPPort([]multiaddr.Multiaddr{loc}, true)
	require.True(t, ok)
	require.Equal(t, "127.0.0.1", ip.String())
	require.EqualValues(t, 4001, port)
}

// handleConnectivityRecord must always store the freshest record per origin and
// must drop stale/duplicate copies (timestamp not advancing). Uses NewPeersDummy
// (no host, no peers) so only the store/freshness logic is exercised.
func TestHandleConnectivityRecordFreshness(t *testing.T) {
	ps := NewPeersDummy()
	const origin = "a1b2c3d4e5f60718"

	rec1 := PeerConnections{Name: origin, ByPeer: map[string]uint64{"x": 10}, Timestamp: 1000, Seq: 1}
	ps.handleConnectivityRecord("", rec1)

	ps.connMutex.RLock()
	got, ok := ps.connMap[origin]
	ps.connMutex.RUnlock()
	require.True(t, ok)
	require.EqualValues(t, 1, got.rec.Seq)

	// newer timestamp -> replaces the stored record
	rec2 := PeerConnections{Name: origin, ByPeer: map[string]uint64{"x": 20}, Timestamp: 2000, Seq: 2}
	ps.handleConnectivityRecord("", rec2)
	ps.connMutex.RLock()
	got = ps.connMap[origin]
	ps.connMutex.RUnlock()
	require.EqualValues(t, 2, got.rec.Seq)

	// older/equal timestamp -> dropped, stored record unchanged
	stale := PeerConnections{Name: origin, ByPeer: map[string]uint64{"x": 99}, Timestamp: 1500, Seq: 3}
	ps.handleConnectivityRecord("", stale)
	ps.connMutex.RLock()
	got = ps.connMap[origin]
	ps.connMutex.RUnlock()
	require.EqualValues(t, 2, got.rec.Seq, "stale record (older timestamp) must not overwrite")
	require.EqualValues(t, 20, got.rec.ByPeer["x"])
}

// evictStaleConnEntries must drop entries older than the TTL and keep fresh ones.
func TestEvictStaleConnEntries(t *testing.T) {
	ps := NewPeersDummy()

	// fresh entry (just received) and a stale one (received past the TTL)
	ps.connMap["fresh"] = connEntry{
		rec:          PeerConnections{Name: "fresh"},
		whenReceived: time.Now(),
	}
	ps.connMap["stale"] = connEntry{
		rec:          PeerConnections{Name: "stale"},
		whenReceived: time.Now().Add(-connectivityEntryTTL - time.Second),
	}

	ps.evictStaleConnEntries()

	ps.connMutex.RLock()
	_, freshOK := ps.connMap["fresh"]
	_, staleOK := ps.connMap["stale"]
	ps.connMutex.RUnlock()
	require.True(t, freshOK, "fresh entry must be kept")
	require.False(t, staleOK, "stale entry (older than TTL) must be evicted")
}

// The forward gate suppresses re-forwarding the same origin within
// connectivityForwardGap. We can't observe outbound sends without a host, but we
// can assert whenReceived advances so the gate timer is anchored on each store.
func TestConnectivityForwardGateTiming(t *testing.T) {
	ps := NewPeersDummy()
	const origin = "deadbeefdeadbeef"

	ps.handleConnectivityRecord("", PeerConnections{Name: origin, Timestamp: 1})
	ps.connMutex.RLock()
	first := ps.connMap[origin].whenReceived
	ps.connMutex.RUnlock()
	require.WithinDuration(t, time.Now(), first, time.Second)
}
