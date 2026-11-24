package peering

import (
	"encoding/binary"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util/set"
)

// The dagstore is a peer that keeps the tangle history (github.com/lunfardo314/dagstore). Between
// a node and a dagstore, in mutual trusted peering declared on both sides between static peers, the
// node pushes every transaction it takes in and answers the dagstore's lineage requests; the
// dagstore announces its sealed branch by heartbeat. Nothing here is active unless a dagstore or a
// feeder is declared, except that a peer advertising the role gets no gossip.

// Pull message types beyond PullTransactions. A request goes from the dagstore to a feeder, the
// reply back on the feeder's own pull stream to the dagstore.
const (
	PullLRB             = byte(1) // -> the feeder's latest reliable branch
	PullBranchList      = byte(2) // -> the branches in the feeder's txstore over a slot range
	PullLRBReply        = byte(3)
	PullBranchListReply = byte(4)
)

const lppProtocolDAGStore = "/proxima/dagstore/%d"

type (
	// DAGStoreHeartbeat is what a dagstore announces to every peer, which also tells the peer that
	// the sender is a dagstore.
	DAGStoreHeartbeat struct {
		SealedBranch   base.TransactionID // newest sealed canonical branch
		StartingBranch base.TransactionID
		CanonicalTip   base.TransactionID
		Lost           uint64 // transactions declared lost
	}

	// LRBReply is a feeder's answer to PullLRB. OldestSlot and PurgeFloor tell how far back its
	// txstore reaches.
	LRBReply struct {
		LRB        base.TransactionID
		OldestSlot uint32
		PurgeFloor uint32
	}

	// BranchListEntry is a branch in the feeder's txstore with the predecessor its stem names.
	BranchListEntry struct {
		Branch, Predecessor base.TransactionID
	}

	// BranchListReply is one message of the answer to PullBranchList; a range may take several,
	// Last marks the final one.
	BranchListReply struct {
		FromSlot, ToSlot uint32
		Last             bool
		Entries          []BranchListEntry
	}
)

const (
	heartbeatMsgLen       = 3*base.TransactionIDLength + 8
	lrbReplyMsgLen        = 1 + base.TransactionIDLength + 4 + 4
	branchListReqMsgLen   = 1 + 4 + 4
	branchListReplyHeader = 1 + 4 + 4 + 1
	branchListEntryLen    = 2 * base.TransactionIDLength
	// MaxBranchListEntries is how many entries fit one reply message.
	MaxBranchListEntries = (MaxPayloadSize - branchListReplyHeader) / branchListEntryLen
)

// resolveNamedPeers maps static peer names to their IDs; a name must be one of peering.peers.
func resolveNamedPeers(cfg *Config, names []string, key string) (set.Set[peer.ID], error) {
	ret := set.New[peer.ID]()
	for _, name := range names {
		maddr, ok := cfg.PreConfiguredPeers[name]
		if !ok {
			return nil, fmt.Errorf("peering.%s: '%s' is not a static peer in peering.peers", key, name)
		}
		info, err := peer.AddrInfoFromP2pAddr(maddr.Multiaddr)
		if err != nil {
			return nil, fmt.Errorf("peering.%s: '%s': %w", key, name, err)
		}
		ret.Insert(info.ID)
	}
	return ret, nil
}

// IsTrustedDAGStore tells whether the peer is a dagstore this node declared: it gets pushes and
// answers to lineage requests, and its heartbeats are believed.
func (ps *Peers) IsTrustedDAGStore(id peer.ID) bool {
	return ps.dagStores.Contains(id)
}

// IsFeeder tells whether the peer is one this dagstore declared as feeder: only feeders' gossip is
// heard, and only their replies to lineage requests are taken.
func (ps *Peers) IsFeeder(id peer.ID) bool {
	return ps.feeders.Contains(id)
}

// HasTrustedDAGStores tells whether any dagstore is declared.
func (ps *Peers) HasTrustedDAGStores() bool {
	return len(ps.dagStores) > 0
}

// isDAGStorePeer tells whether a peer is a dagstore, declared or by its heartbeat. Called under
// ps.mutex.
func (ps *Peers) _isDAGStorePeer(p *Peer) bool {
	return p.isDAGStore || ps.dagStores.Contains(p.id)
}

// gossipTargets are the alive peers that take gossip: every one except dagstores, which get the
// push instead, and except the given peer.
func (ps *Peers) gossipTargets(except ...peer.ID) []peer.ID {
	ret := make([]peer.ID, 0)
	ps.forEachPeerRLock(func(p *Peer) bool {
		if (len(except) > 0 && p.id == except[0]) || ps._isDAGStorePeer(p) {
			return true
		}
		if ps._isAlive(p) {
			ret = append(ret, p.id)
		}
		return true
	})
	return ret
}

// PushTxBytesToDAGStores sends the transaction to every alive trusted dagstore except the one it
// came from, as an ordinary gossip message on its bounded queue.
func (ps *Peers) PushTxBytesToDAGStores(txBytes []byte, txid base.TransactionID, except peer.ID) {
	for id := range ps.dagStores {
		if id != except && ps.IsAlive(id) {
			ps.SendTxBytesToPeer(id, txBytes, txid)
		}
	}
}

// PullTransactionFromPeer sends a pull request to one peer only. Returns false if it was not queued.
func (ps *Peers) PullTransactionFromPeer(id peer.ID, txid base.TransactionID) bool {
	if !ps.sendMsgBytesOut(id, ps.lppProtocolPull, encodePullTransactionMsg(txid)) {
		return false
	}
	ps.pullRequestsOut.Inc()
	return true
}

// PullTransactionFromDAGStores sends a pull request to every alive trusted dagstore and returns
// how many were asked: for a transaction older than this node's txstore keeps.
func (ps *Peers) PullTransactionFromDAGStores(txid base.TransactionID) int {
	n := 0
	for id := range ps.dagStores {
		if ps.IsAlive(id) && ps.PullTransactionFromPeer(id, txid) {
			n++
		}
	}
	return n
}

// SendDAGStoreHeartbeat announces the dagstore's state to every alive peer. A peer running a build
// without the protocol refuses the stream; that costs one failed negotiation per heartbeat and is
// not taken for another ledger.
func (ps *Peers) SendDAGStoreHeartbeat(hb DAGStoreHeartbeat) {
	ps.sendMsgBytesOutMulti(ps.allAliveIDs(), ps.lppProtocolDAGStore, hb.Bytes())
}

// OnDAGStoreHeartbeat sets the handler of heartbeats. It runs for every peer's heartbeat; trusted
// tells whether the sender is a declared dagstore.
func (ps *Peers) OnDAGStoreHeartbeat(fun func(from peer.ID, trusted bool, hb DAGStoreHeartbeat)) {
	ps.mutex.Lock()
	ps.onDAGStoreHeartbeat = fun
	ps.mutex.Unlock()
}

func (ps *Peers) dagStoreStreamHandler(stream network.Stream) {
	defer func() { _ = stream.Close() }()

	id := stream.Conn().RemotePeer()
	if !ps.admitStream(id) {
		_ = stream.Reset()
		return
	}
	if _, err := readFrame(stream); err != nil { // start frame
		return
	}
	for {
		msg, err := readFrame(stream)
		if err != nil {
			return
		}
		ps.inMsgCounter.Inc()
		hb, err := DAGStoreHeartbeatFromBytes(msg)
		if err != nil {
			ps.Log().Warnf("[peering] dagstore heartbeat from %s: %v", ShortPeerIDString(id), err)
			return
		}
		ps.knownPeer(id, func(p *Peer) {
			p.lastMsgTime = time.Now()
			p.isDAGStore = true
		})
		ps.mutex.RLock()
		fun := ps.onDAGStoreHeartbeat
		ps.mutex.RUnlock()
		if fun != nil {
			fun(id, ps.IsTrustedDAGStore(id), hb)
		}
	}
}

// Lineage requests and replies.

// SendPullLRB asks a feeder for its latest reliable branch.
func (ps *Peers) SendPullLRB(id peer.ID) bool {
	return ps.sendMsgBytesOut(id, ps.lppProtocolPull, []byte{PullLRB})
}

// SendPullBranchList asks a feeder for the branches its txstore holds in [fromSlot, toSlot].
func (ps *Peers) SendPullBranchList(id peer.ID, fromSlot, toSlot uint32) bool {
	msg := make([]byte, branchListReqMsgLen)
	msg[0] = PullBranchList
	binary.BigEndian.PutUint32(msg[1:], fromSlot)
	binary.BigEndian.PutUint32(msg[5:], toSlot)
	return ps.sendMsgBytesOut(id, ps.lppProtocolPull, msg)
}

// SendLRBReply answers PullLRB.
func (ps *Peers) SendLRBReply(id peer.ID, r LRBReply) bool {
	return ps.sendMsgBytesOut(id, ps.lppProtocolPull, r.Bytes())
}

// SendBranchListReply answers PullBranchList, in as many messages as the entries need; an empty
// range is one message with no entries.
func (ps *Peers) SendBranchListReply(id peer.ID, fromSlot, toSlot uint32, entries []BranchListEntry) bool {
	for {
		n := min(len(entries), MaxBranchListEntries)
		r := BranchListReply{FromSlot: fromSlot, ToSlot: toSlot, Last: n == len(entries), Entries: entries[:n]}
		if !ps.sendMsgBytesOut(id, ps.lppProtocolPull, r.Bytes()) {
			return false
		}
		if r.Last {
			return true
		}
		entries = entries[n:]
	}
}

// OnPullLRB sets the handler of PullLRB requests from trusted dagstores.
func (ps *Peers) OnPullLRB(fun func(from peer.ID)) {
	ps.mutex.Lock()
	ps.onPullLRB = fun
	ps.mutex.Unlock()
}

// OnPullBranchList sets the handler of PullBranchList requests from trusted dagstores.
func (ps *Peers) OnPullBranchList(fun func(from peer.ID, fromSlot, toSlot uint32)) {
	ps.mutex.Lock()
	ps.onPullBranchList = fun
	ps.mutex.Unlock()
}

// OnLRBReply sets the handler of LRB replies from feeders.
func (ps *Peers) OnLRBReply(fun func(from peer.ID, r LRBReply)) {
	ps.mutex.Lock()
	ps.onLRBReply = fun
	ps.mutex.Unlock()
}

// OnBranchListReply sets the handler of branch list replies from feeders.
func (ps *Peers) OnBranchListReply(fun func(from peer.ID, r BranchListReply)) {
	ps.mutex.Lock()
	ps.onBranchListReply = fun
	ps.mutex.Unlock()
}

// handleLineageMsg handles a pull-protocol message of one of the lineage types. A request is
// answered only to a trusted dagstore, a reply taken only from a feeder; anything else is ignored.
// It returns an error only for a malformed message, which ends the stream.
func (ps *Peers) handleLineageMsg(from peer.ID, msg []byte) error {
	ps.mutex.RLock()
	onLRB, onBranchList, onLRBReply, onBranchListReply := ps.onPullLRB, ps.onPullBranchList, ps.onLRBReply, ps.onBranchListReply
	ps.mutex.RUnlock()

	switch msg[0] {
	case PullLRB:
		if len(msg) != 1 {
			return fmt.Errorf("wrong PullLRB message length %d", len(msg))
		}
		if onLRB != nil && ps.IsTrustedDAGStore(from) {
			go onLRB(from)
		}
	case PullBranchList:
		if len(msg) != branchListReqMsgLen {
			return fmt.Errorf("wrong PullBranchList message length %d", len(msg))
		}
		fromSlot, toSlot := binary.BigEndian.Uint32(msg[1:]), binary.BigEndian.Uint32(msg[5:])
		if onBranchList != nil && ps.IsTrustedDAGStore(from) {
			go onBranchList(from, fromSlot, toSlot)
		}
	case PullLRBReply:
		r, err := LRBReplyFromBytes(msg)
		if err != nil {
			return err
		}
		if onLRBReply != nil && ps.IsFeeder(from) {
			go onLRBReply(from, r)
		}
	case PullBranchListReply:
		r, err := BranchListReplyFromBytes(msg)
		if err != nil {
			return err
		}
		if onBranchListReply != nil && ps.IsFeeder(from) {
			go onBranchListReply(from, r)
		}
	default:
		return fmt.Errorf("wrong pull message type %d", msg[0])
	}
	return nil
}

// Encoding: fixed layouts, integers big-endian.

func (hb *DAGStoreHeartbeat) Bytes() []byte {
	ret := make([]byte, 0, heartbeatMsgLen)
	ret = append(ret, hb.SealedBranch[:]...)
	ret = append(ret, hb.StartingBranch[:]...)
	ret = append(ret, hb.CanonicalTip[:]...)
	return binary.BigEndian.AppendUint64(ret, hb.Lost)
}

func DAGStoreHeartbeatFromBytes(data []byte) (ret DAGStoreHeartbeat, err error) {
	if len(data) != heartbeatMsgLen {
		return ret, fmt.Errorf("wrong heartbeat length %d", len(data))
	}
	for i, dst := range []*base.TransactionID{&ret.SealedBranch, &ret.StartingBranch, &ret.CanonicalTip} {
		if *dst, err = base.TransactionIDFromBytes(data[i*base.TransactionIDLength : (i+1)*base.TransactionIDLength]); err != nil {
			return ret, err
		}
	}
	ret.Lost = binary.BigEndian.Uint64(data[3*base.TransactionIDLength:])
	return ret, nil
}

func (r *LRBReply) Bytes() []byte {
	ret := make([]byte, 0, lrbReplyMsgLen)
	ret = append(ret, PullLRBReply)
	ret = append(ret, r.LRB[:]...)
	ret = binary.BigEndian.AppendUint32(ret, r.OldestSlot)
	return binary.BigEndian.AppendUint32(ret, r.PurgeFloor)
}

func LRBReplyFromBytes(data []byte) (ret LRBReply, err error) {
	if len(data) != lrbReplyMsgLen || data[0] != PullLRBReply {
		return ret, fmt.Errorf("not an LRB reply")
	}
	if ret.LRB, err = base.TransactionIDFromBytes(data[1 : 1+base.TransactionIDLength]); err != nil {
		return ret, err
	}
	ret.OldestSlot = binary.BigEndian.Uint32(data[1+base.TransactionIDLength:])
	ret.PurgeFloor = binary.BigEndian.Uint32(data[1+base.TransactionIDLength+4:])
	return ret, nil
}

func (r *BranchListReply) Bytes() []byte {
	ret := make([]byte, 0, branchListReplyHeader+len(r.Entries)*branchListEntryLen)
	ret = append(ret, PullBranchListReply)
	ret = binary.BigEndian.AppendUint32(ret, r.FromSlot)
	ret = binary.BigEndian.AppendUint32(ret, r.ToSlot)
	last := byte(0)
	if r.Last {
		last = 1
	}
	ret = append(ret, last)
	for _, e := range r.Entries {
		ret = append(ret, e.Branch[:]...)
		ret = append(ret, e.Predecessor[:]...)
	}
	return ret
}

func BranchListReplyFromBytes(data []byte) (ret BranchListReply, err error) {
	if len(data) < branchListReplyHeader || data[0] != PullBranchListReply || (len(data)-branchListReplyHeader)%branchListEntryLen != 0 {
		return ret, fmt.Errorf("not a branch list reply")
	}
	ret.FromSlot = binary.BigEndian.Uint32(data[1:])
	ret.ToSlot = binary.BigEndian.Uint32(data[5:])
	switch data[9] {
	case 0:
	case 1:
		ret.Last = true
	default:
		return ret, fmt.Errorf("branch list reply: wrong last flag %d", data[9])
	}
	body := data[branchListReplyHeader:]
	ret.Entries = make([]BranchListEntry, len(body)/branchListEntryLen)
	for i := range ret.Entries {
		e := body[i*branchListEntryLen:]
		if ret.Entries[i].Branch, err = base.TransactionIDFromBytes(e[:base.TransactionIDLength]); err != nil {
			return ret, err
		}
		if ret.Entries[i].Predecessor, err = base.TransactionIDFromBytes(e[base.TransactionIDLength:branchListEntryLen]); err != nil {
			return ret, err
		}
	}
	return ret, nil
}
