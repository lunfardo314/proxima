package util_cmd

import (
	"fmt"
	"image/color"
	"math"
	"math/rand"
	"os"
	"path/filepath"
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
		Use:   "inflation_emulation [<years, default 10>]",
		Args:  cobra.RangeArgs(0, 1),
		Short: "per-slot emulation of supply growth: bootstrap capital, branch bonus and mined emission",
		Run:   runInflationEmulationCmd,
	}
	cmd.Flags().String("dir", ".internal", "directory the charts are written to")
	cmd.Flags().Float64("pace", 4.7, "mean mining pace, slots per transit")
	cmd.Flags().Int64("seed", 1, "seed of the random draws, so a run reproduces")
	cmd.Flags().Bool("no-charts", false, "print the summary without writing charts")
	return cmd
}

// sample is the state of the emulation at one slot. Supply is split into the
// three pools the supply chart stacks, each carrying the chain inflation its
// own balance earned — attributing inflation per pool rather than to the supply
// as a whole is what makes the bands add up to the supply exactly.
type sample struct {
	slot      uint32
	bootstrap uint64 // genesis supply and the inflation on it
	branch    uint64 // branch bonuses and the inflation on them
	mined     uint64 // mined emission and the inflation on it
}

func (s *sample) supply() uint64 { return s.bootstrap + s.branch + s.mined }

func runInflationEmulationCmd(cmd *cobra.Command, args []string) {
	ledger.InitWithTestingLedgerData(
		ledger.WithCoverageContributionBounds(0, 2*ledger.DefaultInitialSupply),
	)
	lib := ledger.L(base.MaxSlot)

	years := 10
	if len(args) > 0 {
		n, err := strconv.Atoi(args[0])
		glb.AssertNoError(err)
		glb.Assertf(n > 0, "number of years must be positive")
		years = n
	}
	dir, _ := cmd.Flags().GetString("dir")
	meanPace, _ := cmd.Flags().GetFloat64("pace")
	seed, _ := cmd.Flags().GetInt64("seed")
	noCharts, _ := cmd.Flags().GetBool("no-charts")

	slotsPerYear := uint32(lib.SlotsPerYear())
	minPace := uint32(lib.MineMinPace)
	glb.Assertf(meanPace >= float64(minPace), "mean pace must be at least the ledger minimum of %d slots", minPace)

	m0 := lib.MinimumInflatableAmount0
	checkChainInflationFormula(lib, m0)
	rInit := genesisMintable(lib)

	fmt.Printf("Per-slot emulation over %d years (%s slots)\n", years, util.Th(uint64(years)*uint64(slotsPerYear)))
	fmt.Printf("  initial supply:            %s PROX\n", util.Th(lib.InitialSupply/base.PROX))
	fmt.Printf("  mintable budget R_init:    %s PROX\n", util.Th(rInit/base.PROX))
	fmt.Printf("  minted per transit A:      %s PROX\n", util.Th(lib.MineAmount/base.PROX))
	fmt.Printf("  minimum inflatable amount: %s\n", util.Th(m0))
	fmt.Printf("  slots per year:            %s (slot %v)\n", util.Th(slotsPerYear), lib.SlotDuration())
	fmt.Println("Assumptions:")
	fmt.Printf("  chain inflation on the whole supply every slot (upper bound: in reality only chained outputs earn it)\n")
	fmt.Printf("  branch bonus drawn uniformly in [1, base] each slot, as the VRF does — not the base itself\n")
	fmt.Printf("  mining pace shifted-exponential, mean %.2f slots, floor %d, seed %d\n", meanPace, minPace, seed)
	fmt.Println()

	monthly, yearly, transits, minedOut := emulate(lib, years, m0, rInit, meanPace, minPace, seed)

	printYearTable(yearly, slotsPerYear)

	last := yearly[len(yearly)-1]
	fmt.Println()
	fmt.Printf("After %d years:\n", years)
	fmt.Printf("  supply:            %s PROX (from %s PROX, x%.2f)\n",
		util.Th(last.supply()/base.PROX), util.Th(lib.InitialSupply/base.PROX),
		float64(last.supply())/float64(lib.InitialSupply))
	fmt.Printf("  bootstrap capital: %s PROX (%.2f%% of supply)\n",
		util.Th(last.bootstrap/base.PROX), 100*float64(last.bootstrap)/float64(last.supply()))
	fmt.Printf("  branch bonus:      %s PROX (%.2f%% of supply)\n",
		util.Th(last.branch/base.PROX), 100*float64(last.branch)/float64(last.supply()))
	fmt.Printf("  mined:             %s PROX (%.2f%% of supply)\n",
		util.Th(last.mined/base.PROX), 100*float64(last.mined)/float64(last.supply()))
	fmt.Printf("  transits mined:    %s of %s, emission %s PROX\n",
		util.Th(transits), util.Th(rInit/lib.MineAmount), util.Th(minedOut/base.PROX))

	if noCharts {
		return
	}
	glb.AssertNoError(os.MkdirAll(dir, 0755))
	// the horizon is in the file name, so runs of different lengths sit side by
	// side instead of overwriting each other
	supplyFile := filepath.Join(dir, fmt.Sprintf("supply_%dy.png", years))
	sharesFile := filepath.Join(dir, fmt.Sprintf("supply_shares_%dy.png", years))
	rateFile := filepath.Join(dir, fmt.Sprintf("inflation_rate_%dy.png", years))
	glb.AssertNoError(writeSupplyChart(monthly, supplyFile))
	glb.AssertNoError(writeSharesChart(monthly, sharesFile))
	glb.AssertNoError(writeRateChart(yearly, rateFile))
	fmt.Printf("\nCharts written to %s, %s and %s\n", supplyFile, sharesFile, rateFile)
}

// emulate runs the ledger slot by slot. It returns a sample per month and per
// year (both ending on the final slot), the number of transits mined and the
// emission they minted.
func emulate(lib *ledger.Library, years int, m0, rInit uint64, meanPace float64, minPace uint32, seed int64) (monthly, yearly []sample, transits, minedOut uint64) {
	slotsPerYear := uint32(lib.SlotsPerYear())
	slotsPerMonth := slotsPerYear / 12
	nSlots := uint32(years) * slotsPerYear
	mineAmount := lib.MineAmount

	rnd := rand.New(rand.NewSource(seed))
	cur := sample{bootstrap: lib.InitialSupply}
	remaining := rInit
	nextTransit := drawGap(rnd, meanPace, minPace)

	// the branch bonus base is a step function of the slot; walk it in segments
	// rather than evaluating it per slot, which would mean tens of millions of
	// EasyFL evaluations
	var bonusBase uint64
	var bonusSegEnd uint32

	monthly = make([]sample, 0, years*12+1)
	yearly = make([]sample, 0, years+1)
	monthly = append(monthly, cur)
	yearly = append(yearly, cur)

	start := time.Now()
	for s := uint32(0); s < nSlots; s++ {
		if s >= bonusSegEnd {
			bonusBase = lib.BranchInflationBonusBase(s)
			bonusSegEnd = findNextBonusChangeSlot(lib, bonusBase, s+1, nSlots)
		}
		// chain inflation, on the balance each pool starts the slot with
		den := m0 + uint64(s)
		cur.bootstrap += cur.bootstrap / den
		cur.branch += cur.branch / den
		cur.mined += cur.mined / den
		// this slot's branch pays its bonus: the VRF draw, not the base
		cur.branch += 1 + uint64(rnd.Int63n(int64(bonusBase)))
		// a transit lands, as long as the budget can still pay one
		if s == nextTransit {
			if remaining >= mineAmount {
				cur.mined += mineAmount
				minedOut += mineAmount
				remaining -= mineAmount
				transits++
				nextTransit = s + drawGap(rnd, meanPace, minPace)
			} else {
				nextTransit = nSlots // budget exhausted, stop looking
			}
		}

		cur.slot = s + 1
		if cur.slot%slotsPerMonth == 0 {
			monthly = append(monthly, cur)
		}
		if cur.slot%slotsPerYear == 0 {
			yearly = append(yearly, cur)
		}
	}
	fmt.Printf("  emulated %s slots in %v\n\n", util.Th(nSlots), time.Since(start).Round(time.Millisecond))
	return
}

// drawGap draws the slots to the next transit. Mining is a memoryless search,
// which the exponential captures; shifting it by the ledger's minimum pace
// keeps every gap legal while the mean stays at the observed one.
func drawGap(rnd *rand.Rand, meanPace float64, minPace uint32) uint32 {
	gap := uint32(math.Round(float64(minPace) + rnd.ExpFloat64()*(meanPace-float64(minPace))))
	if gap < minPace {
		gap = minPace
	}
	return gap
}

// chainInflationOneSlotFast is the ledger's chainInflationOneSlot inlined. The
// loop runs it tens of millions of times and going through the EasyFL evaluator
// there would take hours; checkChainInflationFormula pins the two together.
func chainInflationOneSlotFast(amount, m0 uint64, slot uint32) uint64 {
	return amount / (m0 + uint64(slot))
}

func checkChainInflationFormula(lib *ledger.Library, m0 uint64) {
	for _, s := range []uint32{0, 1, 1000, 1_000_000, 30_000_000} {
		for _, a := range []uint64{lib.InitialSupply, base.PROX, 12_345_678_901} {
			glb.Assertf(lib.ChainInflationOneSlot(a, s) == chainInflationOneSlotFast(a, m0, s),
				"chain inflation formula mismatch at slot %d, amount %d", s, a)
		}
	}
}

// genesisMintable is R_init, read off the genesis mine output where it is the
// mineLock's R. It is not among the wallet-facing ledger constants.
func genesisMintable(lib *ledger.Library) uint64 {
	lockBytes, err := ledger.GenesisMineChainOutput().Output.At(int(ledger.ConstraintIndexLock))
	glb.AssertNoError(err)
	ml, err := ledger.MineLockFromBytesWithLib(lockBytes, lib)
	glb.AssertNoError(err)
	return ml.R
}

func printYearTable(yearly []sample, slotsPerYear uint32) {
	fmt.Printf("%-5s  %18s  %18s  %18s  %18s  %9s\n",
		"Year", "Bootstrap", "BranchBonus", "Mined", "Supply", "YoY")
	fmt.Println("  " + strconvRepeat("-", 100))
	for i := 1; i < len(yearly); i++ {
		prev, y := yearly[i-1], yearly[i]
		yoy := 100 * float64(y.supply()-prev.supply()) / float64(prev.supply())
		fmt.Printf("%-5d  %18s  %18s  %18s  %18s  %8.2f%%\n",
			i,
			util.Th(y.bootstrap/base.PROX),
			util.Th(y.branch/base.PROX),
			util.Th(y.mined/base.PROX),
			util.Th(y.supply()/base.PROX),
			yoy)
	}
}

func strconvRepeat(s string, n int) string {
	ret := make([]byte, 0, n*len(s))
	for i := 0; i < n; i++ {
		ret = append(ret, s...)
	}
	return string(ret)
}

// findNextBonusChangeSlot finds the first slot in [lo, hi) where
// BranchInflationBonusBase differs from currentBonus. Returns hi if the bonus
// is constant throughout the range.
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

var (
	colBootstrap = color.RGBA{R: 63, G: 107, B: 138, A: 255} // blue
	colBranch    = color.RGBA{R: 74, G: 124, B: 89, A: 255}  // green
	colMined     = color.RGBA{R: 190, G: 140, B: 60, A: 255} // amber
	colRate      = color.RGBA{R: 160, G: 74, B: 68, A: 255}  // red
)

func prox(motes uint64) float64 { return float64(motes) / float64(base.PROX) }

// compact renders an axis value in the unit that keeps it short.
func compact(v float64) string {
	switch {
	case v >= 1e9:
		return fmt.Sprintf("%.1fB", v/1e9)
	case v >= 1e6:
		return fmt.Sprintf("%.0fM", v/1e6)
	case v >= 1e3:
		return fmt.Sprintf("%.0fK", v/1e3)
	}
	return fmt.Sprintf("%.0f", v)
}

// niceTicks lays ticks out over [0, max] at a round step, so the axis reads in
// human units rather than the default exponent notation.
func niceTicks(max float64, want int) []plot.Tick {
	if max <= 0 || want <= 0 {
		return nil
	}
	raw := max / float64(want)
	mag := math.Pow(10, math.Floor(math.Log10(raw)))
	step := 10 * mag
	for _, m := range []float64{1, 2, 2.5, 5} {
		if raw <= m*mag {
			step = m * mag
			break
		}
	}
	ticks := make([]plot.Tick, 0, want+2)
	for v := 0.0; v <= max*1.000001; v += step {
		ticks = append(ticks, plot.Tick{Value: v, Label: compact(v)})
	}
	return ticks
}

// stackedChart draws the three pools as filled bands over the month axis. Each
// band is a cumulative line filled to zero, drawn largest first, so each one
// covers the band below it and what stays visible is that pool's own share.
func stackedChart(title, yLabel string, total, bootBranch, boot plotter.XYs, yTicks []plot.Tick) (*plot.Plot, error) {
	p := plot.New()
	p.Title.Text = title
	p.X.Label.Text = "Months since genesis"
	p.Y.Label.Text = yLabel
	p.Y.Min = 0
	p.X.Min = 0

	band := func(pts plotter.XYs, c color.RGBA) (*plotter.Line, error) {
		l, err := plotter.NewLine(pts)
		if err != nil {
			return nil, err
		}
		l.Color = c
		l.Width = vg.Points(1)
		fill := c
		fill.A = 210
		l.FillColor = fill
		return l, nil
	}
	lMined, err := band(total, colMined)
	if err != nil {
		return nil, err
	}
	lBranch, err := band(bootBranch, colBranch)
	if err != nil {
		return nil, err
	}
	lBoot, err := band(boot, colBootstrap)
	if err != nil {
		return nil, err
	}
	p.Add(lMined, lBranch, lBoot)
	p.Legend.Add("Bootstrap capital", lBoot)
	p.Legend.Add("Branch bonus", lBranch)
	p.Legend.Add("Mined", lMined)
	p.Legend.Top = true
	p.Legend.Left = true

	p.Y.Tick.Marker = plot.ConstantTicks(yTicks)
	// tick on year boundaries, thinned so a long horizon does not crowd the axis
	n := len(total)
	stride := 12 * tickStride(n/12, 15)
	yearTicks := make([]plot.Tick, 0, n/stride+1)
	for i := 0; i < n; i += stride {
		yearTicks = append(yearTicks, plot.Tick{Value: float64(i), Label: fmt.Sprintf("%d", i)})
	}
	p.X.Tick.Marker = plot.ConstantTicks(yearTicks)
	return p, nil
}

// tickStride picks how many items to skip between labels so that at most want
// of them are drawn.
func tickStride(count, want int) int {
	stride := 1
	for count/stride > want {
		stride++
	}
	return stride
}

// writeSupplyChart stacks the three pools in absolute PROX.
func writeSupplyChart(monthly []sample, filename string) error {
	n := len(monthly)
	total := make(plotter.XYs, n)
	bootBranch := make(plotter.XYs, n)
	boot := make(plotter.XYs, n)
	for i, s := range monthly {
		x := float64(i)
		total[i] = plotter.XY{X: x, Y: prox(s.supply())}
		bootBranch[i] = plotter.XY{X: x, Y: prox(s.bootstrap + s.branch)}
		boot[i] = plotter.XY{X: x, Y: prox(s.bootstrap)}
	}
	p, err := stackedChart(
		"Proxima supply by origin (each band includes the chain inflation it earned)",
		"PROX", total, bootBranch, boot, niceTicks(total[n-1].Y, 8))
	if err != nil {
		return err
	}
	return savePlot(p, filename)
}

// writeSharesChart stacks the same three pools normalized to the supply, which
// is what shows the bootstrap capital being diluted: the absolute chart is
// dominated by growth, this one only by the split.
func writeSharesChart(monthly []sample, filename string) error {
	n := len(monthly)
	total := make(plotter.XYs, n)
	bootBranch := make(plotter.XYs, n)
	boot := make(plotter.XYs, n)
	for i, s := range monthly {
		x := float64(i)
		supply := float64(s.supply())
		total[i] = plotter.XY{X: x, Y: 100}
		bootBranch[i] = plotter.XY{X: x, Y: 100 * float64(s.bootstrap+s.branch) / supply}
		boot[i] = plotter.XY{X: x, Y: 100 * float64(s.bootstrap) / supply}
	}
	ticks := make([]plot.Tick, 0, 6)
	for v := 0.0; v <= 100; v += 20 {
		ticks = append(ticks, plot.Tick{Value: v, Label: fmt.Sprintf("%.0f%%", v)})
	}
	p, err := stackedChart(
		"Proxima supply shares by origin",
		"Share of supply", total, bootBranch, boot, ticks)
	if err != nil {
		return err
	}
	p.Y.Max = 100
	return savePlot(p, filename)
}

// writeRateChart plots the realized year-over-year growth of the whole supply,
// mined emission included.
func writeRateChart(yearly []sample, filename string) error {
	pts := make(plotter.XYs, 0, len(yearly)-1)
	for i := 1; i < len(yearly); i++ {
		prev, y := yearly[i-1], yearly[i]
		pts = append(pts, plotter.XY{
			X: float64(i),
			Y: 100 * float64(y.supply()-prev.supply()) / float64(prev.supply()),
		})
	}

	p := plot.New()
	p.Title.Text = "Proxima year-over-year inflation rate (chain, branch bonus and mining)"
	p.X.Label.Text = "Year since genesis"
	p.Y.Label.Text = "Supply growth over the year, % (log scale)"
	// The first year is dominated by mining and runs two orders of magnitude
	// above the steady state, which on a linear axis flattens every later year
	// into one indistinguishable line. A log axis keeps both readable.
	p.Y.Scale = plot.LogScale{}
	p.Y.Tick.Marker = plot.LogTicks{}
	p.Y.Min = 1

	line, err := plotter.NewLine(pts)
	if err != nil {
		return err
	}
	line.Color = colRate
	line.Width = vg.Points(2)
	dots, err := plotter.NewScatter(pts)
	if err != nil {
		return err
	}
	dots.Color = colRate
	dots.Radius = vg.Points(2.5)
	p.Add(line, dots)

	// the exact rate against each point: on a log axis the later years sit close
	// together, and the number is what the chart is for. Thinned on a long
	// horizon, where every year's label would overlap its neighbours.
	stride := tickStride(len(pts), 12)
	texts := make([]string, len(pts))
	for i := range pts {
		if i == 0 || i == len(pts)-1 || i%stride == 0 {
			texts[i] = fmt.Sprintf("%.1f%%", pts[i].Y)
		}
	}
	labels, err := plotter.NewLabels(plotter.XYLabels{XYs: pts, Labels: texts})
	if err != nil {
		return err
	}
	for i := range labels.TextStyle {
		labels.TextStyle[i].XAlign = -0.5
		labels.TextStyle[i].YAlign = -1.4
	}
	p.Add(labels)

	ticks := make([]plot.Tick, 0, len(pts))
	for i := range pts {
		label := ""
		if i == 0 || i == len(pts)-1 || i%stride == 0 {
			label = fmt.Sprintf("%d", int(pts[i].X))
		}
		ticks = append(ticks, plot.Tick{Value: pts[i].X, Label: label})
	}
	p.X.Tick.Marker = plot.ConstantTicks(ticks)

	return savePlot(p, filename)
}

func savePlot(p *plot.Plot, filename string) error {
	wt, err := p.WriterTo(26*vg.Centimeter, 15*vg.Centimeter, "png")
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
