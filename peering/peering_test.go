package peering

import (
	"bytes"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/countdown"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/stretchr/testify/require"
)

// TODO tests fails when started all together due to timing problems and deadlocks
//  Usually pass when started one-by-one

// initializes ledger.Library singleton for all tests and creates testing genesis private key

func init() {
	ledger.InitWithTestingLedgerIDData()
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

func TestBasic(t *testing.T) {
	t.Run("1", func(t *testing.T) {
		const hostIndex = 2
		cfg := MakeConfigFor(5, hostIndex)
		t.Logf("host index: %d, host port: %d", hostIndex, BeginPort+hostIndex)
		for name, ma := range cfg.PreConfiguredPeers {
			t.Logf("%s : %s", name, ma.String())
		}
		env := newEnvironment()
		_, err := New(env, cfg)
		require.NoError(t, err)
	})
	t.Run("2", func(t *testing.T) {
		const hostIndex = 2
		cfg := MakeConfigFor(5, hostIndex)
		env := newEnvironment()
		peers, err := New(env, cfg)
		require.NoError(t, err)
		peers.Run()
		peers.Stop()
	})
}

func makeHosts(t *testing.T, nHosts int, trace bool) []*Peers {
	hosts := make([]*Peers, nHosts)
	var err error
	for i := 0; i < nHosts; i++ {
		cfg := MakeConfigFor(nHosts, i)
		env := newEnvironment()
		hosts[i], err = New(env, cfg)
		require.NoError(t, err)
		if trace {
			env.StartTracingTags(TraceTag)
		}
	}
	return hosts
}

func TestHeartbeat(t *testing.T) {
	const (
		numHosts = 5
		trace    = false
	)
	hosts := makeHosts(t, numHosts, trace)
	for _, h := range hosts {
		h.Run()
	}
	time.Sleep(3 * time.Second)
	for _, ps := range hosts {
		for _, id := range ps.getPeerIDs() {
			require.True(t, ps.IsAlive(id))
		}
	}

	hosts[0].Stop()
	time.Sleep(aliveDuration)
	for i, ps := range hosts {
		if i != 0 {
			require.True(t, !ps.IsAlive(hosts[0].host.ID()))
			ps.Stop()
		}
	}
}

func TestSendMsg(t *testing.T) {
	t.Run("1", func(t *testing.T) {
		const (
			numHosts = 5
			trace    = false
		)
		hosts := makeHosts(t, numHosts, trace)

		for _, h := range hosts {
			h1 := h
			h.OnReceiveTxBytes(func(from peer.ID, txBytes []byte, _ *txmetadata.TransactionMetadata) {
				t.Logf("host %s received %d bytes from %s", h1.host.ID().String(), len(txBytes), from.String())
			})
		}
		for _, h := range hosts {
			h.Run()
		}
		time.Sleep(1 * time.Second)
		for i, id := range hosts[0].getPeerIDs() {
			ok := hosts[0].SendTxBytesWithMetadataToPeer(id, bytes.Repeat([]byte{0xff}, i+5), nil)
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
			trace    = false
			numMsg   = 1000
		)
		hosts := makeHosts(t, numHosts, trace)
		counter := countdown.New(numMsg*(numHosts-1), 2*time.Second)
		counter1 := 0
		for _, h := range hosts {
			h1 := h
			h1.OnReceiveTxBytes(func(from peer.ID, txBytes []byte, _ *txmetadata.TransactionMetadata) {
				counter1++
				counter.Tick()
			})
		}
		for _, h := range hosts {
			h.Run()
		}
		time.Sleep(1 * time.Second)

		count := 0
		ids := hosts[0].getPeerIDs()
		t.Logf("num peers: %d", len(ids))
		for _, id := range ids {
			for i := 0; i < numMsg; i++ {
				ok := hosts[0].SendTxBytesWithMetadataToPeer(id, []byte{0xff, 0xff}, nil)
				require.True(t, ok)
				count++
			}
		}
		t.Logf("count = %d", count)
		err := counter.Wait()
		t.Logf("counter1 = %d", counter1)
		for _, h := range hosts {
			h.Stop()
		}
		require.NoError(t, err)
	})
	t.Run("3-all hosts", func(t *testing.T) {
		// TODO test fails with bigger numMsg
		const (
			numHosts = 5
			trace    = false
			numMsg   = 90 // 100 // 721 // 720 pass, 721 does not
		)
		hosts := makeHosts(t, numHosts, trace)
		counter := countdown.New(numHosts*numMsg*(numHosts-1), 20*time.Second)
		counter1 := 0
		for _, h := range hosts {
			h1 := h
			h1.OnReceiveTxBytes(func(from peer.ID, txBytes []byte, _ *txmetadata.TransactionMetadata) {
				counter1++
				counter.Tick()
			})
		}
		for _, h := range hosts {
			h.Run()
		}
		time.Sleep(3 * time.Second)

		for _, h := range hosts {
			h1 := h
			go func() {
				count := 0
				ids := h1.getPeerIDs()
				t.Logf("num peers: %d", len(ids))
				for _, id := range ids {
					for i := 0; i < numMsg; i++ {
						ok := h1.SendTxBytesWithMetadataToPeer(id, []byte{0xff, 0xff}, nil)
						require.True(t, ok)
						count++
					}
				}
				t.Logf("count = %d", count)
				require.EqualValues(t, numMsg*len(ids), count)
			}()
		}
		err := counter.Wait()
		t.Logf("counter1 = %d", counter1)
		for _, h := range hosts {
			h.Stop()
		}
		require.NoError(t, err)
	})
	t.Run("4-all hosts gossip", func(t *testing.T) {
		// TODO test fails with bigger numMsg
		const (
			numHosts = 5
			trace    = false
			numMsg   = 700
		)
		hosts := makeHosts(t, numHosts, trace)
		counter := countdown.New(numHosts*(numHosts-1)*numMsg, 10*time.Second)
		t.Logf("sending %d messages", numHosts*(numHosts-1)*numMsg)

		counter1 := 0
		for _, h := range hosts {
			h1 := h
			h1.OnReceiveTxBytes(func(from peer.ID, txBytes []byte, _ *txmetadata.TransactionMetadata) {
				counter1++
				counter.Tick()
			})
		}
		for _, h := range hosts {
			h.Run()
		}
		time.Sleep(heartbeatRate * 5)

		for _, h := range hosts {
			h1 := h
			go func() {
				for i := 0; i < numMsg; i++ {
					h1.GossipTxBytesToPeers([]byte{0xff, 0xff}, nil)
				}
			}()
		}
		err := counter.Wait()
		t.Logf("counter1 = %d", counter1)
		for _, h := range hosts {
			h.Stop()
		}
		require.NoError(t, err)
	})
	t.Run("pull", func(t *testing.T) {
		// TODO fails with timeout. Most likely related to the deadlocks in the same process
		const (
			numHosts = 5
			trace    = false
			numMsg   = 100
		)
		hosts := makeHosts(t, numHosts, trace)
		counter := countdown.New(numMsg, 15*time.Second)

		txSet := set.New[ledger.TransactionID]()
		txSetMutex := &sync.Mutex{}

		for _, h := range hosts {
			h1 := h
			h1.OnReceivePullTxRequest(func(from peer.ID, txids []ledger.TransactionID) {
				txSetMutex.Lock()
				defer txSetMutex.Unlock()

				counter.Tick()

				for i := range txids {
					require.True(t, txSet.Contains(txids[i]))
					go h1.SendTxBytesWithMetadataToPeer(from, txids[i][:], nil)
				}
			})

			h1.OnReceiveTxBytes(func(from peer.ID, txBytes []byte, _ *txmetadata.TransactionMetadata) {
				require.True(t, len(txBytes) == 32)
				var txid ledger.TransactionID
				copy(txid[:], txBytes)

				txSetMutex.Lock()
				defer txSetMutex.Unlock()

				txSet.Remove(txid)
			})
		}
		for _, h := range hosts {
			h.Run()
		}
		time.Sleep(4 * time.Second)

		for i := 0; i < numMsg; i++ {
			txids := rndTxIDs()
			txSetMutex.Lock()
			txSet.Insert(txids...)
			txSetMutex.Unlock()

			succ := hosts[i%numHosts].PullTransactionsFromRandomPeer(txids...)
			require.True(t, succ)
		}
		err := counter.Wait()
		require.NoError(t, err)

		time.Sleep(3 * time.Second)
		for _, h := range hosts {
			h.Stop()
		}
		require.EqualValues(t, 0, len(txSet))
	})
}

func rndTxIDs() []ledger.TransactionID {
	rnd := rand.Intn(5) + 1
	ret := make([]ledger.TransactionID, rnd)

	for i := range ret {
		ret[i] = ledger.RandomTransactionID(false)
	}
	return ret
}

func TestSerDe(t *testing.T) {
	t.Run("pullSyncPortion", func(t *testing.T) {
		data := encodeSyncPortionMsg(66000, 200)
		starting, maxSlots, err := decodeSyncPortionMsg(data)
		require.NoError(t, err)
		require.EqualValues(t, 66000, starting)
		require.EqualValues(t, 200, maxSlots)

	})
}
