package peering

import (
	"testing"

	"github.com/lunfardo314/proxima/api"
	"github.com/stretchr/testify/require"
)

// helper: look up d(a,b) in the packed upper-triangular matrix by node name.
func matrixDist(m *api.ConnectivityMatrix, a, b string) uint64 {
	ia, ib := -1, -1
	for i, n := range m.Nodes {
		if n == a {
			ia = i
		}
		if n == b {
			ib = i
		}
	}
	if ia < 0 || ib < 0 {
		return 0
	}
	if ia == ib {
		return 0
	}
	if ia > ib {
		ia, ib = ib, ia
	}
	return m.Matrix[ia][ib-ia-1] // row ia, offset for column ib
}

// buildConnectivityMatrix must: average disagreeing directions, fill missing pairs
// via shortest path, and carry per-node contribution parallel to Nodes.
func TestBuildConnectivityMatrix(t *testing.T) {
	// Three nodes a,b,c. Direct edges a-b and b-c given (both directions, slightly
	// disagreeing); a-c has NO direct edge and must come from the a-b-c path.
	recs := []api.ConnectivityRecord{
		{Name: "a", ConsensusContribution: 100, ByPeer: map[string]uint64{"b": 1000}},
		{Name: "b", ByPeer: map[string]uint64{"a": 1200, "c": 2000}},
		{Name: "c", ConsensusContribution: 50, ByPeer: map[string]uint64{"b": 2200}},
	}
	m := buildConnectivityMatrix(recs)

	require.Equal(t, []string{"a", "b", "c"}, m.Nodes, "nodes sorted & complete")
	require.Equal(t, []uint64{100, 0, 50}, m.Contribution, "contribution parallel to nodes, 0 for non-seq")

	// a-b: average of 1000 (a->b) and 1200 (b->a) = 1100
	require.EqualValues(t, 1100, matrixDist(m, "a", "b"))
	// b-c: average of 2000 and 2200 = 2100
	require.EqualValues(t, 2100, matrixDist(m, "b", "c"))
	// a-c: no direct edge -> shortest path a-b-c = 1100 + 2100 = 3200
	require.EqualValues(t, 3200, matrixDist(m, "a", "c"))

	// symmetry via the packed form is implicit (single stored value per pair).
	require.EqualValues(t, matrixDist(m, "c", "a"), matrixDist(m, "a", "c"))
}

// A direct edge that violates the triangle inequality must be shortened by the
// metric closure: if a-c direct is 10000 but a-b-c is 3200, d(a,c) becomes 3200.
func TestMatrixMetricClosureShortensViolatingEdge(t *testing.T) {
	recs := []api.ConnectivityRecord{
		{Name: "a", ByPeer: map[string]uint64{"b": 1100, "c": 10000}},
		{Name: "b", ByPeer: map[string]uint64{"a": 1100, "c": 2100}},
		{Name: "c", ByPeer: map[string]uint64{"a": 10000, "b": 2100}},
	}
	m := buildConnectivityMatrix(recs)
	require.EqualValues(t, 3200, matrixDist(m, "a", "c"), "long direct edge replaced by shorter 2-hop path")
}

// Disconnected components: pairs with no path are reported as the 0 sentinel.
func TestMatrixDisconnected(t *testing.T) {
	// a-b connected; c isolated (appears only as its own record with no edges).
	recs := []api.ConnectivityRecord{
		{Name: "a", ByPeer: map[string]uint64{"b": 1000}},
		{Name: "b", ByPeer: map[string]uint64{"a": 1000}},
		{Name: "c", ByPeer: map[string]uint64{}},
	}
	m := buildConnectivityMatrix(recs)
	require.EqualValues(t, 1000, matrixDist(m, "a", "b"))
	require.EqualValues(t, 0, matrixDist(m, "a", "c"), "no path -> 0 sentinel")
	require.EqualValues(t, 0, matrixDist(m, "b", "c"))
}
