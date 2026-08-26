package server

import (
	_ "embed"
	"net/http"

	"github.com/lunfardo314/proxima/api/logo"
)

//go:embed netviz.html
var netvizHTML []byte

// netvizPage carries the horizontal lockup in the HUD and the bare mark as the
// tab icon. Dark variants: the page is drawn on near-black.
var netvizPage = logo.Page(netvizHTML, logo.LockupOnDark, logo.MarkOnDark)

// getNetviz serves the self-contained network connectivity visualizer page. The
// page fetches /api/v1/get_connectivity_matrix and renders a force-directed graph.
func (srv *server) getNetviz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(netvizPage)
}
