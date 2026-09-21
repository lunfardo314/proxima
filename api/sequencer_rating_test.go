package api

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/stretchr/testify/require"
)

// TestDelegationRatings checks who takes part in the node-side delegation
// rating and what it says about them: an inactive sequencer and one leaving
// delegators nothing are absent, the rest hold the positions 1..n with draw
// probabilities summing to 100, the best one drawn most often.
func TestDelegationRatings(t *testing.T) {
	id := func(b byte) base.ChainID {
		var ret base.ChainID
		ret[0] = b
		return ret
	}
	const lrbSlot = 100
	candidates := []txbuildercore.SequencerCandidate{
		{ID: id(1), Slot: lrbSlot, Balance: 1000, ShareLeft: 900},
		{ID: id(2), Slot: lrbSlot - 1, Balance: 3000, ShareLeft: 700, FrozenCoverage: 3000},
		{ID: id(3), Slot: lrbSlot - 2, Balance: 2000, ShareLeft: 800},
		{ID: id(4), Slot: lrbSlot - txbuildercore.ActiveSequencerSlots - 1, Balance: 9000, ShareLeft: 999}, // inactive
		{ID: id(5), Slot: lrbSlot, Balance: 9000, ShareLeft: 0},                                            // leaves nothing
	}
	ratings := DelegationRatings(candidates, lrbSlot)
	require.Len(t, ratings, 3)
	require.NotContains(t, ratings, id(4))
	require.NotContains(t, ratings, id(5))

	positions := make(map[int]bool)
	var total float64
	for _, r := range ratings {
		positions[r.Position] = true
		total += r.Probability
	}
	require.Equal(t, map[int]bool{1: true, 2: true, 3: true}, positions)
	require.InDelta(t, 100, total, 1e-9)

	// id 1 wins the share left, the price criterion counted twice, and the
	// frozen-to-balance ratio; the only thing it loses is the balance
	require.Equal(t, 1, ratings[id(1)].Position)
	require.InDelta(t, 50, ratings[id(1)].Probability, 1e-9)
	require.Less(t, ratings[id(1)].Rating, ratings[id(2)].Rating)
}
