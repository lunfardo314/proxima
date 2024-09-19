package node

import (
	"fmt"
	"net/http"
	_ "net/http/pprof"

	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/viper"
)

const defaultPprofPort = 8080

func (p *ProximaNode) startPProfIfEnabled() {
	if !viper.GetBool("pprof.enable") {
		return
	}
	port := viper.GetInt("pprof.port")
	if port == 0 {
		port = defaultPprofPort
	}
	url := fmt.Sprintf("localhost:%d", port)
	p.Log().Infof("starting pprof on '%s'", url)

	go func() {
		util.AssertNoError(http.ListenAndServe(url, nil))
	}()
}
