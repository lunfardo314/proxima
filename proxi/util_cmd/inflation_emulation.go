package util_cmd

import (
	"fmt"
	"image/color"
	"math"
	"os"
	"strconv"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"gonum.org/v1/plot"
	"gonum.org/v1/plot/plotter"
	"gonum.org/v1/plot/vg"
)

func initInflationEmulationCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inflation_emulation [<n slots, default 10000000>] [<step days, default 10>]",
		Args:  cobra.RangeArgs(0, 2),
		Short: "inflation emulation: upper-bound projection of chain and branch inflation over time",
		Run:   runInflationEmulationCmd,
	}
	cmd.Flags().Bool("chart", false, "generate inflation_rates.png chart")
	return cmd
}

type slotInflationData struct {
	Slot            uint32
	Step            uint32
	Year            uint32
	Month           uint32
	BranchInflation uint64
	ChainInflation  uint64
	TotalInflation  uint64
	ProformaSupply  uint64
	ChainAPR        float64
	BranchAPR       float64
	TotalAPR        float64
}

func runInflationEmulationCmd(cmd *cobra.Command, args []string) {
	ledger.InitWithTestingLedgerData(
		ledger.WithCoverageContributionBounds(0, 2*ledger.DefaultInitialSupply),
	)
	lib := ledger.L(base.MaxSlot)

	nSlots := uint32(10_000_000)
	stepDays := 10
	if len(args) > 0 {
		n, err := strconv.Atoi(args[0])
		glb.AssertNoError(err)
		glb.Assertf(n > 0, "number of slots must be positive")
		nSlots = uint32(n)
	}
	if len(args) > 1 {
		d, err := strconv.Atoi(args[1])
		glb.AssertNoError(err)
		glb.Assertf(d > 0, "step days must be positive")
		stepDays = d
	}

	slotsPerDay := lib.SlotsPerDay()
	slotsPerYear := lib.SlotsPerYear()
	step := uint32(slotsPerDay * stepDays)

	data := computeInflationData(lib, nSlots, step)

	fmt.Printf("Initial supply: %s tokens (%s PROX)\n", util.Th(lib.InitialSupply), util.Th(lib.InitialSupply/base.PROX))
	fmt.Printf("Slots per year: %s\n", util.Th(slotsPerYear))
	fmt.Printf("Slots per day: %s\n", util.Th(slotsPerDay))
	fmt.Printf("Slot duration: %v\n", lib.SlotDuration())
	fmt.Printf("MinimumInflatableAmount0: %s\n", util.Th(lib.MinimumInflatableAmount0))
	fmt.Printf("Step: %s slots (%d days)\n", util.Th(step), stepDays)
	fmt.Println()
	fmt.Printf("%-5s  %-6s  %-10s  %16s  %16s  %16s  %20s  %10s  %10s  %10s\n",
		"Year", "Month", "Slot", "BranchInflation", "ChainInflation", "TotalInflation", "ProformaSupply",
		"ChainAPR", "BranchAPR", "TotalAPR")
	fmt.Printf("%s\n", "--------------------------------------------------------------------------------------------------------------------------------------")

	for _, d := range data {
		fmt.Printf("%-5d  %-6d  %-10s  %16s  %16s  %16s  %20s  %.2f%%  %.2f%%  %.2f%%\n",
			d.Year,
			d.Month,
			util.Th(d.Slot),
			util.Th(d.BranchInflation),
			util.Th(d.ChainInflation),
			util.Th(d.TotalInflation),
			util.Th(d.ProformaSupply),
			d.ChainAPR, d.BranchAPR, d.TotalAPR)
	}

	last := data[len(data)-1]
	finalSupply := last.ProformaSupply + last.TotalInflation

	fmt.Println()
	fmt.Printf("After %d slots:\n", nSlots)
	fmt.Printf("  Proforma supply:   %s tokens (%s PRXI)\n", util.Th(finalSupply), util.Th(finalSupply/base.PROX))
	fmt.Printf("  Total inflated:    %s tokens (%s PRXI)\n", util.Th(finalSupply-lib.InitialSupply), util.Th((finalSupply-lib.InitialSupply)/base.PROX))
	fmt.Printf("  Increase:          %.2f%%\n", float64(finalSupply-lib.InitialSupply)/float64(lib.InitialSupply)*100.0)

	elapsed := lib.SlotDuration() * time.Duration(nSlots)
	fmt.Printf("  Elapsed time:      %v (%.2f days)\n", elapsed, elapsed.Hours()/24)

	// per-year actual inflation summary
	slotsPerYearU := uint32(slotsPerYear)
	fmt.Println()
	fmt.Println("Actual inflation per year:")
	fmt.Printf("  %-6s  %20s  %20s  %20s  %10s\n", "Year", "SupplyStart", "SupplyEnd", "Inflated", "Rate")
	fmt.Printf("  %s\n", "------------------------------------------------------------------------------------")

	findBySlot := func(targetSlot uint32) *slotInflationData {
		best := &data[0]
		for i := range data {
			if data[i].Slot <= targetSlot {
				best = &data[i]
			} else {
				break
			}
		}
		return best
	}

	for year := 0; ; year++ {
		yearStartSlot := uint32(year) * slotsPerYearU
		yearEndSlot := yearStartSlot + slotsPerYearU - 1
		if yearStartSlot >= nSlots {
			break
		}
		if yearEndSlot >= nSlots {
			yearEndSlot = nSlots - 1
		}
		dStart := findBySlot(yearStartSlot)
		dEnd := findBySlot(yearEndSlot)

		supplyStart := dStart.ProformaSupply
		supplyEnd := dEnd.ProformaSupply + dEnd.TotalInflation
		inflated := supplyEnd - supplyStart
		rate := float64(inflated) / float64(supplyStart) * 100.0
		full := ""
		if yearEndSlot < yearStartSlot+slotsPerYearU-1 {
			full = " (partial)"
		}
		fmt.Printf("  %-6d  %20s  %20s  %20s  %9.2f%%%s\n",
			year,
			util.Th(supplyStart),
			util.Th(supplyEnd),
			util.Th(inflated),
			rate, full)
	}

	generateChart, _ := cmd.Flags().GetBool("chart")
	if generateChart {
		chartFile := "inflation_rates.png"
		if err := generateInflationChart(data, slotsPerDay, chartFile); err != nil {
			glb.Infof("chart generation failed: %v", err)
		} else {
			fmt.Printf("\nChart saved to %s\n", chartFile)
		}
	}
}

// computeInflationData computes inflation data for the first nSlots slots, advancing in increments of 'step' slots.
// Assumes the entire proforma supply is inflated each slot (upper bound). Coverage bounds are ignored.
//
// Chain inflation is exact for any step size:
//
//	chainInflationMultiStep(A, s, N) = N * A / (M0 + s)
//
// Branch inflation uses the closed-form with chain compounding:
//
//	branchInflationTotal = B * (M0+s+S) * ln((M0+s+S)/(M0+s))
//
// Steps are split into sub-segments at branch bonus boundaries for correctness.
func computeInflationData(lib *ledger.Library, nSlots, step uint32) []slotInflationData {
	if step == 0 {
		step = 1
	}
	slotsPerYear := lib.SlotsPerYear()
	slotsPerDay := lib.SlotsPerDay()
	supply := lib.InitialSupply

	nPoints := (nSlots + step - 1) / step
	ret := make([]slotInflationData, 0, nPoints)

	fmt.Printf("  computing inflation data for %d slots (step=%d, %d data points)...\n", nSlots, step, nPoints)
	start := time.Now()

	for slot := uint32(0); slot < nSlots; slot += step {
		curStep := step
		if slot+curStep > nSlots {
			curStep = nSlots - slot
		}

		chainInfl, branchInfl, newSupply := computeStepInfl(lib, supply, slot, curStep)
		totalInflation := chainInfl + branchInfl
		month := slot / uint32(slotsPerDay*30)

		ret = append(ret, slotInflationData{
			Slot:            slot,
			Step:            curStep,
			Year:            month / 12,
			Month:           month,
			BranchInflation: branchInfl,
			ChainInflation:  chainInfl,
			TotalInflation:  totalInflation,
			ProformaSupply:  supply,
			ChainAPR:        float64(chainInfl) / float64(supply) / float64(curStep) * float64(slotsPerYear) * 100.0,
			BranchAPR:       float64(branchInfl) / float64(supply) / float64(curStep) * float64(slotsPerYear) * 100.0,
			TotalAPR:        float64(totalInflation) / float64(supply) / float64(curStep) * float64(slotsPerYear) * 100.0,
		})

		supply = newSupply
	}

	elapsed := time.Since(start)
	fmt.Printf("  done: %d slots in %v (%d data points)\n", nSlots, elapsed.Round(time.Millisecond), len(ret))

	return ret
}

// computeStepInfl computes the exact inflation for a step, splitting at branch bonus boundaries.
func computeStepInfl(lib *ledger.Library, supply uint64, startSlot, step uint32) (chainInfl, branchInfl uint64, newSupply uint64) {
	m0 := float64(lib.MinimumInflatableAmount0)
	curSupply := supply
	endSlot := startSlot + step

	for segStart := startSlot; segStart < endSlot; {
		bonus := lib.BranchInflationBonusBase(segStart)
		segEnd := findNextBonusChangeSlot(lib, bonus, segStart+1, endSlot)
		segLen := segEnd - segStart

		ci := lib.ChainInflationMultiStep(curSupply, segStart, segLen)
		chainInfl += ci

		var bi uint64
		if segLen == 1 {
			bi = bonus
		} else {
			ms := m0 + float64(segStart)
			msN := ms + float64(segLen)
			bi = uint64(float64(bonus) * msN * math.Log(msN/ms))
		}
		branchInfl += bi

		curSupply += ci + bi
		segStart = segEnd
	}

	return chainInfl, branchInfl, curSupply
}

// findNextBonusChangeSlot finds the first slot in [lo, hi) where BranchInflationBonusBase differs from currentBonus.
// Returns hi if the bonus is constant throughout the range. Uses binary search.
func findNextBonusChangeSlot(lib *ledger.Library, currentBonus uint64, lo, hi uint32) uint32 {
	if lo >= hi {
		return hi
	}
	if lib.BranchInflationBonusBase(hi-1) == currentBonus {
		return hi
	}
	for lo < hi {
		mid := lo + (hi-lo)/2
		if lib.BranchInflationBonusBase(mid) == currentBonus {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

// generateInflationChart creates a PNG chart with three lines: total, chain, and branch inflation rates.
// X axis is time in 30-day months, Y axis is annualized inflation rate in percent.
func generateInflationChart(data []slotInflationData, slotsPerDay int, filename string) error {
	slotsPerMonth := float64(slotsPerDay * 30)

	totalPts := make(plotter.XYs, len(data))
	chainPts := make(plotter.XYs, len(data))
	branchPts := make(plotter.XYs, len(data))

	for i, d := range data {
		x := float64(d.Slot) / slotsPerMonth
		totalPts[i] = plotter.XY{X: x, Y: d.TotalAPR}
		chainPts[i] = plotter.XY{X: x, Y: d.ChainAPR}
		branchPts[i] = plotter.XY{X: x, Y: d.BranchAPR}
	}

	p := plot.New()
	p.Title.Text = "Proxima Inflation Rate (upper bound)"
	p.X.Label.Text = "Months since genesis"
	p.Y.Label.Text = "Annualized rate, %"
	p.Y.Min = 0
	p.Y.Tick.Marker = plot.ConstantTicks(makePercentTicks(data))

	totalLine, err := plotter.NewLine(totalPts)
	if err != nil {
		return err
	}
	totalLine.Color = color.RGBA{R: 220, G: 50, B: 50, A: 255}
	totalLine.Width = vg.Points(2)

	chainLine, err := plotter.NewLine(chainPts)
	if err != nil {
		return err
	}
	chainLine.Color = color.RGBA{R: 50, G: 100, B: 220, A: 255}
	chainLine.Width = vg.Points(2)

	branchLine, err := plotter.NewLine(branchPts)
	if err != nil {
		return err
	}
	branchLine.Color = color.RGBA{R: 50, G: 180, B: 80, A: 255}
	branchLine.Width = vg.Points(2)

	p.Add(totalLine, chainLine, branchLine)
	p.Legend.Add("Total", totalLine)
	p.Legend.Add("Chain", chainLine)
	p.Legend.Add("Branch", branchLine)
	p.Legend.Top = true

	wt, err := p.WriterTo(24*vg.Centimeter, 14*vg.Centimeter, "png")
	if err != nil {
		return err
	}
	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = wt.WriteTo(f)
	return err
}

// makePercentTicks generates Y-axis ticks at 1% intervals up to the max data value.
func makePercentTicks(data []slotInflationData) []plot.Tick {
	maxY := 0.0
	for _, d := range data {
		if d.TotalAPR > maxY {
			maxY = d.TotalAPR
		}
	}
	top := int(math.Ceil(maxY))
	ticks := make([]plot.Tick, 0, top+1)
	for i := 0; i <= top; i++ {
		ticks = append(ticks, plot.Tick{Value: float64(i), Label: fmt.Sprintf("%d", i)})
	}
	return ticks
}
