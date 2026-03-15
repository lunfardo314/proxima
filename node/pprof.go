package node

import (
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"runtime"

	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/viper"
)

const defaultPprofPort = 8080

func (p *ProximaNode) startPProfIfEnabled() {
	if !viper.GetBool("pprof.enable") {
		return
	}
	// Enable mutex and block profiling for contention analysis
	runtime.SetMutexProfileFraction(5)
	runtime.SetBlockProfileRate(1000) // nanoseconds; captures blocks >= 1µs

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
