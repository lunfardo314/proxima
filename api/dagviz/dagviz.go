package dagviz

import (
	_ "embed"
	"net/http"

	"github.com/lunfardo314/proxima/api/logo"
)

//go:embed dagviz.html
var dagvizHTML []byte

// dagvizPage carries the horizontal lockup in the control panel and the bare
// mark as the tab icon. Light variants: the page is drawn on light gray.
var dagvizPage = logo.Page(dagvizHTML, logo.LockupOnLight, logo.MarkOnLight)

func Handler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(dagvizPage)
}
