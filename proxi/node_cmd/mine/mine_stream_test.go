package mine

import (
	"strconv"
	"testing"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/streaming"
	"github.com/stretchr/testify/require"
)

// The stream URL is derived from the node API endpoint the wallet is already
// configured with, so a miner needs no second setting to subscribe.
func TestMiningStreamURL(t *testing.T) {
	// the stream URL names the ledger the miner runs on and the miner version
	// it mines with now, both of which the node checks right after the upgrade
	const hash = "aa11"
	const version = 4
	q := "?" + streaming.MiningLedgerHashQueryKey + "=" + hash + "&" + streaming.MiningMinerVersionQueryKey + "=" + strconv.Itoa(version)
	for _, c := range []struct{ endpoint, want string }{
		{"http://127.0.0.1:8001", "ws://127.0.0.1:8001" + api.PathMiningTxStream + q},
		{"https://node.example:443", "wss://node.example:443" + api.PathMiningTxStream + q},
		{"ws://127.0.0.1:8001", "ws://127.0.0.1:8001" + api.PathMiningTxStream + q},
		{"wss://node.example", "wss://node.example" + api.PathMiningTxStream + q},
		// a path or query on the endpoint is replaced, not appended to
		{"http://127.0.0.1:8001/api/v1?x=1", "ws://127.0.0.1:8001" + api.PathMiningTxStream + q},
		{"  http://127.0.0.1:8001  ", "ws://127.0.0.1:8001" + api.PathMiningTxStream + q},
	} {
		got, err := miningStreamURL(c.endpoint, hash, version)
		require.NoErrorf(t, err, "endpoint %q", c.endpoint)
		require.Equalf(t, c.want, got, "endpoint %q", c.endpoint)
	}
}

func TestMiningStreamURLRejectsBad(t *testing.T) {
	for _, bad := range []string{"", "   ", "ftp://node.example", "http://", "://nope"} {
		_, err := miningStreamURL(bad, "aa11", MinerVersion)
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
