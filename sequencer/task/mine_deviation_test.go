package task

import (
	"bytes"
	"testing"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// The endorsement policy refuses a milestone that took a mine transit against the
// canonical winner rule. These tests cover the two parts that decide it: when a
// consumption of a transit is deviating, and when a rival counts as known in time.
// The transit is the public take 1 fixture used by the settlement tests.

// A consumption in the transit's own slot before the settlement window is deviating
// whatever the rivals; inside the window only when a better rival was known by the
// deadline, the settlement tick minus the margin; in a later slot never.
func TestMineDeviatingInclusion(t *testing.T) {
	vid, _ := mineTransitFixture(t)
	tx := vid.GetTransaction()
	slot := tx.Slot()
	settlementTick := byte(MineSettlementTick(pbcTicks))

	var askedBefore base.LedgerTime
	rival := func(found bool) func(m mineTransit, before base.LedgerTime) bool {
		return func(m mineTransit, before base.LedgerTime) bool {
			require.NotEmpty(t, m.vrfOutput, "the transit is ranked with its VRF output")
			askedBefore = before
			return found
		}
	}

	deviates, early := deviatingInclusion(base.T(slot, settlementTick-1), tx, rival(false), pbcTicks)
	require.True(t, deviates && early, "taken before the window")

	deviates, _ = deviatingInclusion(base.T(slot, settlementTick), tx, rival(false), pbcTicks)
	require.False(t, deviates, "taken in the window, no better rival known")

	deviates, early = deviatingInclusion(base.T(slot, settlementTick), tx, rival(true), pbcTicks)
	require.True(t, deviates)
	require.False(t, early, "taken in the window while a better rival was known")
	require.Equal(t, base.T(slot, settlementTick-mineDeviationMarginTicks), askedBefore)

	deviates, _ = deviatingInclusion(base.T(slot+1, 1), tx, rival(true), pbcTicks)
	require.False(t, deviates, "consumed in a later slot: eligibility never expires")
}

// A registered transit counts as a better rival of a worse one on its predecessor
// only if it arrived before the deadline, is addressed to an active sequencer and
// carries a better solution, not just another transit of the same one.
func TestMineBetterRivalKnownBefore(t *testing.T) {
	vid, _ := mineTransitFixture(t)
	tx := vid.GetTransaction()
	parentBytes := fixtureParentBytes(t)
	pred := tx.MustInputAt(0)
	parentID := pred.TransactionID()
	slot := tx.Slot()
	all := func(base.ChainID) bool { return true }
	none := func(base.ChainID) bool { return false }

	arrival := base.T(slot, 20)
	r := NewMineTransitRegistry(func(txid base.TransactionID) []byte {
		if txid == parentID {
			return parentBytes
		}
		return nil
	})
	r.now = func() base.LedgerTime { return arrival }
	r.Add(tx.Bytes())
	registered, ok := r.Validated(tx.ID())
	require.True(t, ok)

	// a competitor on the same predecessor with the worst possible VRF output, and one
	// with the best possible: the fixture transit ranks above the first, below the second
	worse := transitCandidate(registered.pred, slot, bytes.Repeat([]byte{0xff}, len(registered.vrfOutput)), 1).mine
	better := transitCandidate(registered.pred, slot, make([]byte, len(registered.vrfOutput)), 2).mine

	require.True(t, r.BetterRivalKnownBefore(*worse, arrival.AddTicks(1), all))
	require.False(t, r.BetterRivalKnownBefore(*worse, arrival, all), "arrived at the deadline: too late")
	require.False(t, r.BetterRivalKnownBefore(*worse, arrival.AddTicks(1), none), "addressed to an inactive sequencer")
	require.False(t, r.BetterRivalKnownBefore(*better, arrival.AddTicks(1), all), "not better than the transit asked about")
	require.False(t, r.BetterRivalKnownBefore(registered, arrival.AddTicks(1), all), "a transit is not its own rival")

	// the same solution sent as another transit, to another sequencer: whatever the txids,
	// including either is including the best solution
	sameSolution := transitCandidate(registered.pred, slot, registered.vrfOutput, 3).mine
	require.False(t, r.BetterRivalKnownBefore(*sameSolution, arrival.AddTicks(1), all), "a variant of the same solution is not better")
}
