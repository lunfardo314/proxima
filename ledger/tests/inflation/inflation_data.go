package inflation

import (
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
)

// SlotInflationData holds computed inflation data for a single slot
type SlotInflationData struct {
	Slot            uint32
	Month           uint32  // month number since genesis
	BranchBonusBase uint64  // maximum branch inflation bonus for this slot
	ChainInflation  uint64  // chain inflation upper bound (entire supply inflated)
	TotalInflation  uint64  // chain inflation + branch bonus base
	ProformaSupply  uint64  // cumulative proforma supply before this slot's inflation
	ChainAPR        float64 // extrapolated annual chain inflation rate, percent
	BranchAPR       float64 // extrapolated annual branch inflation rate, percent
	TotalAPR        float64 // extrapolated annual total inflation rate, percent
}

// ComputeInflationData computes per-slot inflation data for the first nSlots slots.
// It assumes the entire proforma supply is inflated each slot (upper bound).
// Coverage bounds are ignored.
// progressPercent controls how often progress is printed (e.g. 10 = every 10%). 0 disables progress.
func ComputeInflationData(nSlots uint32, progressPercent int) []SlotInflationData {
	lib := ledger.L(base.MaxSlot)

	slotsPerYear := lib.SlotsPerYear()
	slotsPerDay := lib.SlotsPerDay()
	supply := lib.InitialSupply

	ret := make([]SlotInflationData, nSlots)

	progressStep := uint32(0)
	if progressPercent > 0 {
		progressStep = nSlots / uint32(100/progressPercent)
	}

	fmt.Printf("  computing inflation data for %d slots...\n", nSlots)
	start := time.Now()

	for slot := uint32(0); slot < nSlots; slot++ {
		branchBonusBase := lib.BranchInflationBonusBase(slot)
		chainInflation := lib.ChainInflationOneSlot(supply, slot)
		totalInflation := chainInflation + branchBonusBase

		ret[slot] = SlotInflationData{
			Slot:            slot,
			Month:           slot / uint32(slotsPerDay*30),
			BranchBonusBase: branchBonusBase,
			ChainInflation:  chainInflation,
			TotalInflation:  totalInflation,
			ProformaSupply:  supply,
			ChainAPR:        float64(chainInflation) / float64(supply) * float64(slotsPerYear) * 100.0,
			BranchAPR:       float64(branchBonusBase) / float64(supply) * float64(slotsPerYear) * 100.0,
			TotalAPR:        float64(totalInflation) / float64(supply) * float64(slotsPerYear) * 100.0,
		}

		supply += totalInflation

		if progressStep > 0 && slot > 0 && slot%progressStep == 0 {
			elapsed := time.Since(start)
			usPerSlot := float64(elapsed.Microseconds()) / float64(slot)
			pct := float64(slot) / float64(nSlots) * 100
			fmt.Printf("  computing... %5.1f%% (%d/%d slots, %.1f us/slot)\n", pct, slot, nSlots, usPerSlot)
		}
	}

	elapsed := time.Since(start)
	usPerSlot := float64(elapsed.Microseconds()) / float64(nSlots)
	fmt.Printf("  done: %d slots in %v (%.1f us/slot)\n", nSlots, elapsed.Round(time.Millisecond), usPerSlot)

	return ret
}
