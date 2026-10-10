package peering

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// TestOtherLedgerPeerDroppedAndBanned pins what happens between nodes of two
// ledgers, as after a reset: the protocol names differ, the libp2p connection
// still comes up, and the first gossip fails its stream negotiation. Each side
// must then cut the other off rather than keep a connected, alive, useless
// peer: the node that dialled the other as a static peer closes the connection
// and bans it (static peers are never dropped), the node that admitted the
// other as an inbound dynamic peer drops it and bans it, and neither registers
// the other again while the ban lasts.
func TestOtherLedgerPeerDroppedAndBanned(t *testing.T) {
	// host 0 lists host 1 as its static peer; host 1 dials nobody and admits
	// inbound peers
	cfg0 := MakeConfigFor(2, 0)
	cfg1 := MakeConfigFor(2, 1)
	cfg1.PreConfiguredPeers = make(map[string]_multiaddr)
	host0, err := New(newEnvironment(), cfg0)
	require.NoError(t, err)
	host1, err := New(newEnvironment(), cfg1)
	require.NoError(t, err)

	// host 1 is on another ledger: its protocols carry another hash
	host1.lppProtocolGossip = protocol.ID("/proxima/gossip/other")
	host1.lppProtocolPull = protocol.ID("/proxima/pull/other")
	host1.lppProtocolConnectivity = protocol.ID("/proxima/connectivity/other")

	host0.Run()
	defer host0.Stop()
	host1.Run()
	defer host1.Stop()

	id0, id1 := host0.host.ID(), host1.host.ID()
	waitFor(t, func() bool { return host0.IsAlive(id1) && host1.IsAlive(id0) }, "the version-blind connection comes up on both sides")

	// the first message each way fails its negotiation and bans the other
	host0.GossipTxBytesToPeers([]byte{1, 2, 3}, base.TransactionID{})
	host1.GossipTxBytesToPeers([]byte{1, 2, 3}, base.TransactionID{})
	waitFor(t, func() bool { return host0.isOtherLedger(id1) }, "host 0 bans its static peer on another ledger")
	waitFor(t, func() bool { return host1.isOtherLedger(id0) }, "host 1 bans the inbound peer on another ledger")

	// the dynamic peer is dropped, the static one is kept but cut off, and the
	// connection is gone on both sides and stays gone despite the static redial
	waitFor(t, func() bool { return len(host1.getPeerIDs()) == 0 }, "host 1 dropped the dynamic peer")
	require.Contains(t, host0.getPeerIDs(), id1, "a static peer is kept")
	waitFor(t, func() bool {
		return host0.host.Network().Connectedness(id1) != network.Connected &&
			host1.host.Network().Connectedness(id0) != network.Connected
	}, "the connection is closed on both sides")
	time.Sleep(2 * time.Second)
	require.False(t, host1.IsAlive(id0), "a banned peer's redial is closed at connect")
	require.Empty(t, host1.getPeerIDs(), "and never registered")
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	require.Eventually(t, cond, 15*time.Second, 50*time.Millisecond, msg)
}
