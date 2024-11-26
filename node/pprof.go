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
	var host string
	if viper.GetBool("pprof.external_access_enabled") {
		host = "0.0.0.0"
	} else {
		host = "localhost"
	}
	url := fmt.Sprintf("%s:%d", host, port)
	p.Log().Infof("starting pprof on '%s'", url)

	go func() {
		util.AssertNoError(http.ListenAndServe(url, nil))
	}()
}
