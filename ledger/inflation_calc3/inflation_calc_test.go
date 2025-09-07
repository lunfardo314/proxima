package inflation_calc3

import (
	"testing"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

func TestInflation3(t *testing.T) {
	t.Logf("----- tick duration: %v, slots per year: %s", ledger.Const.TickDuration, util.Th(ledger.Const.SlotsPerYear()))

	ln := lines.New("   ")
	const (
		initSupply                  = 1_000_000_000_000_000
		inflationTargetAnnual       = initSupply / 10
		inflationEpochSlots         = 100_000
		branchInflationBonus        = 5_000_000
		inflationTargetForSlotConst = uint64(33_000_000)
	)
	ln.Add("initSupply: %s", util.Th(initSupply))
	ln.Add("inflationTargetAnnual: %s", util.Th(inflationTargetAnnual))
	ln.Add("inflationEpochSlots: %s", util.Th(inflationEpochSlots))
	ln.Add("branchInflationBonus: %s", util.Th(branchInflationBonus))
	epochsPerYear := float64(ledger.Const.SlotsPerYear()) / float64(inflationEpochSlots)
	ln.Add("epochsPerYear: %.03f", epochsPerYear)
	inflationTargetForEpoch := inflationTargetAnnual / epochsPerYear
	ln.Add("inflationTargetForEpoch: %s", util.Th(int(inflationTargetForEpoch)))
	inflationTargetForSlot := inflationTargetForEpoch / inflationEpochSlots
	ln.Add("inflationTargetForSlot: %s", util.Th(int(inflationTargetForSlot)))
	// uint64(inflationTargetForEpoch / inflationEpochSlots)
	ln.Add("inflationTargetForSlotConst: %s", util.Th(inflationTargetForSlotConst))

	t.Logf("\n%s", ln.String())

	P := func(n int) uint64 {
		return initSupply/inflationTargetForSlotConst + uint64(n) - 1
	}

	const years = 100

	supply := uint64(initSupply)
	slotsPerYear := ledger.Const.SlotsPerYear()
	year := 1
	supplyYearStart := uint64(initSupply)

	startTime := time.Now()
	for s := 0; s < slotsPerYear*years; s++ {
		p := P(s)
		if s > 0 && s%slotsPerYear == 0 {
			inflationAnnual := supply - supplyYearStart
			t.Logf("EoY %4d, supply %s, inflation: %s, BIB: %s, rate YoY: %.02f%% (incl BIB %.02f%%)",
				year, util.Th(supply), util.Th(inflationAnnual), util.Th(branchInflationBonus*slotsPerYear),
				100*float64(inflationAnnual)/float64(supplyYearStart), 100*float64(branchInflationBonus*slotsPerYear)/float64(supplyYearStart))
			supplyYearStart = supply
			year++
		}
		supply += supply/p + branchInflationBonus
	}
	t.Logf("calc per sec: %d", int64(slotsPerYear*years)/int64(time.Since(startTime).Seconds()))
	t.Logf("MaxUint64: %s", util.Th(uint64(0xffffffffffffffff)))
}

// very long test

func TestInflation3Final(t *testing.T) {
	t.Skip()
	t.Logf("----- tick duration: %v, slots per year: %s", ledger.Const.TickDuration, util.Th(ledger.Const.SlotsPerYear()))

	ln := lines.New("   ")
	const (
		S0 = 1_000_000_000_000_000
		C  = uint64(33_000_000)
		SC = S0 / C
		B  = 5_000_000
	)
	ln.Add("S0 = %s", util.Th(S0))
	ln.Add("C = %s", util.Th(C))
	ln.Add("B = %s", util.Th(B))
	ln.Add("SC = %s", util.Th(SC))
	t.Logf("\n%s", ln.String())

	const years = 3

	supply := ledger.Const.InitialSupply
	slotsPerYear := ledger.Const.SlotsPerYear()
	year := 1
	supplyYearStart := ledger.Const.InitialSupply

	startTime := time.Now()
	for s := 0; s < slotsPerYear*years; s++ {
		ts := base.NewLedgerTime(base.Slot(s), 5)
		inflationSlot := ledger.L().CalcChainInflationAmountOneSlot(ts.Slot, supply)

		if s > 0 && s%slotsPerYear == 0 {
			inflationAnnual := supply - supplyYearStart
			t.Logf("EoY %4d, supply %s, inflation: %s, BIB: %s, rate YoY: %.02f%% (incl BIB %.02f%%), calc per sec: %d",
				year, util.Th(supply), util.Th(inflationAnnual), util.Th(B*slotsPerYear),
				100*float64(inflationAnnual)/float64(supplyYearStart), 100*float64(B*slotsPerYear)/float64(supplyYearStart),
				int64(slotsPerYear)/int64(time.Since(startTime).Seconds()))
			supplyYearStart = supply
			year++
			startTime = time.Now()
		}
		supply += inflationSlot + B
	}
}
