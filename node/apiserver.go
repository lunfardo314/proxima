package node

import (
	"fmt"

	"github.com/lunfardo314/proxima/api/server"
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/global"
	"github.com/spf13/viper"
)

func (p *ProximaNode) startApiServer() {
	port := viper.GetInt("api.server.port")
	addr := fmt.Sprintf(":%d", port)
	p.log.Infof("starting API server on %s", addr)

	go server.RunOn(addr, p.Workflow, p.getNodeInfo)
	go func() {
		<-p.ctx.Done()
		p.stopAPIServer()
	}()
}

func (p *ProximaNode) stopAPIServer() {
	// do we need to do something here?
	p.log.Infof("API server has been stopped")
}

func (p *ProximaNode) getNodeInfo() *global.NodeInfo {
	alivePeers, configuredPeers := p.Peers.NumPeers()
	ret := &global.NodeInfo{
		Name:           "a Proxima node",
		ID:             p.Peers.SelfID(),
		NumStaticPeers: uint16(configuredPeers),
		NumActivePeers: uint16(alivePeers),
		Sequencers:     make([]core.ChainID, len(p.Sequencers)),
		Branches:       make([]core.TransactionID, 0), // TODO
	}
	for i := range p.Sequencers {
		ret.Sequencers[i] = *p.Sequencers[i].ID()
	}
	return ret
}
