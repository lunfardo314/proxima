package node

import (
	"fmt"
	"net/http"
	_ "net/http/pprof"

	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/viper"
)

func (p *ProximaNode) startPProfIfEnabled() {
	if !viper.GetBool("pprof.enable") {
		return
	}
	port := viper.GetInt("pprof.port")
	url := fmt.Sprintf("localhost:%d", port)
	p.Log().Infof("starting pprof on '%s'", url)

	go func() {
		util.AssertNoError(http.ListenAndServe("localhost:8080", nil))
	}()
}
