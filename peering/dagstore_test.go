package peering

import (
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// The message layouts survive a round trip, and a branch list too long for one message is split
// into messages that each fit the frame limit.
func TestDAGStoreMessageEncoding(t *testing.T) {
	hb := DAGStoreHeartbeat{
		SealedBranch:   base.RandomTransactionID(true, 0),
		StartingBranch: base.RandomTransactionID(true, 0),
		CanonicalTip:   base.RandomTransactionID(true, 0),
		Lost:           7,
	}
	hb2, err := DAGStoreHeartbeatFromBytes(hb.Bytes())
	require.NoError(t, err)
	require.Equal(t, hb, hb2)

	r := LRBReply{LRB: base.RandomTransactionID(true, 0), OldestSlot: 12, PurgeFloor: 3}
	r2, err := LRBReplyFromBytes(r.Bytes())
	require.NoError(t, err)
	require.Equal(t, r, r2)

	bl := BranchListReply{FromSlot: 100, ToSlot: 250, Last: true, Entries: make([]BranchListEntry, MaxBranchListEntries)}
	for i := range bl.Entries {
		bl.Entries[i] = BranchListEntry{Branch: base.RandomTransactionID(true, 0), Predecessor: base.RandomTransactionID(true, 0)}
	}
	msg := bl.Bytes()
	require.LessOrEqual(t, len(msg), MaxPayloadSize)
	bl2, err := BranchListReplyFromBytes(msg)
	require.NoError(t, err)
	require.Equal(t, bl, bl2)

	_, err = BranchListReplyFromBytes(msg[:len(msg)-1])
	require.Error(t, err)
}

// recorder collects what a host receives, safe for the handlers' goroutines.
type recorder struct {
	mutex      sync.Mutex
	gossip     map[base.TransactionID]peer.ID
	heartbeats map[peer.ID]bool // sender -> trusted
	lrb        []LRBReply
	branches   []BranchListEntry
	lastSeen   bool
	pullLRB    int
}

func newRecorder() *recorder {
	return &recorder{gossip: map[base.TransactionID]peer.ID{}, heartbeats: map[peer.ID]bool{}}
}

func (r *recorder) do(fun func()) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	fun()
}

// dagStoreHosts makes three hosts: 0 a node that declares 1 its trusted dagstore, 1 the dagstore,
// fed by 0, and 2 a plain node. With oldBuild, host 2 is built without the dagstore protocol, as a
// node of a release before it: the handler is gone before any peer learns its protocols.
func dagStoreHosts(t *testing.T, oldBuild ...bool) ([]*Peers, []*recorder) {
	const n = 3
	hosts := make([]*Peers, n)
	recs := make([]*recorder, n)
	for i := range hosts {
		cfg := MakeConfigFor(n, i)
		switch i {
		case 0:
			cfg.DAGStores = []string{"peer1"}
		case 1:
			cfg.Feeders = []string{"peer0"}
			cfg.IsDAGStore = true
		}
		var err error
		hosts[i], err = New(newEnvironment(), cfg)
		require.NoError(t, err)
		if i == 2 && len(oldBuild) > 0 && oldBuild[0] {
			hosts[i].host.RemoveStreamHandler(hosts[i].lppProtocolDAGStore)
		}
		rec := newRecorder()
		recs[i] = rec
		h := hosts[i]
		h.OnReceiveTxBytes(func(from peer.ID, _ []byte, txid base.TransactionID) {
			rec.do(func() { rec.gossip[txid] = from })
		})
		h.OnDAGStoreHeartbeat(func(from peer.ID, trusted bool, _ DAGStoreHeartbeat) {
			rec.do(func() { rec.heartbeats[from] = trusted })
		})
		h.OnPullLRB(func(from peer.ID) {
			rec.do(func() { rec.pullLRB++ })
			h.SendLRBReply(from, LRBReply{LRB: base.RandomTransactionID(true, 0), OldestSlot: 1, PurgeFloor: 0})
		})
		h.OnPullBranchList(func(from peer.ID, fromSlot, toSlot uint32) {
			entries := make([]BranchListEntry, 2*MaxBranchListEntries+5)
			for j := range entries {
				entries[j].Branch = base.RandomTransactionID(true, 0, base.LedgerTime{Slot: fromSlot})
			}
			h.SendBranchListReply(from, fromSlot, toSlot, entries)
		})
		h.OnLRBReply(func(_ peer.ID, r LRBReply) {
			rec.do(func() { rec.lrb = append(rec.lrb, r) })
		})
		h.OnBranchListReply(func(_ peer.ID, r BranchListReply) {
			rec.do(func() {
				rec.branches = append(rec.branches, r.Entries...)
				rec.lastSeen = rec.lastSeen || r.Last
			})
		})
	}
	for _, h := range hosts {
		h.Run()
	}
	t.Cleanup(func() {
		for _, h := range hosts {
			h.Stop()
		}
	})
	time.Sleep(4 * time.Second)
	return hosts, recs
}

func gossipFrom(r *recorder, txid base.TransactionID) (from peer.ID, ok bool) {
	r.do(func() { from, ok = r.gossip[txid] })
	return
}

// A node pushes to its trusted dagstore and leaves it out of the gossip; a heartbeat makes every
// peer treat the sender as a dagstore, trusted only where declared.
func TestDAGStorePushAndHeartbeat(t *testing.T) {
	hosts, recs := dagStoreHosts(t)
	node, dagstore, plain := hosts[0], hosts[1], hosts[2]
	require.True(t, node.HasTrustedDAGStores())
	require.True(t, node.IsTrustedDAGStore(dagstore.host.ID()))
	require.True(t, dagstore.IsFeeder(node.host.ID()))
	require.False(t, plain.IsTrustedDAGStore(dagstore.host.ID()))

	gossiped := base.RandomTransactionID(false, 1)
	node.GossipTxBytesToPeers(gossiped[:], gossiped)
	pushed := base.RandomTransactionID(false, 2)
	node.PushTxBytesToDAGStores(pushed[:], pushed, "")
	time.Sleep(2 * time.Second)

	_, ok := gossipFrom(recs[2], gossiped)
	require.True(t, ok, "the plain node missed the gossip")
	_, ok = gossipFrom(recs[1], gossiped)
	require.False(t, ok, "the trusted dagstore got gossip")
	from, ok := gossipFrom(recs[1], pushed)
	require.True(t, ok, "the dagstore missed the push")
	require.Equal(t, node.host.ID(), from)
	_, ok = gossipFrom(recs[2], pushed)
	require.False(t, ok, "a push reached a peer that is no dagstore")

	// before the heartbeat the plain node still gossips to the dagstore, an ordinary peer to it
	before := base.RandomTransactionID(false, 3)
	plain.GossipTxBytesToPeers(before[:], before)
	time.Sleep(time.Second)
	_, ok = gossipFrom(recs[1], before)
	require.True(t, ok)

	dagstore.SendDAGStoreHeartbeat(DAGStoreHeartbeat{SealedBranch: base.RandomTransactionID(true, 0)})
	time.Sleep(2 * time.Second)
	var trustedAtNode, trustedAtPlain, seenAtPlain bool
	recs[0].do(func() { trustedAtNode = recs[0].heartbeats[dagstore.host.ID()] })
	recs[2].do(func() { trustedAtPlain, seenAtPlain = recs[2].heartbeats[dagstore.host.ID()] })
	require.True(t, trustedAtNode)
	require.True(t, seenAtPlain)
	require.False(t, trustedAtPlain)

	after := base.RandomTransactionID(false, 4)
	plain.GossipTxBytesToPeers(after[:], after)
	time.Sleep(time.Second)
	_, ok = gossipFrom(recs[1], after)
	require.False(t, ok, "a peer advertised as dagstore still gets gossip")
}

// Lineage requests are answered to a trusted dagstore only, and a long branch list arrives whole,
// over several messages, the last one marked.
func TestDAGStoreLineageRequests(t *testing.T) {
	hosts, recs := dagStoreHosts(t)
	node, dagstore, plain := hosts[0], hosts[1], hosts[2]

	require.True(t, dagstore.SendPullLRB(node.host.ID()))
	require.True(t, dagstore.SendPullBranchList(node.host.ID(), 1000, 1149))
	require.True(t, plain.SendPullLRB(node.host.ID())) // not trusted: ignored
	time.Sleep(2 * time.Second)

	var nLRB, nBranches, nPullLRB int
	var last bool
	recs[1].do(func() { nLRB, nBranches, last = len(recs[1].lrb), len(recs[1].branches), recs[1].lastSeen })
	recs[0].do(func() { nPullLRB = recs[0].pullLRB })
	require.Equal(t, 1, nLRB)
	require.Equal(t, 2*MaxBranchListEntries+5, nBranches)
	require.True(t, last)
	require.Equal(t, 1, nPullLRB, "a request from an undeclared peer was answered")
	var plainGotReply int
	recs[2].do(func() { plainGotReply = len(recs[2].lrb) })
	require.Zero(t, plainGotReply)
}

// A heartbeat to a peer whose build lacks the dagstore protocol is refused, and that does not get
// the peer banned as one of another ledger: gossip with it goes on.
func TestDAGStoreHeartbeatToOldBuild(t *testing.T) {
	hosts, recs := dagStoreHosts(t, true)
	dagstore, plain := hosts[1], hosts[2]

	dagstore.SendDAGStoreHeartbeat(DAGStoreHeartbeat{})
	time.Sleep(2 * time.Second)
	require.False(t, dagstore.isOtherLedger(plain.host.ID()))
	require.True(t, dagstore.IsAlive(plain.host.ID()))

	txid := base.RandomTransactionID(false, 5)
	dagstore.SendTxBytesToPeer(plain.host.ID(), txid[:], txid)
	time.Sleep(time.Second)
	_, ok := gossipFrom(recs[2], txid)
	require.True(t, ok)
}
