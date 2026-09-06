package dag_explorer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

// keyIterator is a minimal common.KVIterator over a fixed set of keys, enough
// for serveSlot which only calls IterateKeys.
type keyIterator struct{ keys [][]byte }

func (it *keyIterator) Iterate(f func(k, v []byte) bool) {
	for _, k := range it.keys {
		if !f(k, nil) {
			return
		}
	}
}

func (it *keyIterator) IterateKeys(f func(k []byte) bool) {
	for _, k := range it.keys {
		if !f(k) {
			return
		}
	}
}

// floodStore returns `count` distinct txids (all in one slot) for any prefix and
// reports every tx as absent, so each load appends exactly one (missing) vertex.
// That isolates the loader's vertex cap from tx parsing. iterCalls counts how
// many slot prefixes were scanned, so a test can assert the slots_back clamp.
type floodStore struct {
	keys      [][]byte
	iterCalls int
}

func newFloodStore(slot uint32, count int) *floodStore {
	keys := make([][]byte, count)
	for i := 0; i < count; i++ {
		txid := base.NewTransactionID(base.T(slot, 1), base.TransactionIDShort{byte(i), byte(i >> 8), byte(i >> 16)}, false)
		keys[i] = txid.Bytes()
	}
	return &floodStore{keys: keys}
}

func (s *floodStore) GetTxBytes(_ *base.TransactionID) []byte { return nil }
func (s *floodStore) HasTxBytes(_ *base.TransactionID) bool   { return false }
func (s *floodStore) Iterator(_ []byte) common.KVIterator {
	s.iterCalls++
	return &keyIterator{keys: s.keys}
}

// TestServeSlotVertexCap is the regression for audit FNET-3: an unbounded
// dag_explorer scan (huge slots_back, or a densely populated slot) must not
// materialise the whole txstore in RAM. The loader caps the vertex count and
// reports truncation.
func TestServeSlotVertexCap(t *testing.T) {
	store := newFloodStore(100, maxDagVizVertices+5000)

	req := httptest.NewRequest(http.MethodGet, "/api/slot?slot=100", nil)
	w := httptest.NewRecorder()
	serveSlot(w, req, store)

	require.Equal(t, http.StatusOK, w.Code)
	var g graph
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &g))
	require.LessOrEqual(t, len(g.Vertices), maxDagVizVertices, "vertex count must be capped")
	require.True(t, g.Truncated, "truncation must be reported when the cap is hit")
}

// TestServeSlotSlotsBackClamp checks that an out-of-range slots_back does not
// drive firstSlot toward 0 and scan the entire history. Without the clamp, a
// slot=1_000_000 request with slots_back=1_000_000 would scan a million slots;
// with it, at most maxSlotsBack+1 slot prefixes are touched.
func TestServeSlotSlotsBackClamp(t *testing.T) {
	store := newFloodStore(1_000_000, 10)

	req := httptest.NewRequest(http.MethodGet, "/api/slot?slot=1000000&slots_back=1000000", nil)
	w := httptest.NewRecorder()
	serveSlot(w, req, store)

	require.Equal(t, http.StatusOK, w.Code)
	require.LessOrEqual(t, store.iterCalls, maxSlotsBack+1, "slots_back must be clamped")
}
