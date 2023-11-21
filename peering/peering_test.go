package peering

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/lunfardo314/proxima/util"
	"github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

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

func privateKeyFromString(k string) ed25519.PrivateKey {
	bin, err := hex.DecodeString(k)
	util.AssertNoError(err)
	return bin
}

func multiAddrString(i int, port int) string {
	return fmt.Sprintf("/ip4/127.0.0.1/tcp/%d/p2p/%s", port, hostID[i])
}

func makeConfigFor(t *testing.T, n, hostIdx int) *Config {
	require.True(t, n > 0 && n <= len(allPrivateKeys))
	require.True(t, hostIdx >= 0 && hostIdx < n)

	pk := privateKeyFromString(allPrivateKeys[hostIdx])
	cfg := &Config{
		HostIDPrivateKey: pk,
		HostIDPublicKey:  pk.Public().(ed25519.PublicKey),
		HostPort:         beginPort + hostIdx,
		KnownPeers:       make(map[string]multiaddr.Multiaddr),
	}
	ids := hostID[:n]
	for i := range ids {
		if i == hostIdx {
			continue
		}
		ma, err := multiaddr.NewMultiaddr(multiAddrString(i, beginPort+i))
		require.NoError(t, err)
		cfg.KnownPeers[fmt.Sprintf("peer%d", i)] = ma
	}
	return cfg
}

func TestGenData(t *testing.T) {
	t.Run("gen ma", func(t *testing.T) {
		for i, s := range allPrivateKeys {
			privKey, err := crypto.UnmarshalEd25519PrivateKey(privateKeyFromString(s))
			util.AssertNoError(err)
			host, err := libp2p.New(libp2p.Identity(privKey))
			util.AssertNoError(err)
			t.Logf("host %d: %s", i, host.ID().String())
		}
	})
	t.Run("multiaddr", func(t *testing.T) {
		const portStart = 4000
		for i := range hostID {
			t.Logf("%d: %s", i, multiAddrString(i, portStart+i))
		}
	})
}

const beginPort = 4000

func TestBasic(t *testing.T) {
	t.Run("1", func(t *testing.T) {
		const hostIndex = 2
		cfg := makeConfigFor(t, 5, hostIndex)
		t.Logf("host index: %d, host port: %d", hostIndex, beginPort+hostIndex)
		for name, ma := range cfg.KnownPeers {
			t.Logf("%s : %s", name, ma.String())
		}
		_, err := New(cfg)
		require.NoError(t, err)
	})
	t.Run("2", func(t *testing.T) {
		const hostIndex = 2
		cfg := makeConfigFor(t, 5, hostIndex)
		peers, err := New(cfg)
		require.NoError(t, err)
		peers.Run()
		peers.Stop()
	})
}

func TestHeartbeat(t *testing.T) {
	const (
		numHosts = 5
		trace    = false
	)
	peers := make([]*Peers, numHosts)
	var err error
	for i := 0; i < numHosts; i++ {
		cfg := makeConfigFor(t, numHosts, i)
		peers[i], err = New(cfg)
		require.NoError(t, err)
		peers[i].SetTrace(trace)
		peers[i].Run()
	}
	time.Sleep(2 * time.Second)
	for _, ps := range peers {
		for _, id := range ps.getPeerIDs() {
			require.True(t, ps.PeerIsAlive(id))
		}
	}
	for _, ps := range peers {
		ps.Stop()
	}
}
