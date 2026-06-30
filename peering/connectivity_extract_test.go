package peering

import (
	"testing"

	"github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func mustAddr(t *testing.T, s string) multiaddr.Multiaddr {
	a, err := multiaddr.NewMultiaddr(s)
	require.NoError(t, err)
	return a
}

// TestExtractIPPortCanonicalDualStack verifies that for a dual-stack node the
// canonical address is the IPv4 one regardless of the order the addresses appear
// in — so every observer derives the same masked name and the node never gets an
// IPv6 phantom twin on the connectivity map.
func TestExtractIPPortCanonicalDualStack(t *testing.T) {
	v4 := mustAddr(t, "/ip4/79.137.70.25/udp/4000/quic-v1")
	v6 := mustAddr(t, "/ip6/2a01:4f9:3071:1aa5::2/udp/4000/quic-v1")

	// IPv6 listed first must not win.
	ip, port, ok := extractIPPort([]multiaddr.Multiaddr{v6, v4}, false)
	require.True(t, ok)
	require.Equal(t, "79.137.70.25", ip.String())
	require.Equal(t, uint16(4000), port)

	// Same set, IPv4 first — identical result (order-independent).
	ip2, port2, ok2 := extractIPPort([]multiaddr.Multiaddr{v4, v6}, false)
	require.True(t, ok2)
	require.Equal(t, ip.String(), ip2.String())
	require.Equal(t, port, port2)
}

// TestExtractIPPortDeterministicMultiHomed verifies that among several public
// IPv4 addresses the lowest one is chosen deterministically, independent of order.
func TestExtractIPPortDeterministicMultiHomed(t *testing.T) {
	lo := mustAddr(t, "/ip4/8.8.8.8/udp/4000/quic-v1")
	hi := mustAddr(t, "/ip4/79.137.70.25/udp/4000/quic-v1")

	for _, order := range [][]multiaddr.Multiaddr{{hi, lo}, {lo, hi}} {
		ip, _, ok := extractIPPort(order, false)
		require.True(t, ok)
		require.Equal(t, "8.8.8.8", ip.String(), "lowest IP must win regardless of order")
	}
}

// TestExtractIPPortPrivateSkipped verifies private/loopback addresses are ignored
// when allowLocal is false (production default), and only the public address is used.
func TestExtractIPPortPrivateSkipped(t *testing.T) {
	priv := mustAddr(t, "/ip4/10.1.2.3/udp/4000/quic-v1")
	pub := mustAddr(t, "/ip4/79.137.70.25/udp/4000/quic-v1")

	ip, _, ok := extractIPPort([]multiaddr.Multiaddr{priv, pub}, false)
	require.True(t, ok)
	require.Equal(t, "79.137.70.25", ip.String())

	// With no public address and allowLocal=false, nothing is returned.
	_, _, ok = extractIPPort([]multiaddr.Multiaddr{priv}, false)
	require.False(t, ok)
	// allowLocal=true falls back to the private address.
	_, _, ok = extractIPPort([]multiaddr.Multiaddr{priv}, true)
	require.True(t, ok)
}
