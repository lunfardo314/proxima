package client

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNodeDownClassification pins what the client reports when the node is not
// answering. Through a reverse proxy a stopped node is an HTML 502 page, which
// must become a short ErrNodeDown error without the page; with no proxy the
// connection is refused, which must become ErrNodeDown as well. A plain
// application error status stays an ordinary error, and a 200 reads the body.
func TestNodeDownClassification(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html><head><title>502 Bad Gateway</title></head></html>"))
	}))
	defer gateway.Close()
	_, err := readBody(http.Get(gateway.URL + "/api/v1/x"))
	require.ErrorIs(t, err, ErrNodeDown)
	require.NotContains(t, err.Error(), "<html>")
	require.Contains(t, err.Error(), "502")

	// a port nobody listens on
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	_, err = readBody(http.Get("http://" + addr + "/api/v1/x"))
	require.ErrorIs(t, err, ErrNodeDown)

	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer other.Close()
	_, err = readBody(http.Get(other.URL + "/api/v1/x"))
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrNodeDown)

	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ok.Close()
	body, err := readBody(http.Get(ok.URL + "/api/v1/x"))
	require.NoError(t, err)
	require.JSONEq(t, `{"ok":true}`, string(body))
}
