package task

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// The canonical winner rule as the sequencer applies it to competing mine
// transits: which of several transits on one predecessor is inserted, and from
// when. The descriptors are built directly, since the rule reads nothing else
// off the transaction.

const pbcTicks = 25

// transitCandidate is a tag-along candidate produced by a mine transit with the
// given predecessor, slot and VRF output; `who` makes the txid distinct.
func transitCandidate(pred base.OutputID, slot uint32, vrf []byte, who byte) *_inputCandidate {
	var txid base.TransactionID
	copy(txid[:base.LedgerTimeByteLength], base.T(slot, 1).Bytes())
	txid[len(txid)-1] = who
	return &_inputCandidate{mine: &mineTransit{pred: pred, slot: slot, vrfOutput: vrf, txid: txid}}
}

func predecessor(b byte) base.OutputID {
	var txid base.TransactionID
	txid[0] = b
	return base.MustNewOutputID(txid, 0)
}

// noRivals stands for a node holding no competitors outside the backlog
func noRivals(*_inputCandidate) []mineTransit { return nil }

// settled is the target timestamp in the settlement window of the given slot
func settled(slot uint32) base.LedgerTime {
	return base.T(slot, base.MaxTickValue-pbcTicks-mineSettlementWindowTicks)
}

// slot first, then the smaller VRF output, then the txid; each key only decides
// when the previous ones are equal
func TestMineTransitOrder(t *testing.T) {
	pred := predecessor(1)
	older := transitCandidate(pred, 10, []byte{0xff}, 1).mine
	newer := transitCandidate(pred, 11, []byte{0x00}, 2).mine
	require.True(t, older.betterThan(newer), "the older slot wins whatever the VRF output")
	require.False(t, newer.betterThan(older))

	small := transitCandidate(pred, 10, []byte{0x01, 0x00}, 9).mine
	big := transitCandidate(pred, 10, []byte{0x01, 0x01}, 2).mine
	require.True(t, small.betterThan(big), "equal slots break on the smaller VRF output, not the txid")
	require.False(t, big.betterThan(small))

	a := transitCandidate(pred, 10, []byte{0x01}, 2).mine
	b := transitCandidate(pred, 10, []byte{0x01}, 9).mine
	require.True(t, a.betterThan(b), "equal VRF outputs break on the lower txid")
	require.False(t, b.betterThan(a))
}

// of the transits on one predecessor only the winner is left, whatever the
// arrival order; transits on different predecessors do not compete; ordinary
// tag-alongs pass through untouched
func TestSettleMineTransitsKeepsOnlyTheWinner(t *testing.T) {
	pred := predecessor(1)
	loser := transitCandidate(pred, 10, []byte{0x80}, 1)
	winner := transitCandidate(pred, 10, []byte{0x10}, 2)
	otherChain := transitCandidate(predecessor(2), 10, []byte{0xf0}, 3)
	plain := &_inputCandidate{}

	outs := settleMineTransits([]*_inputCandidate{loser, plain, winner, otherChain}, settled(10), pbcTicks, noRivals)
	require.ElementsMatch(t, []*_inputCandidate{plain, winner, otherChain}, outs)
}

// a transit is held until the settlement window of its slot, so competitors
// can arrive before the choice is made; from then on it stays eligible in every
// later slot, so a slot without a milestone in the window delays it and never
// drops it
func TestSettleMineTransitsHoldsUntilTheWindow(t *testing.T) {
	c := transitCandidate(predecessor(1), 10, []byte{0x10}, 1)

	early := base.T(10, base.MaxTickValue-pbcTicks-mineSettlementWindowTicks-1)
	require.Empty(t, settleMineTransits([]*_inputCandidate{c}, early, pbcTicks, noRivals), "not eligible before the window")
	require.Len(t, settleMineTransits([]*_inputCandidate{c}, settled(10), pbcTicks, noRivals), 1, "eligible at the window")
	require.Len(t, settleMineTransits([]*_inputCandidate{c}, base.T(11, 3), pbcTicks, noRivals), 1, "still eligible in a later slot")
	require.Empty(t, settleMineTransits([]*_inputCandidate{c}, base.T(9, base.MaxTickValue), pbcTicks, noRivals), "never before its slot")
}

// A competitor addressed to another sequencer is not in this backlog, it is a
// rival found in the memDAG. The sequencer consumes its own best candidate only
// when no rival ranks above it, so between them the sequencers include one
// transit per predecessor, the one the rule names. Rivals on another
// predecessor do not count, and worse rivals do not stop it.
func TestSettleMineTransitsDefersToABetterRival(t *testing.T) {
	pred := predecessor(1)
	own := transitCandidate(pred, 10, []byte{0x40}, 1)
	otherPred := transitCandidate(predecessor(2), 10, []byte{0x40}, 2)

	better := *transitCandidate(pred, 10, []byte{0x10}, 3).mine
	worse := *transitCandidate(pred, 10, []byte{0x80}, 4).mine
	rivals := func(c *_inputCandidate) []mineTransit {
		if c == own {
			return []mineTransit{worse, better}
		}
		return nil
	}
	outs := settleMineTransits([]*_inputCandidate{own, otherPred}, settled(10), pbcTicks, rivals)
	require.ElementsMatch(t, []*_inputCandidate{otherPred}, outs, "a better rival takes its predecessor out")

	rivals = func(c *_inputCandidate) []mineTransit {
		if c == own {
			return []mineTransit{worse}
		}
		return nil
	}
	outs = settleMineTransits([]*_inputCandidate{own, otherPred}, settled(10), pbcTicks, rivals)
	require.ElementsMatch(t, []*_inputCandidate{own, otherPred}, outs, "worse rivals do not stop it")

	// the older slot still decides first: a rival at a newer slot with a smaller
	// VRF output does not outrank the candidate
	newer := *transitCandidate(pred, 11, []byte{0x00}, 5).mine
	rivals = func(c *_inputCandidate) []mineTransit { return []mineTransit{newer} }
	outs = settleMineTransits([]*_inputCandidate{own}, settled(11), pbcTicks, rivals)
	require.Len(t, outs, 1)
}
