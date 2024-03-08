package gossip

import (
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util/queue"
)

type (
	Environment interface {
		global.NodeGlobal
		PeerName(id peer.ID) string
		GossipTxBytesToPeers(txBytes []byte, metadata *txmetadata.TransactionMetadata, except ...peer.ID) int
	}

	Input struct {
		Tx           *transaction.Transaction
		ReceivedFrom *peer.ID
		Metadata     txmetadata.TransactionMetadata
	}

	Gossip struct {
		*queue.Queue[*Input]
		Environment
		// bloom filter to avoid most of the repeating gossips
		gossipedFilter    map[ledger.TransactionIDVeryShort4]time.Time
		gossipedFilterTTL time.Duration
	}
)

const (
	Name                   = "gossip"
	TraceTag               = Name
	chanBufferSize         = 10
	gossipedFilterTTLSlots = 6
	purgePeriod            = 5 * time.Second
)

func New(env Environment) *Gossip {
	return &Gossip{
		Queue:             queue.NewQueueWithBufferSize[*Input](Name, chanBufferSize, env.Log().Level(), nil),
		Environment:       env,
		gossipedFilter:    make(map[ledger.TransactionIDVeryShort4]time.Time),
		gossipedFilterTTL: gossipedFilterTTLSlots * ledger.L().ID.SlotDuration(),
	}
}

func (d *Gossip) Start() {
	d.MarkWorkProcessStarted(Name)
	d.AddOnClosed(func() {
		d.MarkWorkProcessStopped(Name)
	})
	d.Queue.Start(d, d.Environment.Ctx())
	go d.purgeLoop()
}

func (d *Gossip) Consume(inp *Input) {
	if inp == nil {
		// purge filter command
		d.Tracef(TraceTag, "purge gossiped tx filter")
		d.purgeFilter()
		return
	}
	// check filter
	vsID := inp.Tx.ID().VeryShortID4()
	if _, already := d.gossipedFilter[vsID]; already {
		// repeating, ignore. Rare false positives won't be gossiped. No problem: it will be pulled if needed
		d.Tracef(TraceTag, "%s already gossiped, ignore", inp.Tx.IDShortString)
		return
	}
	d.gossipedFilter[vsID] = time.Now().Add(d.gossipedFilterTTL)

	if inp.ReceivedFrom == nil {
		d.Tracef(TraceTag, "send %s to all peers, meta: %s",
			inp.Tx.IDShortString, inp.Metadata.String)
		d.TraceTx(inp.Tx.ID(), TraceTag+": send to all peers")
		d.GossipTxBytesToPeers(inp.Tx.Bytes(), &inp.Metadata)
	} else {
		d.Tracef(TraceTag, "send %s to peers except %s(%s), meta: %s",
			inp.Tx.IDShortString, inp.ReceivedFrom, d.PeerName(*inp.ReceivedFrom), inp.Metadata.String)
		d.TraceTx(inp.Tx.ID(), TraceTag+": send to all peers, except %s(%s)", inp.ReceivedFrom, d.PeerName(*inp.ReceivedFrom))
		d.GossipTxBytesToPeers(inp.Tx.Bytes(), &inp.Metadata, *inp.ReceivedFrom)
	}
}

func (d *Gossip) purgeFilter() {
	nowis := time.Now()
	toDelete := make([]ledger.TransactionIDVeryShort4, 0)
	for vsID, ttl := range d.gossipedFilter {
		if ttl.Before(nowis) {
			toDelete = append(toDelete, vsID)
		}
	}
	for _, vsID := range toDelete {
		delete(d.gossipedFilter, vsID)
	}
}

func (d *Gossip) purgeLoop() {
	for {
		select {
		case <-d.Ctx().Done():
			return
		case <-time.After(purgePeriod):
			d.Push(nil, true)
		}
	}
}
