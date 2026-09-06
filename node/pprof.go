package node

import (
	"fmt"
	"net/http"
	"net/http/pprof"
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

	// Serve pprof on its OWN mux, never http.DefaultServeMux. Importing
	// net/http/pprof for its side effect used to register /debug/pprof/* on the
	// default mux at init time, and the API + metrics servers served that mux —
	// so pprof was live on the public API port regardless of pprof.enable. With
	// an explicit private mux, pprof is reachable only here, only when enabled.
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	go func() {
		util.AssertNoError(http.ListenAndServe(url, mux))
	}()
}
