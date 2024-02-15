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
		global.Glb
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
	chanBufferSize = 10
)

func New(env Environment) *Gossip {
	return &Gossip{
		Queue:       queue.NewQueueWithBufferSize[*Input](Name, chanBufferSize, env.Log().Level(), nil),
		Environment: env,
	}
}

func (d *Gossip) Start() {
	d.MarkStartedComponent(Name)
	d.AddOnClosed(func() {
		d.MarkStoppedComponent(Name)
	})
	d.Queue.Start(d, d.Environment.Ctx())
}

func (d *Gossip) Consume(inp *Input) {
	if inp.ReceivedFrom == nil {
		d.GossipTxBytesToPeers(inp.Tx.Bytes(), &inp.Metadata)
	} else {
		d.GossipTxBytesToPeers(inp.Tx.Bytes(), &inp.Metadata, *inp.ReceivedFrom)
	}
}
