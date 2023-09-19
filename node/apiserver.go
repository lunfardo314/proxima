package node

import (
	"fmt"

	"github.com/lunfardo314/proxima/api/server"
	"github.com/spf13/viper"
)

func (p *ProximaNode) startApiServer() {
	port := viper.GetInt("api.server.port")
	addr := fmt.Sprintf(":%d", port)
	p.log.Infof("starting API server on %s", addr)

	go server.RunOn(addr, p.uTangle)
}

func (p *ProximaNode) stopApiServer() {
	// do we need to do something here?
	p.log.Infof("API server has been stopped")
}
