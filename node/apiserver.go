package node

import (
	"fmt"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/server"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/spf13/viper"
)

func (p *ProximaNode) startAPIServer() {
	if viper.GetBool("api.disable") {
		// default is enabled API
		p.Log().Infof("API server is disabled")
		return
	}
	port := viper.GetInt("api.port")
	addr := fmt.Sprintf(":%d", port)
	p.Log().Infof("starting API server on %s", addr)

	go server.Run(addr, p)
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
	aliveStaticPeers, aliveDynamicPeers, _ := p.peers.NumAlive()

	ret := &global.NodeInfo{
		ID:              p.peers.SelfID(),
		Version:         global.Version,
		NumStaticAlive:  uint16(aliveStaticPeers),
		NumDynamicAlive: uint16(aliveDynamicPeers),
		Sequencer:       p.GetOwnSequencerID(),
	}
	return ret
}

// GetSyncInfo TODO not finished
func (p *ProximaNode) GetSyncInfo() *api.SyncInfo {
	latestSlot, latestHealthySlot, synced := p.workflow.LatestBranchSlots()
	ret := &api.SyncInfo{
		Synced:       synced,
		PerSequencer: make(map[string]api.SequencerSyncInfo),
	}
	if p.sequencer != nil {
		seqInfo := p.sequencer.Info()
		ssi := api.SequencerSyncInfo{
			Synced:              synced,
			LatestHealthySlot:   uint32(latestHealthySlot),
			LatestCommittedSlot: uint32(latestSlot),
			LedgerCoverage:      seqInfo.LedgerCoverage,
		}
		chainId := p.sequencer.SequencerID()
		ret.PerSequencer[chainId.StringHex()] = ssi
	}
	return ret
}

func (p *ProximaNode) GetPeersInfo() *api.PeersInfo {
	return p.peers.GetPeersInfo()
}

func (p *ProximaNode) LatestReliableState() (multistate.SugaredStateReader, error) {
	return p.workflow.LatestReliableState()
}

func (p *ProximaNode) SubmitTxBytesFromAPI(txBytes []byte, trace bool) {
	p.workflow.TxBytesInFromAPIQueued(txBytes, trace)
}

func (p *ProximaNode) QueryTxIDStatusJSONAble(txid *ledger.TransactionID) vertex.TxIDStatusJSONAble {
	return p.workflow.QueryTxIDStatusJSONAble(txid)
}

func (p *ProximaNode) GetTxInclusion(txid *ledger.TransactionID, slotsBack int) *multistate.TxInclusion {
	return p.workflow.GetTxInclusion(txid, slotsBack)
}

func (p *ProximaNode) GetLatestReliableBranch() *multistate.BranchData {
	return multistate.FindLatestReliableBranch(p.StateStore(), global.FractionHealthyBranch)
}
