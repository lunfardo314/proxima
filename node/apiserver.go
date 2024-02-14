package node

import (
	"fmt"

	"github.com/lunfardo314/proxima/api/server"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/spf13/viper"
)

func (p *ProximaNode) startApiServer() {
	port := viper.GetInt("api.server.port")
	addr := fmt.Sprintf(":%d", port)
	p.Log().Infof("starting API server on %s", addr)

	go server.RunOn(addr, p)
	go func() {
		<-p.ctx.Done()
		p.stopAPIServer()
	}()
}

func (p *ProximaNode) stopAPIServer() {
	// do we need to do something here?
	p.Log().Infof("API server has been stopped")
}

func (p *ProximaNode) GetNodeInfo() *global.NodeInfo {
	alivePeers, configuredPeers := p.Peers.NumPeers()
	ret := &global.NodeInfo{
		Name:           "a Proxima node",
		ID:             p.Peers.SelfID(),
		NumStaticPeers: uint16(configuredPeers),
		NumActivePeers: uint16(alivePeers),
		Sequencers:     make([]ledger.ChainID, len(p.Sequencers)),
		Branches:       make([]ledger.TransactionID, 0),
	}
	// TODO
	//for i := range p.Sequencers {
	//	ret.Sequencers[i] = *p.Sequencers[i].ID()
	//}
	return ret
}

func (p *ProximaNode) HeaviestStateForLatestTimeSlot() multistate.SugaredStateReader {
	return p.Workflow.HeaviestStateForLatestTimeSlot()
}

func (p *ProximaNode) SubmitTxBytesFromAPI(txBytes []byte) error {
	_, err := p.Workflow.TxBytesIn(txBytes)
	return err
}
