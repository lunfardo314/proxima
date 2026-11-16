package ledger

import (
	"encoding/binary"
	"math/bits"

	"github.com/lunfardo314/proxima/util/vrf"
)

// The tie-break between branches of equal coverage from library upgrade 7: a branch b ranks by
// T = sigma x u - B, u its VRF output read as a fraction, B the bonus it chose, so a smaller bonus
// has a better chance and the bonus settles where competition puts it.

// TieBreakOffset keeps the score term non-negative: twice the largest bonus, since a consumed
// baseline sequencer output has its bonus taken out twice (once from the counted amount, once
// as the bid).
const TieBreakOffset = 2 * 5_000_000

// BranchTieBreakDraw is U, the first 8 bytes big-endian of the VRF output of the branch whose
// stem output is given; u = U / 2^64. 0 for a stem without a proof (genesis).
func BranchTieBreakDraw(stem *Output) uint64 {
	stemLock, ok := stem.StemLock()
	if !ok || len(stemLock.VRFProof) == 0 {
		return 0
	}
	beta, err := vrf.ProofToHash(stemLock.VRFProof)
	if err != nil || len(beta) < 8 {
		return 0
	}
	return binary.BigEndian.Uint64(beta[:8])
}

// TieBreakKey is sigma x u - B shifted by the largest bonus to stay non-negative: the order of
// branches of equal coverage, the larger first.
func TieBreakKey(sigma, draw, bonus uint64) uint64 {
	hi, _ := bits.Mul64(sigma, draw)
	return hi + TieBreakOffset/2 - min(bonus, TieBreakOffset/2)
}

// TieBreakScoreTerm is what the baseline branch adds to the score of a milestone built on it:
// sigma x u - B plus TieBreakOffset, less B once more when the baseline's sequencer output is
// consumed in the milestone's past cone, whose counted amount then holds the bonus. Consuming the
// output therefore never changes the score.
func TieBreakScoreTerm(sigma, draw, bonus uint64, consumed bool) uint64 {
	hi, _ := bits.Mul64(sigma, draw)
	bonus = min(bonus, TieBreakOffset/2)
	ret := hi + TieBreakOffset - bonus
	if consumed {
		ret -= bonus
	}
	return ret
}
