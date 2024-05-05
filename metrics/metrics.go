package metrics

import (
	"fmt"
	"net/http"

	"github.com/lunfardo314/proxima/global"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/viper"
)

const defaultMetricsPort = 14000

type (
	Environment interface {
		global.NodeGlobal
	}
)

func Start(env Environment) {
	port := viper.GetInt("metrics.port")
	if port == 0 {
		env.Log().Warnf("metrics.port not specified. Will use %d for Prometheus metrics exposure", defaultMetricsPort)
		port = defaultMetricsPort
	}
	env.MetricsRegistry().MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	collectors.NewBuildInfoCollector()
	go func() {
		http.Handle("/metrics", promhttp.HandlerFor(
			env.MetricsRegistry(),
			promhttp.HandlerOpts{
				Registry: env.MetricsRegistry(),
			},
		))
		env.Log().Fatal(http.ListenAndServe(fmt.Sprintf(":%d", port), nil))
	}()
	env.Log().Infof("Prometheus metrics exposed on port %d", port)
}
