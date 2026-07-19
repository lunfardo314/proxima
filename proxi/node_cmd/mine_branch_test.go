package node_cmd

import (
	"testing"
	"time"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/stretchr/testify/require"
)

// Fork detection of the speculative miner. The mine chain is a singleton, so a
// confirmed tip is fully described by (transition counter, output ID): the
// counter says which height was won, the ID says by whom. These cases pin the
// decisions the miner makes from that pair alone — no node, no ledger library.

// tipAt builds a confirmed-tip stand-in at the given height. The output ID is
// derived from `who` so two miners at the same height produce different IDs.
func tipAt(counter uint64, who byte) *mineTip {
	var txid base.TransactionID
	txid[len(txid)-1] = who
	return &mineTip{
		oid: base.MustNewOutputID(txid, 0),
		cc:  &txbuildercore.ChainConstraintView{TransitionCounter: counter},
		ml:  &txbuildercore.MineLockView{},
	}
}

// newTestMiner is a miner with only the fields onConfirmedTip touches, anchored
// at the given height.
func newTestMiner(anchorCounter uint64) *miner {
	m := &miner{a: 1000, mode: modeStash, perC: 1, ourChain: make(map[uint64]base.OutputID)}
	m.setAnchor(tipAt(anchorCounter, 0))
	m.st.orphaned = 0 // the initial anchor is not an orphaning event
	return m
}

// submitting a transit and then seeing exactly it confirmed must count as our
// own confirmed transit and leave the branch alive.
func TestOnConfirmedTipOwnTransitConfirmed(t *testing.T) {
	m := newTestMiner(5)
	own := tipAt(6, 1)
	m.recordSubmitted(own)

	verdict, _ := m.onConfirmedTip(tipAt(6, 1))
	require.Equal(t, tipConfirmedOurs, verdict)
	require.Equal(t, 1, m.st.transits)
	require.EqualValues(t, 1000, m.st.minted)
	require.EqualValues(t, 6, m.confirmed)
	require.Nil(t, m.pending)
	require.False(t, m.abort.Load())
}

// the LRB can jump several heights between polls. Because the chain is a
// singleton, a confirmed successor implies its whole predecessor chain, so all
// skipped heights count at once.
func TestOnConfirmedTipCountsSkippedHeights(t *testing.T) {
	m := newTestMiner(5)
	for c := uint64(6); c <= 8; c++ {
		m.recordSubmitted(tipAt(c, 1))
	}

	verdict, _ := m.onConfirmedTip(tipAt(8, 1))
	require.Equal(t, tipConfirmedOurs, verdict)
	require.Equal(t, 3, m.st.transits)
	require.EqualValues(t, 8, m.confirmed)
}

// a competing transit confirmed at a height we also submitted kills the whole
// speculative branch above the last confirmed height.
func TestOnConfirmedTipCompetingTransitReanchors(t *testing.T) {
	m := newTestMiner(5)
	for c := uint64(6); c <= 9; c++ {
		m.recordSubmitted(tipAt(c, 1))
	}

	competitor := tipAt(6, 2) // same height, different miner
	verdict, _ := m.onConfirmedTip(competitor)
	require.Equal(t, tipReanchor, verdict)
	require.True(t, m.abort.Load())
	require.Equal(t, competitor, m.pending)
	require.Equal(t, 0, m.st.transits)

	// the loop picks the tip up, which clears the abort flag and drops heights
	// 6..9 as orphaned
	require.Equal(t, competitor, m.takePending())
	require.False(t, m.abort.Load())
	m.setAnchor(competitor)
	require.Equal(t, 4, m.st.orphaned)
	require.EqualValues(t, 6, m.confirmed)
	require.Empty(t, m.ourChain)
}

// a height nobody of ours submitted is by definition a competitor's.
func TestOnConfirmedTipUnknownHeightReanchors(t *testing.T) {
	m := newTestMiner(5)
	verdict, _ := m.onConfirmedTip(tipAt(6, 2))
	require.Equal(t, tipReanchor, verdict)
	require.True(t, m.abort.Load())
}

// an LRB that has not caught up yet is not a signal on its own.
func TestOnConfirmedTipLaggingLRBIsNoChange(t *testing.T) {
	m := newTestMiner(5)
	m.recordSubmitted(tipAt(6, 1))

	verdict, _ := m.onConfirmedTip(tipAt(5, 0))
	require.Equal(t, tipNoChange, verdict)
	require.False(t, m.abort.Load())
	require.Nil(t, m.pending)
}

// ...but if our submitted transits stop being confirmed altogether, the branch
// is presumed lost even though no competing transit is visible.
func TestOnConfirmedTipStallReanchors(t *testing.T) {
	m := newTestMiner(5)
	m.recordSubmitted(tipAt(6, 1))
	m.lastConfirmedAt = time.Now().Add(-mineConfirmationStall - time.Second)

	verdict, _ := m.onConfirmedTip(tipAt(5, 0))
	require.Equal(t, tipReanchor, verdict)
	require.True(t, m.abort.Load())
}

// with nothing in flight a stale confirmation time must not trigger a re-anchor.
func TestOnConfirmedTipNothingInFlightNeverStalls(t *testing.T) {
	m := newTestMiner(5)
	m.lastConfirmedAt = time.Now().Add(-10 * mineConfirmationStall)

	verdict, _ := m.onConfirmedTip(tipAt(5, 0))
	require.Equal(t, tipNoChange, verdict)
	require.False(t, m.abort.Load())
}

// delegate mode accumulates confirmed transits and only fires every --per.
func TestOnConfirmedTipDelegateAccumulation(t *testing.T) {
	m := newTestMiner(0)
	m.mode = modeDelegate
	m.perC = 3
	for c := uint64(1); c <= 3; c++ {
		m.recordSubmitted(tipAt(c, 1))
	}

	_, doDelegate := m.onConfirmedTip(tipAt(2, 1))
	require.False(t, doDelegate)
	_, doDelegate = m.onConfirmedTip(tipAt(3, 1))
	require.True(t, doDelegate)
}
