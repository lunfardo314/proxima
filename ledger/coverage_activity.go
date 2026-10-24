package ledger

import (
	"fmt"
	"github.com/lunfardo314/proxima/ledger/base"
	"math/bits"
)

// CoverageActivityTime: ordinary candidates count their own slot; a branch
// closes the immediately completed slot. Dispatch still uses target's library.
func (lib *Library) CoverageActivityTime(target base.LedgerTime) base.LedgerTime {
	if lib.SlotLocalConsumptionCoverage && target.IsSlotBoundary() && target.Slot > 0 {
		return base.T(target.Slot-1, base.MaxTickValue)
	}
	return target
}

// SlotLocalBaselineBonus preserves the baseline's own bonus, which is absent
// from its stored total. Same-slot raw consumption and the adjustment must not
// double count it. Older bonus is aged with history, independently of importing
// its old consumer; combine BEFORE halving to preserve integer rounding.
func SlotLocalBaselineBonus(total, bonus uint64, baselineSlot, activitySlot uint32, consumedInActivity bool) (uint64, error) {
	if baselineSlot > activitySlot {
		return 0, nil
	}
	if baselineSlot == activitySlot {
		if consumedInActivity {
			return 0, nil
		}
		return bonus, nil
	}
	complete, overflow := bits.Add64(total>>1, bonus, 0)
	if overflow != 0 {
		return 0, fmt.Errorf("coverage bonus carry overflow")
	}
	gap := uint64(activitySlot - baselineSlot)
	return (complete >> gap) - (total >> (gap + 1)), nil
}
