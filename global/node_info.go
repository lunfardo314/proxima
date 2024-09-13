package global

import (
	"encoding/json"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

type NodeInfo struct {
	Name            string                 `json:"name"`
	ID              peer.ID                `json:"id"`
	Version         string                 `json:"version"`
	NumStaticAlive  uint16                 `json:"num_static_peers"`
	NumDynamicAlive uint16                 `json:"num_dynamic_alive"`
	Sequencer       *ledger.ChainID        `json:"sequencers,omitempty"`
	Branches        []ledger.TransactionID `json:"branches,omitempty"`
}

func (ni *NodeInfo) Bytes() []byte {
	ret, err := json.Marshal(ni)
	util.AssertNoError(err)
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
	seqStr := "<none>"
	if ni.Sequencer != nil {
		seqStr = ni.Sequencer.String()
	}
	ret.Add("Node info:").
		Add("   name: '%s'", ni.Name).
		Add("   lpp host ID: %s", ni.ID.String()).
		Add("   static peers alive: %d", ni.NumStaticAlive).
		Add("   dynamic peers alive: %d", ni.NumDynamicAlive).
		Add("   sequencer: %s", seqStr).
		Add("   branches: %d", len(ni.Branches))
	return ret
}
