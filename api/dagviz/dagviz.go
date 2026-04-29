package dagviz

import (
	_ "embed"
	"net/http"
)

//go:embed dagviz.html
var dagvizHTML []byte

func Handler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(dagvizHTML)
}
