package peering

import (
	"bytes"
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
