package peering

import (
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core"
)

type PeerInfo struct {
	Name           string               `json:"name"`
	ID             peer.ID              `json:"id"`
	NumStaticPeers uint16               `json:"num_static_peers"`
	NumActivePeers uint16               `json:"num_active_peers"`
	Sequencers     []core.ChainID       `json:"sequencers,omitempty"`
	SyncedBranches []core.TransactionID `json:"synced_branches,omitempty"`
}
