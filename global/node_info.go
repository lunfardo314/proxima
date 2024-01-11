package global

import (
	"encoding/json"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util/lines"
)

type NodeInfo struct {
	Name           string                 `json:"name"`
	ID             peer.ID                `json:"id"`
	NumStaticPeers uint16                 `json:"num_static_peers"`
	NumActivePeers uint16                 `json:"num_active_peers"`
	Sequencers     []ledger.ChainID       `json:"sequencers,omitempty"`
	Branches       []ledger.TransactionID `json:"branches,omitempty"`
}

func (ni *NodeInfo) Bytes() []byte {
	ret, err := json.Marshal(ni)
	glb.AssertNoError(err)
	return ret
}

func NodeInfoFromBytes(data []byte) (*NodeInfo, error) {
	var ret NodeInfo
	err := json.Unmarshal(data, &ret)
	if err != nil {
		return nil, err
	}
	return &ret, nil
}

// TODO not finished
func (ni *NodeInfo) Lines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	ret.Add("Node info:").
		Add("   name: '%s'", ni.Name).
		Add("   lpp host ID: %s", ni.ID.String()).
		Add("   static peers: %d", ni.NumStaticPeers).
		Add("   active peers: %d", ni.NumActivePeers).
		Add("   sequencers: %d", len(ni.Sequencers)).
		Add("   branches: %d", len(ni.Branches))
	return ret
}
