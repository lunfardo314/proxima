package mine

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/stretchr/testify/require"
)

// TestActiveSequencersFetchInterval pins how often the miner reads the
// sequencer list. A public node rate-limits that scan (nginx answers 429), and
// a failed read used to turn the round's reward into the opening. The list is
// fetched once per seqFetchSlots target slots; when a fetch fails the payout
// targets come from the last list, whatever its age.
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

	m := &miner{c: client.NewWithGoogleDNS(node.URL), seqFetchSlots: 2, stalledTargets: make(map[base.ChainID]uint32)}

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

	// rate-limited: every attempt fails and the fetch is an error, but the
	// payout targets still come from the list of slot 102, however old
	limited.Store(true)
	_, err = m.activeSequencers(104)
	require.Error(t, err)
	require.EqualValues(t, 2+seqFetchAttempts, calls.Load())
	require.NotNil(t, m.targetCandidates(102+10*maxSeqFetchSlots))
	require.EqualValues(t, 102, m.seqsSlot)
}

// TestStalledTargetsLeftOut pins that a sequencer which let one of the
// miner's transits stall is no payout target until its exclusion ends, while
// the others stay. The list is set fresh so that no node is called.
func TestStalledTargetsLeftOut(t *testing.T) {
	stalled, other := base.RandomChainID(), base.RandomChainID()
	m := &miner{
		seqFetchSlots:  maxSeqFetchSlots,
		seqs:           map[base.ChainID]txbuildercore.SequencerCandidate{stalled: {ID: stalled}, other: {ID: other}},
		seqsSlot:       200,
		stalledTargets: map[base.ChainID]uint32{stalled: 202},
	}
	got := m.targetCandidates(201)
	require.NotContains(t, got, stalled)
	require.Contains(t, got, other)

	// from the end slot of the exclusion on it is a target again
	got = m.targetCandidates(202)
	require.Contains(t, got, stalled)
	require.Contains(t, got, other)

	// with every target left out the exclusions step aside: the stall may have
	// been the node's, and an empty target set would stop the miner
	m.stalledTargets[other] = 210
	m.stalledTargets[stalled] = 210
	require.Len(t, m.targetCandidates(203), 2)
}
