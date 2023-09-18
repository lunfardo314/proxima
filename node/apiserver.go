package node

import (
	"fmt"
	"net/http"

	"github.com/lunfardo314/proxima/api/handlers"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/viper"
)

func (p *ProximaNode) startApiServer() {
	port := viper.GetInt("api.server.port")
	addr := fmt.Sprintf(":%d", port)
	p.log.Infof("starting API server on %s", addr)

	handlers.RegisterHandlers(p.uTangle)

	go func() {
		err := http.ListenAndServe(addr, nil)
		util.AssertNoError(err)
	}()
}

func (p *ProximaNode) stopApiServer() {
	// do we need to do something here?
	p.log.Infof("API server has been stopped")
}
