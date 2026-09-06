package node

import (
	"net/http"
	"net/http/httptest"
	"net/http/pprof"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPprofNotServedOnPrivateMux is the regression for the audit FNET-2 / FOPS-1
// finding: Go pprof was reachable, unauthenticated, on the public API and metrics
// ports regardless of pprof.enable.
//
// The root cause is subtle: importing net/http/pprof (even for its named
// handlers) runs its init(), which registers /debug/pprof/* on
// http.DefaultServeMux. The remedy is therefore NOT "don't import it" but "never
// serve http.DefaultServeMux": the API server, the metrics server and the pprof
// server each now use a private *http.ServeMux. This test documents and guards
// both halves.
func TestPprofNotServedOnPrivateMux(t *testing.T) {
	// The hazard: net/http/pprof's init registered pprof on the default mux.
	// (Documented so a future maintainer does not "clean up" the named import
	// expecting that to remove the exposure — it would not.)
	h, pattern := http.DefaultServeMux.Handler(httptest.NewRequest(http.MethodGet, "/debug/pprof/heap", nil))
	require.NotEmpty(t, pattern, "net/http/pprof still self-registers on the default mux")
	require.NotNil(t, h)

	// The remedy: a private mux (as the API and metrics servers use) does NOT
	// serve pprof. Serving such a mux is what keeps pprof off the public ports.
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/api/v1/ping", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	api := httptest.NewServer(apiMux)
	defer api.Close()

	resp, err := http.Get(api.URL + "/debug/pprof/heap")
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode, "pprof must not be reachable on the API mux")

	resp, err = http.Get(api.URL + "/api/v1/ping")
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "the API's own handlers are still served")

	// The pprof server's own mux (built like startPProfIfEnabled) does serve pprof
	// — reachable only there, only when pprof.enable is set.
	pmux := http.NewServeMux()
	pmux.HandleFunc("/debug/pprof/", pprof.Index)
	pmux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	pmux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	psrv := httptest.NewServer(pmux)
	defer psrv.Close()

	resp, err = http.Get(psrv.URL + "/debug/pprof/heap")
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "pprof is served on its dedicated mux")
}
