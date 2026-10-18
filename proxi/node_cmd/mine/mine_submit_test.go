package mine

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// A transit goes to the wallet's node and to every extra node as well: from a
// slow link one path alone often reaches the sequencers after settlement. The
// wallet's node alone decides whether the submit succeeded; an extra node that
// refuses or is down changes nothing.

// submitNode is a stand-in node API that answers every submit with ok and
// records the transaction bytes it received.
func submitNode(t *testing.T, ok bool) (*httptest.Server, chan string) {
	got := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req api.SubmitTxRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		got <- req.TxBytes
		res := api.SubmitTxResponse{OK: ok, TxID: hex.EncodeToString(make([]byte, base.TransactionIDLength))}
		if !ok {
			res = api.SubmitTxResponse{Stage: "full", Error: "refused"}
		}
		_ = json.NewEncoder(w).Encode(res)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func TestSubmitReachesExtraNodes(t *testing.T) {
	primary, atPrimary := submitNode(t, true)
	extra, atExtra := submitNode(t, false)
	m := &miner{
		c:            client.NewWithGoogleDNS(primary.URL),
		extraClients: []*client.APIClient{client.NewWithGoogleDNS(extra.URL), client.NewWithGoogleDNS("http://127.0.0.1:1")},
	}
	tx := []byte{1, 2, 3}

	require.True(t, m.submit(tx, []byte{9}), "the wallet's node accepted it; the extra nodes do not count")
	require.Equal(t, hex.EncodeToString(tx), <-atPrimary)
	select {
	case b := <-atExtra:
		require.Equal(t, hex.EncodeToString(tx), b)
	case <-time.After(5 * time.Second):
		t.Fatal("the extra node did not get the transit")
	}

	// the other way round: an extra node accepting does not make a refused submit succeed
	m.c, m.extraClients = client.NewWithGoogleDNS(extra.URL), []*client.APIClient{client.NewWithGoogleDNS(primary.URL)}
	require.False(t, m.submit(tx, []byte{9}))
}
