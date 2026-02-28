package inflation

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

const numSlots = 4_000_000

// TestInflationEmulation calculates and prints inflation data for the first numSlots slots.
// For each slot it computes:
//   - branch inflation bonus base (upper bound of VRF-based bonus)
//   - upper bound of chain inflation (assuming the entire proforma supply is inflated each slot)
//   - total inflation = chain inflation + branch inflation bonus base
//   - extrapolated annual inflation rates for each component and total
//
// Coverage bounds are intentionally ignored — this is a pure upper-bound projection.
// Output is displayed every 10 days with the current month number.
func TestInflationEmulation(t *testing.T) {
	lib := ledger.L(base.MaxSlot)
	slotsPerDay := lib.SlotsPerDay()
	slotsPer10Days := uint32(slotsPerDay * 10)

	data := ComputeInflationData(numSlots, 10)

	t.Logf("Initial supply: %s tokens (%s PRXI)", util.Th(lib.InitialSupply), util.Th(lib.InitialSupply/ledger.PRXI))
	t.Logf("Slots per year: %s", util.Th(lib.SlotsPerYear()))
	t.Logf("Slots per day: %s", util.Th(slotsPerDay))
	t.Logf("Slot duration: %v", lib.SlotDuration())
	t.Logf("MinimumInflatableAmount0: %s", util.Th(lib.MinimumInflatableAmount0))
	t.Logf("")
	t.Logf("%-6s  %-10s  %16s  %16s  %16s  %20s  %10s  %10s  %10s",
		"Month", "Slot", "BranchBonusBase", "ChainInflation", "TotalInflation", "ProformaSupply",
		"ChainAPR", "BranchAPR", "TotalAPR")
	t.Logf("%s", "-------------------------------------------------------------------------------------------------------------------------------")

	for _, d := range data {
		if d.Slot == 0 || d.Slot%slotsPer10Days == 0 || d.Slot == numSlots-1 {
			t.Logf("%-6d  %-10s  %16s  %16s  %16s  %20s  %.2f%%  %.2f%%  %.2f%%",
				d.Month,
				util.Th(d.Slot),
				util.Th(d.BranchBonusBase),
				util.Th(d.ChainInflation),
				util.Th(d.TotalInflation),
				util.Th(d.ProformaSupply),
				d.ChainAPR, d.BranchAPR, d.TotalAPR)
		}
	}

	last := data[numSlots-1]
	finalSupply := last.ProformaSupply + last.TotalInflation

	t.Logf("")
	t.Logf("After %d slots:", numSlots)
	t.Logf("  Proforma supply:   %s tokens (%s PRXI)", util.Th(finalSupply), util.Th(finalSupply/ledger.PRXI))
	t.Logf("  Total inflated:    %s tokens (%s PRXI)", util.Th(finalSupply-lib.InitialSupply), util.Th((finalSupply-lib.InitialSupply)/ledger.PRXI))
	t.Logf("  Increase:          %.2f%%", float64(finalSupply-lib.InitialSupply)/float64(lib.InitialSupply)*100.0)

	elapsed := lib.SlotDuration() * numSlots
	t.Logf("  Elapsed time:      %v (%.2f days)", elapsed, elapsed.Hours()/24)

	// per-year actual inflation summary
	slotsPerYear := uint32(lib.SlotsPerYear())
	t.Logf("")
	t.Logf("Actual inflation per year:")
	t.Logf("  %-6s  %20s  %20s  %20s  %10s", "Year", "SupplyStart", "SupplyEnd", "Inflated", "Rate")
	t.Logf("  %s", "------------------------------------------------------------------------------------")
	for year := 0; ; year++ {
		yearStart := uint32(year) * slotsPerYear
		yearEnd := yearStart + slotsPerYear - 1
		if yearStart >= numSlots {
			break
		}
		if yearEnd >= numSlots {
			yearEnd = numSlots - 1
		}
		supplyStart := data[yearStart].ProformaSupply
		supplyEnd := data[yearEnd].ProformaSupply + data[yearEnd].TotalInflation
		inflated := supplyEnd - supplyStart
		rate := float64(inflated) / float64(supplyStart) * 100.0
		full := ""
		if yearEnd < yearStart+slotsPerYear-1 {
			full = " (partial)"
		}
		t.Logf("  %-6d  %20s  %20s  %20s  %9.2f%%%s",
			year,
			util.Th(supplyStart),
			util.Th(supplyEnd),
			util.Th(inflated),
			rate, full)
	}
}
