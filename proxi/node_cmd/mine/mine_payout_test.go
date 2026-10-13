package mine

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/stretchr/testify/require"
)

// TestActiveSequencersFetchInterval pins how often the miner reads the
// sequencer list. A public node rate-limits that scan (nginx answers 429), and
// a failed read used to turn the round's reward into the opening. The list is
// fetched once per seqFetchSlots target slots; a failed fetch falls back to the
// last list while it is under maxSeqFetchSlots old, and is an error after that.
func TestActiveSequencersFetchInterval(t *testing.T) {
	var calls atomic.Int32
	var limited atomic.Bool
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, api.PathGetSequencers, r.URL.Path)
		calls.Add(1)
		if limited.Load() {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		// an empty sequencer set is enough: the test counts fetches, not candidates
		_, _ = w.Write([]byte(`{"lrbid":"00001cad010187a67a1de869ee04bf19e4822342a569a15fb07a3f275606f18a","sequencers":{}}`))
	}))
	defer node.Close()

	m := &miner{c: client.NewWithGoogleDNS(node.URL), seqFetchSlots: 2}

	// the first round fetches; rounds of the same and of the next slot reuse it
	for _, slot := range []uint32{100, 100, 101} {
		_, err := m.activeSequencers(slot)
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, calls.Load())

	// two slots on, the list is refetched
	_, err := m.activeSequencers(102)
	require.NoError(t, err)
	require.EqualValues(t, 2, calls.Load())

	// rate-limited: every attempt fails, and the list of slot 102 stands in
	// while it is under maxSeqFetchSlots old
	limited.Store(true)
	seqs, err := m.activeSequencers(104)
	require.NoError(t, err)
	require.NotNil(t, seqs)
	require.EqualValues(t, 2+seqFetchAttempts, calls.Load())
	require.EqualValues(t, 102, m.seqsSlot)

	// too old to stand in: the error reaches choosePayout, which pays the opening
	_, err = m.activeSequencers(102 + maxSeqFetchSlots)
	require.Error(t, err)
}
