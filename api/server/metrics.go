package server

import "github.com/prometheus/client_golang/prometheus"

type metrics struct {
	totalRequests prometheus.Counter
}

func (srv *Server) registerMetrics() {
	srv.metrics.totalRequests = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_api_totalRequests",
		Help: "total API requests",
	})
	srv.MetricsRegistry().MustRegister(srv.metrics.totalRequests)
}
