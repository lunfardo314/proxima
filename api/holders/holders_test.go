package holders

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lunfardo314/proxima/api"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

// TestDisabled: with the browser off (the default) both the endpoint and the
// page refuse before the state is touched, and the refusal names the key to
// set. The env is nil, so a state access would panic.
func TestDisabled(t *testing.T) {
	viper.Set(ConfigKeyEnable, false)

	req := httptest.NewRequest(http.MethodGet, api.PathGetHolders, nil)
	w := httptest.NewRecorder()
	serveData(w, req, nil)
	data, _ := io.ReadAll(w.Result().Body)
	require.Contains(t, string(data), ConfigKeyEnable)

	w = httptest.NewRecorder()
	servePage(w, httptest.NewRequest(http.MethodGet, api.PathHolders, nil))
	require.Equal(t, http.StatusNotFound, w.Result().StatusCode)
}

// TestBadMaxUTXOs: a max_utxos which is not a positive integer is rejected
// before the state is touched. Zero and negatives are refused rather than read
// as "no cap", which is the one thing the parameter must not mean.
func TestBadMaxUTXOs(t *testing.T) {
	viper.Set(ConfigKeyEnable, true)
	defer viper.Set(ConfigKeyEnable, false)

	for _, v := range []string{"abc", "0", "-5", ""} {
		req := httptest.NewRequest(http.MethodGet, api.PathGetHolders+"?max_utxos="+v, nil)
		w := httptest.NewRecorder()
		serveData(w, req, nil)

		// api.WriteErr returns 2xx with an error envelope; assert by content.
		data, _ := io.ReadAll(w.Result().Body)
		require.Contains(t, string(data), "max_utxos: positive integer expected", "max_utxos=%q", v)
	}
}

// TestMaxUTXOsConfig: the configured scan cap defaults when absent or not
// positive, and can never exceed the server's ceiling.
func TestMaxUTXOsConfig(t *testing.T) {
	defer viper.Set(ConfigKeyMaxUTXOs, nil)

	for cfg, want := range map[int]int{
		0:                   DefaultMaxUTXOs,
		-1:                  DefaultMaxUTXOs,
		10:                  10,
		MaxUTXOsCeiling * 5: MaxUTXOsCeiling,
	} {
		viper.Set(ConfigKeyMaxUTXOs, cfg)
		require.Equal(t, want, maxUTXOs(), "config %d", cfg)
	}
}

// TestPageEnabled: with the browser on, the page is served as HTML, with the
// logo substituted in and the data fetched from the get_holders endpoint.
func TestPageEnabled(t *testing.T) {
	viper.Set(ConfigKeyEnable, true)
	defer viper.Set(ConfigKeyEnable, false)

	w := httptest.NewRecorder()
	servePage(w, httptest.NewRequest(http.MethodGet, api.PathHolders, nil))
	require.Equal(t, http.StatusOK, w.Result().StatusCode)
	page, _ := io.ReadAll(w.Result().Body)
	require.Contains(t, string(page), api.PathGetHolders)
	require.NotContains(t, string(page), "<!--LOGO-->")
}
