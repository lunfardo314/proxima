package task

import (
	"github.com/lunfardo314/proxima/sequencer/mine"
	"testing"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// The canonical winner rule (package mine) as the sequencer applies it to the
// tag-along candidates: which of several transits on one predecessor is
// inserted, and from when. The descriptors are built directly, since the rule reads nothing else
// off the transaction.

const pbcTicks = 25

// transitCandidate is a tag-along candidate produced by a mine transit with the
// given predecessor, slot and VRF output; `who` makes the txid distinct.
func transitCandidate(pred base.OutputID, slot uint32, vrf []byte, who byte) *_inputCandidate {
	var txid base.TransactionID
	copy(txid[:base.LedgerTimeByteLength], base.T(slot, 1).Bytes())
	txid[len(txid)-1] = who
	return &_inputCandidate{mine: &mine.Transit{Pred: pred, Slot: slot, VRFOutput: vrf, TxID: txid}}
}

func predecessor(b byte) base.OutputID {
	var txid base.TransactionID
	txid[0] = b
	return base.MustNewOutputID(txid, 0)
}

// noRivals stands for a node holding no competitors outside the backlog
func noRivals(*_inputCandidate) []mine.Transit { return nil }

// settled is the target timestamp in the settlement window of the given slot
func settled(slot uint32) base.LedgerTime {
	return base.T(slot, byte(mine.SettlementTick(pbcTicks)))
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

	early := base.T(10, byte(mine.SettlementTick(pbcTicks)-1))
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
	rivals := func(c *_inputCandidate) []mine.Transit {
		if c == own {
			return []mine.Transit{worse, better}
		}
		return nil
	}
	outs := settleMineTransits([]*_inputCandidate{own, otherPred}, settled(10), pbcTicks, rivals)
	require.ElementsMatch(t, []*_inputCandidate{otherPred}, outs, "a better rival takes its predecessor out")

	rivals = func(c *_inputCandidate) []mine.Transit {
		if c == own {
			return []mine.Transit{worse}
		}
		return nil
	}
	outs = settleMineTransits([]*_inputCandidate{own, otherPred}, settled(10), pbcTicks, rivals)
	require.ElementsMatch(t, []*_inputCandidate{own, otherPred}, outs, "worse rivals do not stop it")

	// the older slot still decides first: a rival at a newer slot with a smaller
	// VRF output does not outrank the candidate
	newer := *transitCandidate(pred, 11, []byte{0x00}, 5).mine
	rivals = func(c *_inputCandidate) []mine.Transit { return []mine.Transit{newer} }
	outs = settleMineTransits([]*_inputCandidate{own}, settled(11), pbcTicks, rivals)
	require.Len(t, outs, 1)
}
