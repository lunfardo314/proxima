package server

import (
	_ "embed"
	"net/http"
)

//go:embed netviz.html
var netvizHTML []byte

// getNetviz serves the self-contained network connectivity visualizer page. The
// page fetches /api/v1/get_connectivity_matrix and renders a force-directed graph.
func (srv *server) getNetviz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(netvizHTML)
}
