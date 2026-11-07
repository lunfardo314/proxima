package mine

import (
	"encoding/binary"
	"testing"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// Transits whose work is their transaction ID (kb/library_upgrade6_mining.md): the
// registry keeps only the best few per predecessor, and a transit with several
// tag-alongs defers others when any of its targets is an active sequencer.

// TestRegistryKeepsTheBestHashTransits fills one predecessor with more transits than
// maxHashTransitsPerPred, in arrival order unrelated to their rank, and checks that the
// best ones by the canonical order are those kept, in both indexes.
func TestRegistryKeepsTheBestHashTransits(t *testing.T) {
	r := NewRegistry(nil)
	pred := predecessor(1)
	n := maxHashTransitsPerPred + 10
	for i := 0; i < n; i++ {
		work := make([]byte, 8)
		binary.BigEndian.PutUint64(work, uint64((i*7919)%n)) // a permutation of 0..n-1
		e := &registeredTransit{desc: *transitOf(pred, 10, work, byte(i))}
		r.byTxID[e.desc.TxID] = e
		r.byPred[pred] = append(r.byPred[pred], e)
		r.keepBestLocked(pred)
	}
	require.Len(t, r.byPred[pred], maxHashTransitsPerPred)
	require.Len(t, r.byTxID, maxHashTransitsPerPred)
	for _, e := range r.byPred[pred] {
		require.Less(t, binary.BigEndian.Uint64(e.desc.Work), uint64(maxHashTransitsPerPred), "only the smallest work values stay")
	}
}

// TestTransitAddressedToAnyActiveTarget checks the eligibility of a rival: any active
// target among its tag-alongs makes it one, none (or no tag-along at all) does not.
func TestTransitAddressedToAnyActiveTarget(t *testing.T) {
	var a, b base.ChainID
	a[0], b[0] = 1, 2
	onlyB := func(id base.ChainID) bool { return id == b }

	m := transitOf(predecessor(1), 10, []byte{1}, 1)
	require.False(t, m.addressedTo(onlyB), "no tag-along: not a rival")
	m.Targets = []base.ChainID{a}
	require.False(t, m.addressedTo(onlyB), "only an inactive target")
	m.Targets = []base.ChainID{a, b}
	require.True(t, m.addressedTo(onlyB), "one active target among several")
}
