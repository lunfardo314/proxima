package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lunfardo314/proxima/api"
	"github.com/stretchr/testify/require"
)

// TestGetHoldings_BadMaxUTXOs: a max_utxos which is not a positive integer is
// rejected before the state is touched. Zero and negatives are refused rather
// than read as "no cap", which is the one thing the parameter must not mean.
func TestGetHoldings_BadMaxUTXOs(t *testing.T) {
	srv := &server{}

	for _, v := range []string{"abc", "0", "-5", ""} {
		req := httptest.NewRequest(http.MethodGet, api.PathGetHoldings+"?max_utxos="+v, nil)
		w := httptest.NewRecorder()
		srv.getHoldings(w, req)

		// api.WriteErr returns 2xx with an error envelope; assert by content.
		data, _ := io.ReadAll(w.Result().Body)
		require.Contains(t, string(data), "max_utxos: positive integer expected", "max_utxos=%q", v)
	}
}
