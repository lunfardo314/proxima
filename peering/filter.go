package peering

import (
	"fmt"
	"net"

	"github.com/lunfardo314/proxima/util"
	"github.com/multiformats/go-multiaddr"
	mamask "github.com/whyrusleeping/multiaddr-filter"
)

// Based on https://github.com/ipfs/kubo/blob/master/config/profile.go
// defaultServerFilters has is a list of IPv4 and IPv6 prefixes that are private, local only, or unrouteable.
// according to https://www.iana.org/assignments/iana-ipv4-special-registry/iana-ipv4-special-registry.xhtml
// and https://www.iana.org/assignments/iana-ipv6-special-registry/iana-ipv6-special-registry.xhtml
var reservedFilters = []string{
	"/ip4/0.0.0.0/ipcidr/32",
	"/ip4/100.64.0.0/ipcidr/10",
	"/ip4/127.0.0.0/ipcidr/8",
	"/ip4/169.254.0.0/ipcidr/16",
	"/ip4/192.0.0.0/ipcidr/24",
	"/ip4/192.0.2.0/ipcidr/24",
	"/ip4/192.31.196.0/ipcidr/24",
	"/ip4/192.52.193.0/ipcidr/24",
	"/ip4/198.18.0.0/ipcidr/15",
	"/ip4/198.51.100.0/ipcidr/24",
	"/ip4/203.0.113.0/ipcidr/24",
	"/ip4/240.0.0.0/ipcidr/4",

	"/ip6/::/ipcidr/128",
	"/ip6/::1/ipcidr/128",
	"/ip6/100::/ipcidr/64",
	"/ip6/2001:2::/ipcidr/48",
	"/ip6/2001:db8::/ipcidr/32",
}

var localNetworks = []string{
	"/ip4/10.0.0.0/ipcidr/8",
	"/ip4/172.16.0.0/ipcidr/12",
	"/ip4/192.168.0.0/ipcidr/16",

	"/ip6/fc00::/ipcidr/7",
	"/ip6/fe80::/ipcidr/10",
}

type AddressFilter = func([]multiaddr.Multiaddr) []multiaddr.Multiaddr

func publicOnlyAddressesFilter(allowLocalNetworks bool) AddressFilter {
	// Create a filter that blocks localhost and reserved addresses.
	filters := multiaddr.NewFilters()

	filtersToApply := reservedFilters
	if !allowLocalNetworks {
		filtersToApply = append(filtersToApply, localNetworks...)
	}

	for _, addr := range filtersToApply {
		f, err := mamask.NewMask(addr)
		if err != nil {
			panic(fmt.Sprintf("unable to parse ip mask filter %s: %s", addr, err))
		}
		filters.AddFilter(*f, multiaddr.ActionDeny)
	}

	return func(addresses []multiaddr.Multiaddr) []multiaddr.Multiaddr {
		return util.PurgeSlice(addresses, func(m multiaddr.Multiaddr) bool {
			return !filters.AddrBlocked(m)
		})
	}
}

// FilterAddresses builds the host's advertised address set (libp2p AddrsFactory).
// On top of whatever libp2p discovered, the node advertises itself at hostPort on:
//   - every local network interface IP, and
//   - every operator-declared external IP (peering.host.external_addresses).
// The union is deduplicated and reduced to public addresses only (unless
// allowLocalNetworks). The interface IPs cover the common case where the public
// IP is bound to the host's NIC; declared external IPs cover NAT/port-mapping,
// where the public IP is on no local interface and cannot be auto-detected.
func FilterAddresses(allowLocalNetworks bool, hostPort int, externalIPs []net.IP) AddressFilter {
	publicFilter := publicOnlyAddressesFilter(allowLocalNetworks)

	return func(addresses []multiaddr.Multiaddr) []multiaddr.Multiaddr {
		advertised := append([]multiaddr.Multiaddr{}, addresses...)
		for _, ip := range append(localInterfaceIPs(), externalIPs...) {
			if a := udpQuicMultiaddr(ip, hostPort); a != nil {
				advertised = append(advertised, a)
			}
		}
		return dedupMultiaddrs(publicFilter(advertised))
	}
}

// localInterfaceIPs returns the IPs assigned to this host's network interfaces.
func localInterfaceIPs() []net.IP {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	ret := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok {
			ret = append(ret, ipnet.IP)
		}
	}
	return ret
}

// udpQuicMultiaddr builds /ip{4,6}/<ip>/udp/<port>/quic-v1, or nil for an invalid IP.
func udpQuicMultiaddr(ip net.IP, port int) multiaddr.Multiaddr {
	proto := "ip6"
	if ip.To4() != nil {
		proto = "ip4"
	}
	a, err := multiaddr.NewMultiaddr(fmt.Sprintf("/%s/%s/udp/%d/quic-v1", proto, ip, port))
	if err != nil {
		return nil
	}
	return a
}

func dedupMultiaddrs(addrs []multiaddr.Multiaddr) []multiaddr.Multiaddr {
	seen := make(map[string]struct{}, len(addrs))
	ret := addrs[:0]
	for _, a := range addrs {
		if _, ok := seen[a.String()]; ok {
			continue
		}
		seen[a.String()] = struct{}{}
		ret = append(ret, a)
	}
	return ret
}
