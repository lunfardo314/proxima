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

	outs := settleMineTransits([]*_inputCandidate{loser, plain, winner, otherChain}, settled(10), pbcTicks)
	require.ElementsMatch(t, []*_inputCandidate{plain, winner, otherChain}, outs)
}

// a transit is held until the settlement window of its slot, so competitors
// can arrive before the choice is made; from then on it stays eligible in every
// later slot, so a slot without a milestone in the window delays it and never
// drops it
func TestSettleMineTransitsHoldsUntilTheWindow(t *testing.T) {
	c := transitCandidate(predecessor(1), 10, []byte{0x10}, 1)

	early := base.T(10, base.MaxTickValue-pbcTicks-mineSettlementWindowTicks-1)
	require.Empty(t, settleMineTransits([]*_inputCandidate{c}, early, pbcTicks), "not eligible before the window")
	require.Len(t, settleMineTransits([]*_inputCandidate{c}, settled(10), pbcTicks), 1, "eligible at the window")
	require.Len(t, settleMineTransits([]*_inputCandidate{c}, base.T(11, 3), pbcTicks), 1, "still eligible in a later slot")
	require.Empty(t, settleMineTransits([]*_inputCandidate{c}, base.T(9, base.MaxTickValue), pbcTicks), "never before its slot")
}
