package node

import (
	"errors"
	"fmt"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/server"
	"github.com/lunfardo314/proxima/api/streaming"
	"github.com/lunfardo314/proxima/core/core_modules/tippool"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
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

func (p *ProximaNode) startStreaming() {
	if viper.GetBool("streaming.enable") || viper.GetBool("api.streaming_enable") {
		streaming.Run(p)
	}
}

// GetNodeInfo TODO not finished
func (p *ProximaNode) GetNodeInfo() *global.NodeInfo {
	aliveStaticPeers, aliveDynamicPeers, _ := p.peers.NumAlive()

	ret := &global.NodeInfo{
		ID:              p.peers.SelfPeerID(),
		Version:         global.Version,
		NumStaticAlive:  uint16(aliveStaticPeers),
		NumDynamicAlive: uint16(aliveDynamicPeers),
		Sequencer:       p.GetOwnSequencerID(),
		CommitHash:      global.CommitHash,
		CommitTime:      global.CommitTime,
	}
	return ret
}

// GetSyncInfo TODO not finished
func (p *ProximaNode) GetSyncInfo() *api.SyncInfo {
	latestSlot, latestHealthySlot, synced := p.workflow.LatestBranchSlots()
	lrb := p.GetLatestReliableBranch()
	lrbSlot := uint32(0)
	curSlot := uint32(ledger.TimeNow().Slot)
	var cov uint64
	if lrb == nil {
		p.Log().Warnf("[sync] can't find latest reliable branch")
	} else {
		cov = p.workflow.Branches().LedgerCoverage(lrb.TxID())
		lrbSlot = uint32(lrb.Stem.ID.Slot())
	}

	ret := &api.SyncInfo{
		Synced:         synced,
		CurrentSlot:    curSlot,
		LrbSlot:        lrbSlot,
		LedgerCoverage: cov,
		PerSequencer:   make(map[string]api.SequencerSyncInfo),
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
	lrb := multistate.FindLatestReliableBranch(p.StateStore(), global.FractionHealthyBranch)
	if lrb == nil {
		return multistate.SugaredStateReader{}, fmt.Errorf("LatestReliableState: can't find latest reliable branch")
	}
	return multistate.MakeSugared(multistate.MustNewReadable(p.StateStore(), lrb.Root, 0), p.Log()), nil
}

func (p *ProximaNode) CheckTransactionInLRB(txid base.TransactionID, maxDepth int) (lrbid base.TransactionID, foundAtDepth int) {
	return p.workflow.CheckTransactionInLRB(txid, maxDepth)
}

func (p *ProximaNode) SubmitTxBytesFromAPI(txBytes []byte) {
	p.workflow.TxBytesInFromAPIQueued(txBytes)
}

func (p *ProximaNode) GetLatestReliableBranch() (ret *multistate.BranchData) {
	err := util.CatchPanicOrError(func() error {
		//ret = p.workflow.Branches().FindLatestReliableBranch(global.FractionHealthyBranch)
		ret = multistate.FindLatestReliableBranch(p.StateStore(), global.FractionHealthyBranch)
		return nil
	})
	if err != nil {
		if errors.Is(err, common.ErrDBUnavailable) {
			return nil
		}
		p.Fatal(err)
	}
	return
}

func (p *ProximaNode) GetSnapshotBranchID() base.TransactionID {
	return multistate.FetchSnapshotBranchID(p.StateStore())
}

func (p *ProximaNode) SelfPeerID() peer.ID {
	return p.peers.SelfPeerID()
}

func (p *ProximaNode) GetKnownLatestMilestonesJSONAble() map[string]tippool.LatestSequencerTipDataJSONAble {
	return p.workflow.GetKnownLatestSequencerDataJSONAble()
}

func (p *ProximaNode) OnTransaction(fun func(tx *transaction.Transaction) bool) {
	p.workflow.OnTransaction(fun)
}

func (p *ProximaNode) OnTxDeleted(fun func(txid base.TransactionID) bool) {
	p.workflow.OnTxDeleted(fun)
}
