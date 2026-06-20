package peering

import (
	"math"
	"sort"
	"time"

	"github.com/lunfardo314/proxima/api"
)

// connMatrixRefreshInterval bounds how often the (potentially O(N^3)) distance
// matrix is recomputed. Served from cache between refreshes.
const connMatrixRefreshInterval = 25 * time.Second

// matrixINF is the "no edge / unreachable" sentinel used internally during the
// shortest-path closure. Kept well below MaxUint64 so a+b never overflows.
const matrixINF = uint64(math.MaxUint64) >> 2

// GetConnectivityMatrix returns the symmetric pairwise distance metric d(i,j)
// derived from the connectivity map. The result is cached and lazily recomputed
// at most once per connMatrixRefreshInterval (the matrix is expensive for large N).
func (ps *Peers) GetConnectivityMatrix() *api.ConnectivityMatrix {
	ps.connMatrixMutex.Lock()
	defer ps.connMatrixMutex.Unlock()

	if ps.connMatrix != nil && time.Since(ps.connMatrixComputed) < connMatrixRefreshInterval {
		return ps.connMatrix
	}
	ps.connMatrix = ps.computeConnectivityMatrix()
	ps.connMatrixComputed = time.Now()
	return ps.connMatrix
}

// computeConnectivityMatrix snapshots the current connectivity map and builds the
// distance matrix, stamping this node's own masked name as Self.
func (ps *Peers) computeConnectivityMatrix() *api.ConnectivityMatrix {
	ps.connMutex.RLock()
	records := make([]api.ConnectivityRecord, 0, len(ps.connMap))
	for _, e := range ps.connMap {
		records = append(records, api.ConnectivityRecord{
			Name:                  e.rec.Name,
			ConsensusContribution: e.rec.ConsensusContribution,
			ByPeer:                e.rec.ByPeer,
		})
	}
	ps.connMutex.RUnlock()

	m := buildConnectivityMatrix(records)
	m.Self = ps.ownMaskedName()
	return m
}

// buildConnectivityMatrix builds d(i,j) from a set of connectivity records (pure,
// no node state — directly unit-testable):
//  1. the node set is the union of all names that appear as a record origin or as
//     a byPeer key;
//  2. each unordered pair's direct distance is the AVERAGE of whichever directions
//     are present (a->b and/or b->a) — reconciling perspective disagreement;
//  3. shortest-path (Floyd-Warshall) metric closure fills missing pairs and enforces
//     the triangle inequality, so d(a,b) <= d(a,c)+d(c,b) holds throughout.
//
// Distances are microseconds. The result is packed as the upper triangle (Self unset).
func buildConnectivityMatrix(records []api.ConnectivityRecord) *api.ConnectivityMatrix {
	// 1. node set (sorted for a stable, deterministic index)
	nameSet := make(map[string]struct{})
	for _, r := range records {
		nameSet[r.Name] = struct{}{}
		for p := range r.ByPeer {
			nameSet[p] = struct{}{}
		}
	}
	names := make([]string, 0, len(nameSet))
	for n := range nameSet {
		names = append(names, n)
	}
	sort.Strings(names)
	idx := make(map[string]int, len(names))
	for i, n := range names {
		idx[n] = i
	}
	n := len(names)

	contribution := make([]uint64, n)
	for _, r := range records {
		if r.ConsensusContribution > 0 {
			contribution[idx[r.Name]] = r.ConsensusContribution
		}
	}

	// 2. averaged symmetric direct distances
	type pair struct{ i, j int }
	sum := make(map[pair]uint64)
	cnt := make(map[pair]int)
	for _, r := range records {
		a := idx[r.Name]
		for p, rtt := range r.ByPeer {
			b, ok := idx[p]
			if !ok || a == b {
				continue
			}
			k := pair{a, b}
			if a > b {
				k = pair{b, a}
			}
			sum[k] += rtt
			cnt[k]++
		}
	}

	dist := make([][]uint64, n)
	for i := range dist {
		dist[i] = make([]uint64, n)
		for j := range dist[i] {
			if i == j {
				dist[i][j] = 0
			} else {
				dist[i][j] = matrixINF
			}
		}
	}
	for k, s := range sum {
		d := s / uint64(cnt[k])
		dist[k.i][k.j] = d
		dist[k.j][k.i] = d
	}

	// 4. Floyd-Warshall metric closure
	for kk := 0; kk < n; kk++ {
		for i := 0; i < n; i++ {
			if dist[i][kk] == matrixINF {
				continue
			}
			for j := 0; j < n; j++ {
				if dist[kk][j] == matrixINF {
					continue
				}
				if nd := dist[i][kk] + dist[kk][j]; nd < dist[i][j] {
					dist[i][j] = nd
				}
			}
		}
	}

	// pack upper triangle; unreachable (INF) -> 0 sentinel
	matrix := make([][]uint64, n)
	for i := 0; i < n; i++ {
		row := make([]uint64, 0, n-1-i)
		for j := i + 1; j < n; j++ {
			d := dist[i][j]
			if d == matrixINF {
				d = 0
			}
			row = append(row, d)
		}
		matrix[i] = row
	}

	return &api.ConnectivityMatrix{
		CapturedAt:   time.Now().UnixNano(),
		Nodes:        names,
		Contribution: contribution,
		Matrix:       matrix,
	}
}
