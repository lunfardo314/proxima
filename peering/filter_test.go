package peering

import (
	"net"
	"testing"

	"github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

// addrSet collects the string forms of a multiaddr slice for order-independent assertions.
func addrSet(addrs []multiaddr.Multiaddr) map[string]struct{} {
	m := make(map[string]struct{}, len(addrs))
	for _, a := range addrs {
		m[a.String()] = struct{}{}
	}
	return m
}

// TestFilterAddressesExternalIPs verifies that an operator-declared external IP is
// advertised at the peering port as a /ip4/.../udp/<port>/quic-v1 transport address
// (no /p2p/ component — libp2p attaches the host ID). This is the NAT/port-mapping
// path where the public IP is on no local interface.
func TestFilterAddressesExternalIPs(t *testing.T) {
	const port = 4000
	f := FilterAddresses(false, port, []net.IP{net.ParseIP("9.9.9.9")})

	got := addrSet(f(nil))
	_, ok := got["/ip4/9.9.9.9/udp/4000/quic-v1"]
	require.True(t, ok, "declared external IP must be advertised at the peering port")
}

// TestFilterAddressesDropsPrivate verifies the public-only filter drops private/reserved
// external IPs when allowLocalNetworks is false (the production default), so a misconfigured
// private address is never advertised.
func TestFilterAddressesDropsPrivate(t *testing.T) {
	const port = 4000
	f := FilterAddresses(false, port, []net.IP{net.ParseIP("10.1.2.3"), net.ParseIP("9.9.9.9")})

	got := addrSet(f(nil))
	_, hasPrivate := got["/ip4/10.1.2.3/udp/4000/quic-v1"]
	require.False(t, hasPrivate, "private external IP must be filtered out")
	_, hasPublic := got["/ip4/9.9.9.9/udp/4000/quic-v1"]
	require.True(t, hasPublic, "public external IP must survive")
}

// TestFilterAddressesDedup verifies that an address already present in libp2p's
// discovered set is not duplicated when the same IP is also declared/derived.
func TestFilterAddressesDedup(t *testing.T) {
	const port = 4000
	existing, err := multiaddr.NewMultiaddr("/ip4/9.9.9.9/udp/4000/quic-v1")
	require.NoError(t, err)

	f := FilterAddresses(false, port, []net.IP{net.ParseIP("9.9.9.9")})

	out := f([]multiaddr.Multiaddr{existing})
	count := 0
	for _, a := range out {
		if a.String() == "/ip4/9.9.9.9/udp/4000/quic-v1" {
			count++
		}
	}
	require.Equal(t, 1, count, "the address must appear exactly once after dedup")
}
