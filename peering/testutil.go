package peering

import (
	"fmt"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/util"
	"github.com/multiformats/go-multiaddr"
)

const BeginPort = 4000
const TestBlacklistTTL = 20 // ms

var (
	allPrivateKeys = []string{
		"136381bd4dea7b2f0fa6c6ca14ee660dc7929975b47e6cdb85adee9e26032a810530b790e0e7de626f36e20ba656f09e324c68203de899d0daf9f98fcb3cf684",
		"a66f8768224b241fe4dd2e4c00132d029452468846b149f2c2c4d894805964c8e63df08176c9443f0fe015d3582635d3d5a02e33765fbb3a2995db9cf3bbe606",
		"3a2ed3c4f738a06e9543f80e032fd870be5e524585e0144f2499d1a28bc4797902e2a7a85bd158469adae3294a615e17ef49e72642c4eb58ff00d92301b9bb88",
		"34d39e13811f8cd25d78c0779ca3673e03710f50ecf9d723ea6c775ca88fcfd3b6bc2f88cb000d0fd63121d99bf57ab5c184cefe0ba89cf0f96ee60858696d78",
		"a8088c55a9827ddcd2207b373d7c01c4efb21a1ffdfd87fe973cccdbb897125a8601428a67a956d625889c264adae2b9e3e74b1f2bf3e3957eb7b1f78c6314aa",
	}
	hostID = []string{
		"12D3KooWAAdHwbjAoSf32Z2i4BEcd4DXkEmRuQcTSt45ddjbjgAF",
		"12D3KooWRK8iea53VnV99vK3wAEx2cCgfwhFJDE1sTbGA1W3Yppm",
		"12D3KooWA1dSQuxLZdNL5KhDFxHyzDNCA9zWVtAwPKZrnemZtvpw",
		"12D3KooWN7goUmGrAYyeK44k2L5jMGDYCPebnd3ZB6UGMdCDi9QT",
		"12D3KooWJqTuzwu16CVdERPnvBpzPgyVVs1efiTmEfBA8fhM9fSh",
	}
)

func MultiAddrString(i int, port int) string {
	return fmt.Sprintf("/ip4/127.0.0.1/udp/%d/quic-v1/p2p/%s", port, hostID[i])
}

func MakeConfigFor(n, hostIdx int) *Config {
	util.Assertf(n > 0 && n <= len(allPrivateKeys), "n > 0 && n <= len(allPrivateKeys)")
	util.Assertf(hostIdx >= 0 && hostIdx < n, "hostIdx >= 0 && hostIdx < n")

	pk := util.MustPrivateKeyFromHexString(allPrivateKeys[hostIdx])
	pklpp, err := crypto.UnmarshalEd25519PrivateKey(pk)
	util.AssertNoError(err)

	hid, err := peer.IDFromPrivateKey(pklpp)
	util.AssertNoError(err)

	cfg := &Config{
		HostIDPrivateKey:      pk,
		HostID:                hid,
		HostPort:              BeginPort + hostIdx,
		PreConfiguredPeers:    make(map[string]_multiaddr),
		ForcePullFromAllPeers: true,
		MaxDynamicPeers:       10, // allow dynamic peers
		BlacklistTTL:          TestBlacklistTTL,
		CooloffListTTL:        5,
		DisableQuicreuse:      true, // with quic reuse enabled quic cannot be properly shutdown and subsequent tests will fail
	}
	ids := hostID[:n]
	for i := range ids {
		if i == hostIdx {
			continue
		}
		addrString := MultiAddrString(i, BeginPort+i)
		ma, err := multiaddr.NewMultiaddr(addrString)
		util.AssertNoError(err)
		cfg.PreConfiguredPeers[fmt.Sprintf("peer%d", i)] = _multiaddr{
			addrString: addrString,
			Multiaddr:  ma,
		}
	}
	return cfg
}
