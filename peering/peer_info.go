package peering

import (
	"encoding/json"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/proxi/glb"
)

type PeerInfo struct {
	Name           string               `json:"name"`
	ID             peer.ID              `json:"id"`
	NumStaticPeers uint16               `json:"num_static_peers"`
	NumActivePeers uint16               `json:"num_active_peers"`
	Sequencers     []core.ChainID       `json:"sequencers,omitempty"`
	SyncedBranches []core.TransactionID `json:"synced_branches,omitempty"`
}

func (pi *PeerInfo) Bytes() []byte {
	ret, err := json.Marshal(pi)
	glb.AssertNoError(err)
	return ret
}

func PeerInfoFromBytes(data []byte) (*PeerInfo, error) {
	var ret PeerInfo
	err := json.Unmarshal(data, &ret)
	if err != nil {
		return nil, err
	}
	return &ret, nil
}
