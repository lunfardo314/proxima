package peering

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lunfardo314/proxima/api"
	"github.com/stretchr/testify/require"
)

// TestConnectivityRecordBounds checks the bounds on connectivity records received from peers: names
// must have the masked-name form (16 lowercase hex digits), a record may list at most
// maxConnectivityByPeer peers, at most maxConnectivityOrigins origins are kept (an origin already
// kept is still refreshed), and the matrix is not built over more than maxConnectivityMatrixNs names.
// Without them one peer could make every node keep and compute without limit.
func TestConnectivityRecordBounds(t *testing.T) {
	name := func(i int) string { return fmt.Sprintf("%016x", i) }

	require.True(t, validConnectivityRecord(&PeerConnections{Name: name(1), ByPeer: map[string]uint64{name(2): 10}}))
	require.False(t, validConnectivityRecord(&PeerConnections{Name: "", ByPeer: nil}))
	require.False(t, validConnectivityRecord(&PeerConnections{Name: strings.ToUpper("a1b2c3d4e5f60718")}))
	require.False(t, validConnectivityRecord(&PeerConnections{Name: name(1), ByPeer: map[string]uint64{"x": 10}}))
	tooMany := make(map[string]uint64)
	for i := 0; i <= maxConnectivityByPeer; i++ {
		tooMany[name(i)] = 1
	}
	require.False(t, validConnectivityRecord(&PeerConnections{Name: name(1), ByPeer: tooMany}))

	// origins: the store stops growing at the cap, a kept origin is still refreshed
	ps := NewPeersDummy()
	for i := 0; i < maxConnectivityOrigins+10; i++ {
		ps.handleConnectivityRecord("", PeerConnections{Name: name(i), Timestamp: 1})
	}
	require.Len(t, ps.connMap, maxConnectivityOrigins)
	ps.handleConnectivityRecord("", PeerConnections{Name: name(0), Timestamp: 2, Seq: 7})
	require.EqualValues(t, 7, ps.connMap[name(0)].rec.Seq)

	// matrix: built at the cap, refused above it
	records := func(n int) []api.ConnectivityRecord {
		ret := make([]api.ConnectivityRecord, n)
		for i := range ret {
			ret[i] = api.ConnectivityRecord{Name: name(i)}
		}
		return ret
	}
	require.NotNil(t, buildConnectivityMatrix(records(10)))
	require.Nil(t, buildConnectivityMatrix(records(maxConnectivityMatrixNs+1)))
}
