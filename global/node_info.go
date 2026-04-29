package global

import (
	"encoding/json"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

type NodeInfo struct {
	ID              peer.ID       `json:"id"`
	Version         string        `json:"version"`
	CommitHash      string        `json:"commit_hash"`
	CommitTime      string        `json:"commit_time"`
	NumStaticAlive  uint16        `json:"num_static_peers"`
	NumDynamicAlive uint16        `json:"num_dynamic_alive"`
	Sequencer       *base.ChainID `json:"sequencers,omitempty"`
	// Adaptive rate control metrics
	MemoryStressLevel int  `json:"memory_stress_level"`        // 0-100, derived from allocated/limit
	PipelineSize      int  `json:"pipeline_size"`              // total vertices + solicited queue + wait counter
	IsSyncing         bool `json:"is_syncing,omitempty"`       // true when forward-sync is active
	IsSnapshotting    bool `json:"is_snapshotting,omitempty"`  // true when snapshot is being generated
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

func (ni *NodeInfo) Lines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	seqStr := "<none>"
	if ni.Sequencer != nil {
		seqStr = ni.Sequencer.String()
	}
	ret.Add("lpp host id: %s", ni.ID.String()).
		Add("static peers alive: %d", ni.NumStaticAlive).
		Add("dynamic peers alive: %d", ni.NumDynamicAlive).
		Add("sequencer: %s", seqStr).
		Add("commit hash: %s", ni.CommitHash).
		Add("commit time: %s", ni.CommitTime).
		Add("memory stress: %d%%", ni.MemoryStressLevel).
		Add("pipeline size: %d", ni.PipelineSize)
	return ret
}
