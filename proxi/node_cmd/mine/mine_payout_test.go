package mine

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/consolidator"
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

// TestPlaceRewardPinnedFirst reproduces a reported case: with autodelegate
// pinned, the pinned sequencer was down for a restart, a win opened a
// delegation elsewhere, and once the pinned sequencer was back every top-up
// still went to that delegation, being the smallest. While the pinned target
// is active, its delegations come first; another one is topped up only when
// none at the pinned target can take the reward and the count is reached.
func TestPlaceRewardPinnedFirst(t *testing.T) {
	pinned, other := base.ChainID{1}, base.ChainID{2}
	const a = 95_000_000
	m := &miner{
		consts: &txbuildercore.Constants{MineDelegationAmountProx: 50, SmallestAmountsPerBaseToken: 1_000_000, MineTagAlongFee: 1_000_000},
		cfg:    consolidator.Config{DelegateTo: &pinned, TargetDelegations: 1, TargetSize: 10_000_000_000},
	}
	del := func(id byte, target base.ChainID, balance uint64) *ownDelegation {
		return &ownDelegation{view: &txbuildercore.DelegationOutputView{ChainID: base.ChainID{id}, Target: target}, balance: balance}
	}
	both := map[base.ChainID]txbuildercore.SequencerCandidate{
		pinned: {ID: pinned, ShareLeft: 900}, other: {ID: other, ShareLeft: 900},
	}
	stray, atPinned := del(10, other, 50_000_000), del(11, pinned, 5_000_000_000)

	// the pinned target is back: its delegation is topped up, not the smaller stray one
	p, ok := m.placeReward(a, both, []*ownDelegation{stray, atPinned})
	require.True(t, ok)
	require.True(t, p.topUp)
	require.Equal(t, atPinned.view.ChainID, p.delegationID)

	// no delegation at the pinned target yet: a new one there, though the
	// count is already reached by the stray one
	p, ok = m.placeReward(a, both, []*ownDelegation{stray})
	require.True(t, ok)
	require.EqualValues(t, txbuildercore.MinePayoutDelegation, p.option)
	require.Equal(t, pinned, p.target)

	// the pinned delegation cannot take a top-up (frozen at a share the target
	// no longer leaves) and the count is reached: the stray one is topped up
	frozen := del(11, pinned, 5_000_000_000)
	frozen.frozen, frozen.view.AdvanceShare = true, 950
	p, ok = m.placeReward(a, both, []*ownDelegation{stray, frozen})
	require.True(t, ok)
	require.True(t, p.topUp)
	require.Equal(t, stray.view.ChainID, p.delegationID)

	// the pinned target is down: the delegations elsewhere take the reward as before
	onlyOther := map[base.ChainID]txbuildercore.SequencerCandidate{other: both[other]}
	p, ok = m.placeReward(a, onlyOther, []*ownDelegation{stray, atPinned})
	require.True(t, ok)
	require.True(t, p.topUp)
	require.Equal(t, stray.view.ChainID, p.delegationID)
}
