package gossip

import (
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/global"
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
	}
)

const (
	Name           = "gossip"
	TraceTag       = Name
	chanBufferSize = 10
)

func New(env Environment) *Gossip {
	return &Gossip{
		Queue:       queue.NewQueueWithBufferSize[*Input](Name, chanBufferSize, env.Log().Level(), nil),
		Environment: env,
	}
}

func (d *Gossip) Start() {
	d.MarkWorkProcessStarted(Name)
	d.AddOnClosed(func() {
		d.MarkWorkProcessStopped(Name)
	})
	d.Queue.Start(d, d.Environment.Ctx())
}

func (d *Gossip) Consume(inp *Input) {
	if inp.ReceivedFrom == nil {
		d.Tracef(TraceTag, "send %s to all peers, meta: %s",
			inp.Tx.IDShortString, inp.Metadata.String())
		d.GossipTxBytesToPeers(inp.Tx.Bytes(), &inp.Metadata)
	} else {
		d.Tracef(TraceTag, "send %s to peers except %s(%s), meta: %s",
			inp.Tx.IDShortString, inp.ReceivedFrom, d.PeerName(*inp.ReceivedFrom), inp.Metadata.String())
		d.GossipTxBytesToPeers(inp.Tx.Bytes(), &inp.Metadata, *inp.ReceivedFrom)
	}
}
