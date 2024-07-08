package node

import (
	"fmt"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/server"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/core/workflow"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/spf13/viper"
)

func (p *ProximaNode) startAPIServer() {
	port := viper.GetInt("api.port")
	addr := fmt.Sprintf(":%d", port)
	p.Log().Infof("starting API server on %s", addr)

	go server.RunOn(addr, p)
	go func() {
		<-p.Ctx().Done()
		p.stopAPIServer()
	}()
}

func (p *ProximaNode) stopAPIServer() {
	// do we need to do something else here?
	p.Log().Debugf("API server has been stopped")
}

// GetNodeInfo TODO not finished
func (p *ProximaNode) GetNodeInfo() *global.NodeInfo {
	aliveStaticPeers, aliveDynamicPeers := p.peers.NumAlive()

	ret := &global.NodeInfo{
		Name:            "a Proxima node",
		ID:              p.peers.SelfID(),
		Version:         global.Version,
		NumStaticAlive:  uint16(aliveStaticPeers),
		NumDynamicAlive: uint16(aliveDynamicPeers),
		Sequencers:      make([]ledger.ChainID, len(p.Sequencers)),
		Branches:        make([]ledger.TransactionID, 0),
	}

	return ret
}

// GetSyncInfo TODO not finished
func (p *ProximaNode) GetSyncInfo() *api.SyncInfo {

	latestSlot, latestHealthySlot, synced := p.workflow.LatestBranchSlots()
	//synced, _ := p.workflow.SyncedStatus()
	ret := &api.SyncInfo{
		Synced: synced,
		//InSyncWindow bool                         `json:"in_sync_window,omitempty"`
		//PerSequencer map[string]SequencerSyncInfo `json:"per_sequencer,omitempty"`
		PerSequencer: make(map[string]api.SequencerSyncInfo),
	}
	for seq := range p.Sequencers {
		seqInfo := p.Sequencers[seq].Info()
		ssi := api.SequencerSyncInfo{
			Synced:           synced,
			LatestBookedSlot: uint32(latestHealthySlot),
			LatestSeenSlot:   uint32(latestSlot),
			LedgerCoverage:   seqInfo.LedgerCoverage,
		}
		chainId := p.Sequencers[seq].SequencerID()
		ret.PerSequencer[chainId.StringHex()] = ssi
	}

	return ret
}

// GetPeersInfo TODO not finished
func (p *ProximaNode) GetPeersInfo() *api.PeersInfo {

	ids, addrs := p.peers.GetPeers()
	peers := make([]api.PeerInfo, len(ids))
	for i := 0; i < len(ids); i++ {
		peers[i].ID = ids[i].String()
		peers[i].MultiAddresses = make([]string, 1)
		peers[i].MultiAddresses[0] = addrs[i]
	}
	ret := &api.PeersInfo{
		Peers: peers,
	}

	return ret
}

func (p *ProximaNode) HeaviestStateForLatestTimeSlot() multistate.SugaredStateReader {
	return p.workflow.HeaviestStateForLatestTimeSlot()
}

func (p *ProximaNode) SubmitTxBytesFromAPI(txBytes []byte, trace ...bool) (*ledger.TransactionID, error) {
	traceFlag := false
	if len(trace) > 0 {
		traceFlag = trace[0]
	}
	return p.workflow.TxBytesIn(txBytes,
		workflow.WithSourceType(txmetadata.SourceTypeAPI),
		workflow.WithTxTraceFlag(traceFlag),
	)
}

func (p *ProximaNode) QueryTxIDStatusJSONAble(txid *ledger.TransactionID) vertex.TxIDStatusJSONAble {
	return p.workflow.QueryTxIDStatusJSONAble(txid)
}

func (p *ProximaNode) GetTxInclusion(txid *ledger.TransactionID, slotsBack int) *multistate.TxInclusion {
	return p.workflow.GetTxInclusion(txid, slotsBack)
}
