package server

import (
	_ "embed"
	"net/http"
)

//go:embed peers_dashboard.html
var peersDashboardHTML []byte

func (srv *server) getPeersDashboard(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(peersDashboardHTML)
}
