package node_cmd

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// chainID builds a distinguishable ChainID from a single byte, so the tests can
// name candidates without constructing real chain origins.
func chainID(b byte) base.ChainID {
	var ret base.ChainID
	ret[0] = b
	return ret
}

// TestSelectDelegationTargetCut covers the delegator-cut filter the miner
// applies when it picks a delegation target on its own: a sequencer keeps its
// own cut, so what it can leave a delegator is 1000 minus that.
func TestSelectDelegationTargetCut(t *testing.T) {
	const nowSlot = 100

	// Only the second sequencer leaves enough: the first keeps 800 promille and
	// so tolerates a delegator cut of at most 200.
	t.Run("filters out sequencers that keep too much", func(t *testing.T) {
		got, err := selectDelegationTarget([]delegationTarget{
			{id: chainID(1), slot: nowSlot, tolerance: 200},
			{id: chainID(2), slot: nowSlot, tolerance: 900},
		}, nowSlot, 900)
		require.NoError(t, err)
		require.Equal(t, chainID(2), got)
	})

	// A tolerance exactly equal to the required cut is acceptable: the delegation
	// lock rejects only a share strictly below the delegator's floor.
	t.Run("tolerance equal to the required cut is accepted", func(t *testing.T) {
		got, err := selectDelegationTarget([]delegationTarget{
			{id: chainID(1), slot: nowSlot, tolerance: 900},
		}, nowSlot, 900)
		require.NoError(t, err)
		require.Equal(t, chainID(1), got)
	})

	// With no tolerant sequencer the miner refuses rather than delegating at a
	// cut that would be refused on freezing.
	t.Run("refuses when nobody tolerates the cut", func(t *testing.T) {
		_, err := selectDelegationTarget([]delegationTarget{
			{id: chainID(1), slot: nowSlot, tolerance: 200},
			{id: chainID(2), slot: nowSlot, tolerance: 500},
		}, nowSlot, 900)
		require.ErrorContains(t, err, "900")
	})

	// The staleness fallback must stay inside the tolerant set: a fresher
	// sequencer that keeps too much is not a valid target.
	t.Run("stale fallback only picks a tolerant sequencer", func(t *testing.T) {
		got, err := selectDelegationTarget([]delegationTarget{
			{id: chainID(1), slot: nowSlot, tolerance: 100},      // fresh, intolerant
			{id: chainID(2), slot: nowSlot - 50, tolerance: 900}, // stale, tolerant
		}, nowSlot, 900)
		require.NoError(t, err)
		require.Equal(t, chainID(2), got)
	})

	// An empty candidate set is a different failure from an intolerant one, and
	// must not claim the cut is the problem.
	t.Run("no sequencers at all", func(t *testing.T) {
		_, err := selectDelegationTarget(nil, nowSlot, 900)
		require.ErrorContains(t, err, "no sequencer to delegate to")
	})
}
