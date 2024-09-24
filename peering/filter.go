package peering

import (
	"fmt"

	"github.com/iotaledger/hive.go/lo"
	"github.com/multiformats/go-multiaddr"
	mamask "github.com/whyrusleeping/multiaddr-filter"
)

// copied from https://github.com/iotaledger/iota-core
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
type ExternalMultiAddresses []string

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
		return lo.Filter(addresses, func(m multiaddr.Multiaddr) bool {
			return !filters.AddrBlocked(m)
		})
	}
}

func FilterAddresses(allowLocalNetworks bool) AddressFilter {
	var externalMultiAddrs []multiaddr.Multiaddr

	publicFilter := publicOnlyAddressesFilter(allowLocalNetworks)

	return func(addresses []multiaddr.Multiaddr) []multiaddr.Multiaddr {
		return publicFilter(append(addresses, externalMultiAddrs...))
	}
}
