package node_cmd

import (
	"testing"

	"github.com/lunfardo314/proxima/api"
	"github.com/stretchr/testify/require"
)

// The stream URL is derived from the node API endpoint the wallet is already
// configured with, so a miner needs no second setting to subscribe.
func TestMiningStreamURL(t *testing.T) {
	for _, c := range []struct{ endpoint, want string }{
		{"http://127.0.0.1:8001", "ws://127.0.0.1:8001" + api.PathMiningTxStream},
		{"https://node.example:443", "wss://node.example:443" + api.PathMiningTxStream},
		{"ws://127.0.0.1:8001", "ws://127.0.0.1:8001" + api.PathMiningTxStream},
		{"wss://node.example", "wss://node.example" + api.PathMiningTxStream},
		// a path or query on the endpoint is replaced, not appended to
		{"http://127.0.0.1:8001/api/v1?x=1", "ws://127.0.0.1:8001" + api.PathMiningTxStream},
		{"  http://127.0.0.1:8001  ", "ws://127.0.0.1:8001" + api.PathMiningTxStream},
	} {
		got, err := miningStreamURL(c.endpoint)
		require.NoErrorf(t, err, "endpoint %q", c.endpoint)
		require.Equalf(t, c.want, got, "endpoint %q", c.endpoint)
	}
}

func TestMiningStreamURLRejectsBad(t *testing.T) {
	for _, bad := range []string{"", "   ", "ftp://node.example", "http://", "://nope"} {
		_, err := miningStreamURL(bad)
		require.Errorf(t, err, "endpoint %q must be rejected", bad)
	}
}

// --no-stream is an explicit opt-out; otherwise the configured node is always
// included and extras are appended without duplicates.
func TestMiningStreamEndpoints(t *testing.T) {
	require.Nil(t, miningStreamEndpoints(true, []string{"http://a"}), "--no-stream disables subscription")

	// viper is not configured in this test, so api.endpoint resolves empty and
	// only the extras remain — which also pins the de-duplication
	got := miningStreamEndpoints(false, []string{"http://a", " http://b ", "http://a", ""})
	require.Equal(t, []string{"http://a", "http://b"}, got)
}
