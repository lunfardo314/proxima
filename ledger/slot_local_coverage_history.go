package ledger

import (
	"fmt"
	"math/bits"
)

// SlotLocalHistoricalCarry returns the initial baseline contribution missing
// from the native historical term for an activity slot after that baseline.
// gap is activitySlot - baselineSlot, not branchSlot - baselineSlot: a branch
// closes its preceding slot. The caller must reject negative slot differences.
//
// baselineTotal is the stored branch total, which does not yet include the
// branch's initial fresh contribution. initialFresh must be that complete
// contribution at the baseline slot: intrinsic sequencer-output inflation plus
// the native weight of any other branch-created outputs counted by this rule.
// It is derived from the immutable baseline, never the currently unspent subset.
//
// At gap zero, initialFresh already belongs to the fresh delta, so no separate
// carry is added. Later, preserve halving of the combined initial score before
// subtracting the historical term already supplied by FinalLedgerCoverage.
// Shifting initialFresh alone can lose one unit through separate rounding.
// This carries known baseline activity only; it does not reconstruct activity
// from missing intermediate slots under the latest-slot-only closure rule.
func SlotLocalHistoricalCarry(baselineTotal, initialFresh uint64, gap uint32) (uint64, error) {
	if gap == 0 {
		return 0, nil
	}
	initialScore, overflow := bits.Add64(baselineTotal>>1, initialFresh, 0)
	if overflow != 0 {
		return 0, fmt.Errorf("slot-local initial coverage overflow")
	}
	if gap >= 64 {
		return 0, nil
	}
	return (initialScore >> gap) - (baselineTotal >> (uint64(gap) + 1)), nil
}
