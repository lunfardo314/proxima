package peering

import (
	"bytes"
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/countdown"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

// TODO tests fails when started all together due to timing problems and deadlocks
//  Usually pass when started one-by-one

// initializes ledger.Library singleton for all tests and creates testing genesis private key

func init() {
	ledger.InitWithTestingLedgerData()
}

func TestGenData(t *testing.T) {
	t.Run("gen ma", func(t *testing.T) {
		for i, s := range allPrivateKeys {
			privKey, err := crypto.UnmarshalEd25519PrivateKey(util.MustPrivateKeyFromHexString(s))
			util.AssertNoError(err)
			host, err := libp2p.New(libp2p.Identity(privKey))
			util.AssertNoError(err)
			t.Logf("host %d: %s", i, host.ID().String())
		}
	})
	t.Run("multiaddr", func(t *testing.T) {
		for i := range hostID {
			t.Logf("%d: %s", i, MultiAddrString(i, BeginPort+i))
		}
	})
}

type peeringEnvForTesting struct {
	*global.Global
}

func (e *peeringEnvForTesting) SyncServerDisabled() bool {
	return false
}

func newEnvironment() environment {
	return &peeringEnvForTesting{global.NewDefault()}
}

func TestBasic1(t *testing.T) {
	const hostIndex = 2
	cfg := MakeConfigFor(5, hostIndex)
	t.Logf("host index: %d, host port: %d", hostIndex, BeginPort+hostIndex)
	for name, ma := range cfg.PreConfiguredPeers {
		t.Logf("%s : %s", name, ma.String())
	}
	env := newEnvironment()
	peers, err := New(env, cfg)
	require.NoError(t, err)
	_ = peers.host.Close()
}

func TestBasic2(t *testing.T) {
	const hostIndex = 2
	cfg := MakeConfigFor(5, hostIndex)
	env := newEnvironment()
	peers, err := New(env, cfg)
	require.NoError(t, err)
	peers.Run()
	peers.Stop()
}

func makeHosts(t *testing.T, nHosts int) []*Peers {
	hosts := make([]*Peers, nHosts)
	var err error
	for i := 0; i < nHosts; i++ {
		cfg := MakeConfigFor(nHosts, i)
		env := newEnvironment()
		hosts[i], err = New(env, cfg)
		require.NoError(t, err)
	}
	return hosts
}

// TestPeerLiveness verifies the connection-driven IsAlive path (post-HB
// removal): all hosts should report each other alive once libp2p has
// established the connections, and after stopping host 0, the others should
// observe its connection going down.
func TestPeerLiveness(t *testing.T) {
	const numHosts = 5
	hosts := makeHosts(t, numHosts)
	for _, h := range hosts {
		h.Run()
	}
	time.Sleep(5 * time.Second)
	for _, ps := range hosts {
		require.True(t, len(ps.getPeerIDs()) == numHosts-1)
		for _, id := range ps.getPeerIDs() {
			require.True(t, ps.IsAlive(id))
		}
	}

	hosts[0].Stop()
	// libp2p needs a moment to tear down the connection on the remote end and
	// fire Notifiee.Disconnected; ~2 s is plenty over QUIC.
	time.Sleep(2 * time.Second)
	for i, ps := range hosts {
		if i != 0 {
			require.True(t, !ps.IsAlive(hosts[0].host.ID()))
			ps.Stop()
		}
	}
}

// TestInboundPeerRegistered pins the admission of a peer we did not dial. Host 1 has no static
// peers and one dynamic slot; host 0 lists host 1 as static and dials it. Host 1 must register
// the peer from its inbound connection and its gossip must reach it — the direction that goes to
// registered peers only. Without inbound registration a newcomer would connect and hear nothing.
func TestInboundPeerRegistered(t *testing.T) {
	cfg1 := MakeConfigFor(2, 1)
	cfg1.PreConfiguredPeers = make(map[string]_multiaddr) // host 1 dials nobody
	cfg1.MaxDynamicPeers = 1
	host1, err := New(newEnvironment(), cfg1)
	require.NoError(t, err)

	cfg0 := MakeConfigFor(2, 0)
	require.Len(t, cfg0.PreConfiguredPeers, 1) // host 0 dials host 1
	newcomer, err := New(newEnvironment(), cfg0)
	require.NoError(t, err)

	var received atomic.Int64
	newcomer.OnReceiveTxBytes(func(from peer.ID, _ []byte, _ base.TransactionID) {
		require.EqualValues(t, host1.host.ID(), from)
		received.Add(1)
	})

	host1.Run()
	defer host1.Stop()
	newcomer.Run()
	defer newcomer.Stop()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !host1.IsAlive(newcomer.host.ID()) {
		time.Sleep(50 * time.Millisecond)
	}
	require.True(t, host1.IsAlive(newcomer.host.ID()), "host 1 did not register the inbound peer")

	host1.GossipTxBytesToPeers([]byte{0xff, 0xff}, base.TransactionID{})
	waitForCount(t, &received, 1, 5*time.Second)
}

// TestDynamicPeerCap pins peering.max_dynamic_peers as a hard ceiling: host 1 has two dynamic
// slots and no static peers, while four newcomers all list it as static and dial it. Whether a
// peer arrives inbound or is discovered outbound, host 1 must never hold more than two dynamic
// peers — before the fix inbound peers bypassed the cap and every one of them was registered.
func TestDynamicPeerCap(t *testing.T) {
	const (
		numNewcomers    = 4
		maxDynamicPeers = 2
	)
	cfg1 := MakeConfigFor(numNewcomers+1, 1)
	cfg1.PreConfiguredPeers = make(map[string]_multiaddr)
	cfg1.MaxDynamicPeers = maxDynamicPeers
	host1, err := New(newEnvironment(), cfg1)
	require.NoError(t, err)

	newcomers := make([]*Peers, 0, numNewcomers)
	for _, idx := range []int{0, 2, 3, 4} {
		cfg := MakeConfigFor(numNewcomers+1, idx)
		require.Contains(t, cfg.PreConfiguredPeers, "peer1")
		h, err := New(newEnvironment(), cfg)
		require.NoError(t, err)
		newcomers = append(newcomers, h)
	}

	host1.Run()
	defer host1.Stop()
	for _, h := range newcomers {
		h.Run()
		defer h.Stop()
	}

	// Watch the count while the refused newcomers keep retrying their static dial: it must reach
	// the cap and then stay there.
	deadline := time.Now().Add(15 * time.Second)
	var reachedCap time.Time
	for time.Now().Before(deadline) {
		n := len(host1.getPeerIDs())
		require.LessOrEqual(t, n, maxDynamicPeers, "more dynamic peers registered than peering.max_dynamic_peers")
		if n == maxDynamicPeers {
			if reachedCap.IsZero() {
				reachedCap = time.Now()
			} else if time.Since(reachedCap) > 3*time.Second {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if reachedCap.IsZero() {
		t.Fatalf("host 1 registered %d dynamic peers, want %d", len(host1.getPeerIDs()), maxDynamicPeers)
	}
	t.Fatalf("host 1's dynamic peer count did not stay at %d", maxDynamicPeers)
}

// TestInboundPeerChurn pins the churn rule: when a node's dynamic slots are all
// taken, an inbound newcomer takes one over instead of being turned away. The
// peer admitted within the grace period is protected, so the evicted peer
// redialling cannot displace the newcomer in turn.
func TestInboundPeerChurn(t *testing.T) {
	cfg1 := MakeConfigFor(3, 1)
	cfg1.PreConfiguredPeers = make(map[string]_multiaddr) // host 1 dials nobody
	cfg1.MaxDynamicPeers = 1
	host1, err := New(newEnvironment(), cfg1)
	require.NoError(t, err)

	cfgFirst := MakeConfigFor(3, 0)
	cfgFirst.PreConfiguredPeers = map[string]_multiaddr{"peer1": cfgFirst.PreConfiguredPeers["peer1"]}
	first, err := New(newEnvironment(), cfgFirst)
	require.NoError(t, err)

	host1.Run()
	defer host1.Stop()
	first.Run()
	defer first.Stop()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && host1.getPeer(first.host.ID()) == nil {
		time.Sleep(50 * time.Millisecond)
	}
	require.NotNil(t, host1.getPeer(first.host.ID()), "host 1 did not register the first peer")

	// Age the registered peer past the grace period so it becomes a churn victim.
	host1.withPeer(first.host.ID(), func(p *Peer) {
		p.whenAdded = time.Now().Add(-2 * inboundChurnGrace)
	})

	cfgNewcomer := MakeConfigFor(3, 2)
	cfgNewcomer.PreConfiguredPeers = map[string]_multiaddr{"peer1": cfgNewcomer.PreConfiguredPeers["peer1"]}
	newcomer, err := New(newEnvironment(), cfgNewcomer)
	require.NoError(t, err)
	newcomer.Run()
	defer newcomer.Stop()

	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && host1.getPeer(newcomer.host.ID()) == nil {
		time.Sleep(50 * time.Millisecond)
	}
	require.NotNil(t, host1.getPeer(newcomer.host.ID()), "the newcomer was not admitted at the cap")
	require.Nil(t, host1.getPeer(first.host.ID()), "the aged peer was not churned out")
	require.Len(t, host1.getPeerIDs(), 1)
	require.True(t, host1.isChurnCooldown(first.host.ID()), "the evicted peer was not barred from churning")
	host1.mutex.RLock()
	until := host1.churnCooldown[first.host.ID()]
	host1.mutex.RUnlock()
	require.WithinDuration(t, time.Now().Add(churnCooldownDuration), until, time.Minute,
		"a churned peer should get the standard cooldown, not the flap cooldown")

	// The evicted peer has host 1 in its static list and redials after the
	// reconnect backoff. It must not be able to churn the newcomer back out.
	time.Sleep(3 * time.Second)
	require.NotNil(t, host1.getPeer(newcomer.host.ID()), "the evicted peer churned the newcomer back out")
	require.Nil(t, host1.getPeer(first.host.ID()))
}

// TestFlappingPeerCannotChurn pins the second half of the churn cooldown: a
// peer that connects and immediately disappears is unstable, so its next redial
// must not displace a working peer. Before the cooldown such a peer could churn
// a healthy connection on every attempt, which is what an other-ledger node
// that closes before the protocol check does.
func TestFlappingPeerCannotChurn(t *testing.T) {
	cfg1 := MakeConfigFor(4, 1)
	cfg1.PreConfiguredPeers = make(map[string]_multiaddr) // host 1 dials nobody
	cfg1.MaxDynamicPeers = 1
	host1, err := New(newEnvironment(), cfg1)
	require.NoError(t, err)
	host1.Run()
	defer host1.Stop()

	// The clients are not pre-configured and do not autopeer: each connection is
	// made explicitly below, so the test controls when each peer arrives.
	newClient := func(idx int) *Peers {
		cfg := MakeConfigFor(4, idx)
		cfg.PreConfiguredPeers = make(map[string]_multiaddr)
		cfg.MaxDynamicPeers = 0
		p, err := New(newEnvironment(), cfg)
		require.NoError(t, err)
		p.Run()
		t.Cleanup(p.Stop)
		return p
	}
	connect := func(from *Peers) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// The listen address is the IPv4 loopback the static test config uses;
		// host.Addrs() advertises the machine's IPv6 interfaces instead.
		ma, err := multiaddr.NewMultiaddr(MultiAddrString(1, BeginPort+1))
		require.NoError(t, err)
		return from.host.Connect(ctx, peer.AddrInfo{ID: host1.host.ID(), Addrs: []multiaddr.Multiaddr{ma}})
	}
	waitRegistered := func(p *Peers, present bool) {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if (host1.getPeer(p.host.ID()) != nil) == present {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("host 1 registration of %s: got %v, want %v", ShortPeerIDString(p.host.ID()), !present, present)
	}

	flapper := newClient(2)
	require.NoError(t, connect(flapper))
	waitRegistered(flapper, true)

	// Close the connection while it is clearly inside the flap window. The
	// registration timestamp is reset first so a slow test host cannot push the
	// disconnect outside the window.
	host1.withPeer(flapper.host.ID(), func(p *Peer) {
		p.whenAdded = time.Now()
	})
	require.NoError(t, flapper.host.Network().ClosePeer(host1.host.ID()))
	waitRegistered(flapper, false)
	require.True(t, host1.isChurnCooldown(flapper.host.ID()), "a peer that vanished right after admission was not barred")
	host1.mutex.RLock()
	until := host1.churnCooldown[flapper.host.ID()]
	host1.mutex.RUnlock()
	require.WithinDuration(t, time.Now().Add(churnFlapCooldown), until, time.Minute,
		"a peer that vanished right after admission did not get the flap cooldown")

	// Fill the now-free slot with a stable peer.
	stable := newClient(3)
	require.NoError(t, connect(stable))
	waitRegistered(stable, true)

	// The flapper redials while the node is full. The cooldown must refuse it
	// instead of evicting the stable peer.
	require.Eventually(t, func() bool {
		return connect(flapper) == nil
	}, 5*time.Second, 50*time.Millisecond)
	time.Sleep(time.Second)
	require.Nil(t, host1.getPeer(flapper.host.ID()), "the flapping peer was registered at the cap")
	require.NotNil(t, host1.getPeer(stable.host.ID()), "the flapping peer evicted a stable peer")
}

// TestInboundPeerDisconnectStopsWriters pins the teardown of a dynamic peer that goes away on
// its own, which is the path a libp2p Disconnected notification takes rather than dropPeer: the
// peer must be forgotten AND its three stream writers must exit. Before the fix the entry was
// deleted but the writers were left parked, three goroutines leaked per disconnect, and a
// flapping external peer grew a node by hundreds of goroutines a day.
func TestInboundPeerDisconnectStopsWriters(t *testing.T) {
	cfg1 := MakeConfigFor(2, 1)
	cfg1.PreConfiguredPeers = make(map[string]_multiaddr) // host 1 dials nobody
	cfg1.MaxDynamicPeers = 1
	host1, err := New(newEnvironment(), cfg1)
	require.NoError(t, err)

	cfg0 := MakeConfigFor(2, 0)
	require.Len(t, cfg0.PreConfiguredPeers, 1)
	newcomer, err := New(newEnvironment(), cfg0)
	require.NoError(t, err)

	host1.Run()
	defer host1.Stop()
	newcomer.Run()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !host1.IsAlive(newcomer.host.ID()) {
		time.Sleep(50 * time.Millisecond)
	}
	require.True(t, host1.IsAlive(newcomer.host.ID()), "host 1 did not register the inbound peer")

	// take the writers of the inbound peer's entry before it is forgotten
	var streams []*peerStream
	host1.withPeer(newcomer.host.ID(), func(p *Peer) {
		require.NotNil(t, p)
		for _, s := range p.streams {
			streams = append(streams, s)
		}
	})
	require.Len(t, streams, 3)

	// the newcomer leaves; host 1 learns it from libp2p, not from its own drop path
	newcomer.Stop()

	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && host1.getPeer(newcomer.host.ID()) != nil {
		time.Sleep(50 * time.Millisecond)
	}
	require.Nil(t, host1.getPeer(newcomer.host.ID()), "host 1 still tracks the peer that left")

	for _, s := range streams {
		select {
		case <-s.done:
		case <-time.After(5 * time.Second):
			t.Fatal("a stream writer of the departed peer was never told to stop")
		}
	}
}

// waitForCount blocks until the counter reaches want, failing the test if it has not within
// timeout. Used where the expected total is only known after sending, so countdown (which needs
// its target up front) does not fit.
func waitForCount(t *testing.T, c *atomic.Int64, want int64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if c.Load() >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.GreaterOrEqual(t, c.Load(), want, "timed out waiting for %d messages", want)
}

// TestSendQueueBounded pins the property the per-peer send queue exists for: a producer far faster
// than the link neither blocks nor makes the process grow a goroutine per queued message. Before
// the queue, a burst like this parked one goroutine per message per peer — half a million of them
// accumulated on a spammed node over a day.
func TestSendQueueBounded(t *testing.T) {
	const (
		numHosts = 3
		numMsg   = 20_000
	)
	hosts := makeHosts(t, numHosts)
	for _, h := range hosts {
		h.OnReceiveTxBytes(func(_ peer.ID, _ []byte, _ base.TransactionID) {})
	}
	for _, h := range hosts {
		h.Run()
	}
	time.Sleep(time.Second)

	before := runtime.NumGoroutine()
	started := time.Now()
	for i := 0; i < numMsg; i++ {
		hosts[0].GossipTxBytesToPeers([]byte{0xff, 0xff}, base.TransactionID{})
	}
	elapsed := time.Since(started)
	peak := runtime.NumGoroutine()

	t.Logf("%d broadcasts in %v; goroutines %d -> %d", numMsg, elapsed, before, peak)
	// Non-blocking: enqueue is a channel send with a default branch, so the whole burst costs far
	// less than the time even one stalled write would.
	require.Less(t, elapsed, 10*time.Second)
	// Bounded: writers are one per peer per protocol, created with the peer, not with the message.
	require.Less(t, peak-before, 50)

	for _, h := range hosts {
		h.Stop()
	}
}

func TestSendMsg(t *testing.T) {
	t.Run("1", func(t *testing.T) {
		const numHosts = 5
		hosts := makeHosts(t, numHosts)

		for _, h := range hosts {
			h1 := h
			h.OnReceiveTxBytes(func(from peer.ID, txBytes []byte, _ base.TransactionID) {
				t.Logf("host %s received %d bytes from %s", h1.host.ID().String(), len(txBytes), from.String())
			})
		}
		for _, h := range hosts {
			h.Run()
		}
		time.Sleep(1 * time.Second)
		for i, id := range hosts[0].getPeerIDs() {
			ok := hosts[0].SendTxBytesToPeer(id, bytes.Repeat([]byte{0xff}, i+5), base.TransactionID{})
			require.True(t, ok)
		}
		time.Sleep(1 * time.Second)
		for _, h := range hosts {
			h.Stop()
		}
	})
	t.Run("2-from one host", func(t *testing.T) {
		const (
			numHosts = 5
			numMsg   = 1000
		)
		hosts := makeHosts(t, numHosts)
		var received atomic.Int64
		for _, h := range hosts {
			h.OnReceiveTxBytes(func(from peer.ID, txBytes []byte, _ base.TransactionID) {
				received.Add(1)
			})
		}
		for _, h := range hosts {
			h.Run()
		}
		time.Sleep(1 * time.Second)

		accepted := 0
		ids := hosts[0].getPeerIDs()
		t.Logf("num peers: %d", len(ids))
		for _, id := range ids {
			for i := 0; i < numMsg; i++ {
				if hosts[0].SendTxBytesToPeer(id, []byte{0xff, 0xff}, base.TransactionID{}) {
					accepted++
				}
			}
		}
		// The per-peer backlog is bounded, so a burst pushed faster than the link drains is shed on
		// purpose and this send is best-effort. The contract that must hold is the narrower one:
		// a message the queue accepted is delivered.
		t.Logf("accepted %d of %d", accepted, numMsg*len(ids))
		require.Greater(t, accepted, 0)
		waitForCount(t, &received, int64(accepted), 10*time.Second)

		for _, h := range hosts {
			h.Stop()
		}
	})
	t.Run("3-all hosts", func(t *testing.T) {
		const (
			numHosts = 5
			numMsg   = 90
		)
		hosts := makeHosts(t, numHosts)
		var received atomic.Int64
		for _, h := range hosts {
			h.OnReceiveTxBytes(func(from peer.ID, txBytes []byte, _ base.TransactionID) {
				received.Add(1)
			})
		}
		for _, h := range hosts {
			h.Run()
		}
		time.Sleep(3 * time.Second)

		var accepted atomic.Int64
		var wg sync.WaitGroup
		for _, h := range hosts {
			h1 := h
			wg.Add(1)
			go func() {
				defer wg.Done()
				for _, id := range h1.getPeerIDs() {
					for i := 0; i < numMsg; i++ {
						if h1.SendTxBytesToPeer(id, []byte{0xff, 0xff}, base.TransactionID{}) {
							accepted.Add(1)
						}
					}
				}
			}()
		}
		wg.Wait()
		t.Logf("accepted %d", accepted.Load())
		require.Greater(t, accepted.Load(), int64(0))
		waitForCount(t, &received, accepted.Load(), 20*time.Second)

		for _, h := range hosts {
			h.Stop()
		}
	})
	t.Run("4-all hosts gossip", func(t *testing.T) {
		const (
			numHosts = 5
			numMsg   = 700
			// Paced below the drain rate so the bounded backlog never fills: this case is about
			// broadcast reaching everyone, not about what happens when it cannot. Saturation is
			// covered by TestSendQueueBounded.
			pace = 200 * time.Microsecond
		)
		hosts := makeHosts(t, numHosts)
		counter := countdown.New(numHosts*(numHosts-1)*numMsg, 30*time.Second)
		t.Logf("sending %d messages", numHosts*(numHosts-1)*numMsg)

		var counter1 atomic.Int64
		for _, h := range hosts {
			h1 := h
			h1.OnReceiveTxBytes(func(from peer.ID, txBytes []byte, _ base.TransactionID) {
				counter1.Add(1)
				counter.Tick()
			})
		}
		for _, h := range hosts {
			h.Run()
		}
		time.Sleep(2 * time.Second)

		for _, h := range hosts {
			h1 := h
			go func() {
				for i := 0; i < numMsg; i++ {
					h1.GossipTxBytesToPeers([]byte{0xff, 0xff}, base.TransactionID{})
					time.Sleep(pace)
				}
			}()
		}
		err := counter.Wait()
		t.Logf("counter1 = %d", counter1.Load())
		for _, h := range hosts {
			h.Stop()
		}
		require.NoError(t, err)
	})
	t.Run("pull", func(t *testing.T) {
		const (
			numHosts = 5
			numTx    = 50
		)
		hosts := makeHosts(t, numHosts)
		counter := countdown.New(numTx*numHosts*(numHosts-1), 15*time.Second)

		txSet := set.New[base.TransactionID]()
		for i := 0; i < numTx; i++ {
			txid := base.RandomTransactionID(false, 2)
			txSet.Insert(txid)
		}

		for _, h := range hosts {
			h1 := h
			h1.OnReceivePullTxRequest(func(from peer.ID, txid base.TransactionID) {
				counter.Tick()

				require.True(t, txSet.Contains(txid))
				go h1.SendTxBytesToPeer(from, txid[:], base.TransactionID{})
			})

			h1.OnReceiveTxBytes(func(from peer.ID, txBytes []byte, _ base.TransactionID) {
				txid, err := base.TransactionIDFromBytes(txBytes)
				require.NoError(t, err)
				require.True(t, txSet.Contains(txid))
			})
		}
		for _, h := range hosts {
			h.Run()
		}
		time.Sleep(4 * time.Second)

		for _, h := range hosts {
			for txid := range txSet {
				n := h.PullTransactionsFromPeers(txid)
				require.EqualValues(t, numHosts-1, n)
			}
		}

		err := counter.Wait()
		require.NoError(t, err)

		time.Sleep(3 * time.Second)
		for _, h := range hosts {
			h.Stop()
		}
	})
}
