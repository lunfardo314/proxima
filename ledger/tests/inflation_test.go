package tests

import (
	"fmt"
	"math"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
)

// Validating and making sense of inflation-related constants

func TestInflationConst1Year(t *testing.T) {
	t.Logf("init supply: %s", util.Th(ledger.DefaultInitialSupply))
	t.Logf("max annual chain inflation: %s", util.Th(ledger.DefaultChainInflationPerTickBase))
	t.Logf("branch inflation per slot: %s", util.Th(ledger.DefaultMaxBranchInflationBonus))
	t.Logf("slots per year: %s", util.Th(ledger.L().ID.SlotsPerYear()))
	t.Logf("ticks per year: %s", util.Th(ledger.L().ID.TicksPerYear()))
	branchInflationAnnual := ledger.L().ID.BranchInflationBonusBase * uint64(ledger.L().ID.SlotsPerYear())
	t.Logf("branch inflation per year: %s", util.Th(branchInflationAnnual))
	branchInflationAnnualPerc := float64(branchInflationAnnual*100) / float64(ledger.DefaultInitialSupply)
	t.Logf("branch inflation per year %% of initial supply: %.2f%%", branchInflationAnnualPerc)
	t.Logf("max chain inflation per slot: %s", util.Th(ledger.DefaultChainInflationPerTickBase*int(ledger.TicksPerSlot())))
	inTs := ledger.MustNewLedgerTime(0, 1)
	outTs := ledger.MustNewLedgerTime(1, 1)
	t.Logf("chain inflation per initial slot: %s", util.Th(ledger.L().CalcChainInflationAmount(inTs, outTs, ledger.DefaultInitialSupply, 0)))

	supply := ledger.DefaultInitialSupply
	for i := 0; i < ledger.L().ID.SlotsPerYear(); i++ {
		supply += supply / (ledger.DefaultChainInflationPerTickBase / ledger.DefaultTicksPerSlot)
	}
	t.Logf("annual chain inflation: %s", util.Th(supply))
	t.Logf("annual chain inflation %% of initial supply: %.2f%%", (float64(supply-ledger.DefaultInitialSupply)*100)/float64(ledger.DefaultInitialSupply))

}

func TestInflationOpportunityWindow(t *testing.T) {
	testFun := func(ticks int, amount uint64, expect bool) {
		res := ledger.L().InsideInflationOpportunityWindow(ticks)
		t.Logf("inside inflation is '%v' window for %d ticks, %s input amount", res, ticks, util.Th(amount))
		require.EqualValues(t, expect, res)
	}

	t.Run("1", func(t *testing.T) {
		util.RequirePanicOrErrorWith(t, func() error {
			ledger.L().InsideInflationOpportunityWindow(0)
			return nil
		}, "divide by zero")
	})
	t.Run("2", func(t *testing.T) {
		util.RequirePanicOrErrorWith(t, func() error {
			ledger.L().InsideInflationOpportunityWindow(1)
			return nil
		}, "divide by zero")
	})
	t.Run("3", func(t *testing.T) {
		testFun(1, 1, true)
		testFun(int(ledger.L().ID.MaxTickValueInSlot), 1, true)
		testFun(ledger.L().ID.TicksPerSlot(), 1, true)
		testFun(1000, 1, true)
		testFun(int(ledger.L().ID.ChainInflationOpportunitySlots)*ledger.L().ID.TicksPerSlot(), 1, true)
		testFun(int(ledger.L().ID.ChainInflationOpportunitySlots)*ledger.L().ID.TicksPerSlot()+1, 1, true)

		maxInflationTicks := int(ledger.L().ID.ChainInflationOpportunitySlots)*ledger.L().ID.TicksPerSlot() + int(ledger.L().ID.MaxTickValueInSlot)
		t.Logf("max inflation ticks: %d", maxInflationTicks)
		testFun(maxInflationTicks, 1, true)
		testFun(maxInflationTicks+1, 1, false)
		testFun(maxInflationTicks+1000, 1, false)

		testFun(1, math.MaxUint64/2, true)
		testFun(1, math.MaxUint64/2+1, false)
		testFun(maxInflationTicks, ledger.L().ID.InitialSupply, true)
		testFun(maxInflationTicks, ledger.L().ID.InitialSupply+1, true)
		testFun(maxInflationTicks, ledger.L().ID.InitialSupply*10, true)
		testFun(maxInflationTicks, ledger.L().ID.InitialSupply*14, true)
		testFun(maxInflationTicks, ledger.L().ID.InitialSupply*15, false)
	})
}

func TestInflation(t *testing.T) {
	//testFun := func(inTs, outTs ledger.Time, inAmount, delayed uint64, expect uint64) {
	//	inflation := ledger.L().CalcChainInflationAmount(inTs, outTs, inAmount, delayed)
	//	t.Logf("inTs: %s, outTs: %s, diffTicks: %d, in: %s, delayed: %s -> inflation %s",
	//		inTs.String(), outTs.String(), ledger.DiffTicks(outTs, inTs), util.Th(inAmount), util.Th(delayed), util.Th(inflation))
	//	require.EqualValues(t, int(expect), int(inflation))
	//}
	t.Run("1", func(t *testing.T) {
		ledger.L().MustEqual("constGenesisTimeUnix", fmt.Sprintf("u64/%d", ledger.L().ID.GenesisTimeUnix))
		require.EqualValues(t, ledger.L().ID.TicksPerSlot(), ledger.TicksPerSlot())
	})
	t.Run("upper supply bound", func(t *testing.T) {
		tsIn := ledger.MustNewLedgerTime(0, 0)

		run := func(ts ledger.Time) {
			u := ledger.L().UpperSupplyBound(tsIn)
			t.Logf("upperSupplyBound(%s) = %s (%.2f%%)", tsIn.String(), util.Th(u), float64(u-ledger.L().ID.InitialSupply)*100/float64(ledger.L().ID.InitialSupply))
		}
		run(tsIn)
		tsIn = tsIn.AddTicks(1)
		run(tsIn)
		tsIn = tsIn.AddTicks(99)
		run(tsIn)
		tsIn = tsIn.AddTicks(1_000_000)
		run(tsIn)
		slotsPerInflationEpoch := int(ledger.L().ID.TicksPerInflationEpoch) / ledger.L().ID.TicksPerSlot()
		tsIn = ledger.MustNewLedgerTime(ledger.Slot(slotsPerInflationEpoch), 0)
		run(tsIn)
		tsIn = ledger.MustNewLedgerTime(ledger.Slot(10*slotsPerInflationEpoch), 0)
		run(tsIn)
		tsIn = ledger.MustNewLedgerTime(ledger.Slot(100*slotsPerInflationEpoch), 0)
		run(tsIn)
		t.Logf("max uint64: %s", util.Th(uint64(math.MaxUint64)))
	})
	t.Run("amount factor", func(t *testing.T) {
		run := func(ts ledger.Time, amount uint64) uint64 {
			af := ledger.L().AmountFactor(ts, amount)
			t.Logf("amountFactor(%s, %s) = %s", ts.String(), util.Th(amount), util.Th(af))
			return af
		}
		tsIn := ledger.MustNewLedgerTime(0, 0)
		af := run(tsIn, 1)
		require.EqualValues(t, ledger.DefaultInitialSupply, af)
		af = run(tsIn, ledger.DefaultInitialSupply)
		require.EqualValues(t, 10, af)
		af = run(tsIn, ledger.DefaultInitialSupply/2)
		require.EqualValues(t, 10, af)
		af = run(tsIn, ledger.DefaultInitialSupply/9)
		require.EqualValues(t, 10, af)
		af = run(tsIn, ledger.DefaultInitialSupply/10)
		require.EqualValues(t, 10, af)
		af = run(tsIn, ledger.DefaultInitialSupply/11)
		require.EqualValues(t, 11, af)
		af = run(tsIn, ledger.DefaultInitialSupply/20)
		require.EqualValues(t, 20, af)
		af = run(tsIn, ledger.DefaultInitialSupply/1000)
		require.EqualValues(t, 1000, af)
	})

	//t.Run("amounts", func(t *testing.T) {
	//	maxInflationTicks := int(ledger.L().ID.ChainInflationOpportunitySlots)*ledger.L().ID.TicksPerSlot() + int(ledger.L().ID.MaxTickValueInSlot)
	//
	//	t.Logf("chain inflation per tick base: %s", util.Th(ledger.L().ID.ChainInflationPerTickBase))
	//
	//	tsIn := ledger.MustNewLedgerTime(0, 0)
	//
	//	diff := 1
	//	tsOut := tsIn.AddTicks(diff)
	//	testFun(tsIn, tsOut, 1, 0, 0)
	//	testFun(tsIn, tsOut, 1, 0, 1)
	//	t.Logf("-------  minimum amount which can be inflated in %d tick(s): %s dust = %s PRXI", diff, util.Th(frac), util.Th(frac/ledger.PRXI))
	//	testFun(tsIn, tsOut, ledger.DefaultInitialSupply, 0, 400_000)
	//	t.Logf("-------- inflation amount of default initial supply in %d tick(s): %s dust", diff, util.Th(400_000))
	//
	//	diff = 5
	//	tsOut = tsIn.AddTicks(diff)
	//	amount := frac / uint64(diff)
	//	testFun(tsIn, tsOut, amount-1, 0, 0)
	//	testFun(tsIn, tsOut, amount, 0, 1)
	//	t.Logf("-------- minimum amount which can be inflated in %d ticks: %s dust = %s PRXI", diff, util.Th(amount), util.Th(amount/ledger.PRXI))
	//	testFun(tsIn, tsOut, ledger.DefaultInitialSupply, 0, uint64(diff*400_000))
	//	t.Logf("-------- inflation amount of default initial supply in %d ticks(s): %s dust", diff, util.Th(diff*400_000))
	//
	//	diff = ledger.L().ID.TicksPerSlot()
	//	tsOut = tsIn.AddTicks(diff)
	//	amount = frac / uint64(diff)
	//	testFun(tsIn, tsOut, amount-1, 0, 0)
	//	testFun(tsIn, tsOut, amount, 0, 1)
	//	t.Logf("-------- minimum amount which can be inflated in %d ticks: %s dust = %s PRXI", diff, util.Th(amount), util.Th(amount/ledger.PRXI))
	//	testFun(tsIn, tsOut, ledger.DefaultInitialSupply, 0, uint64(diff*400_000))
	//	t.Logf("-------- inflation amount of default initial supply in %d ticks(s): %s dust", diff, util.Th(diff*400_000))
	//
	//	diff = maxInflationTicks
	//	tsOut = tsIn.AddTicks(diff)
	//	testFun(tsIn, tsOut, 10_000_000, 0, 5)
	//	testFun(tsIn, tsOut, 10_000_000, 1_000, 1_005)
	//
	//	diff = maxInflationTicks + 1
	//	tsOut = tsIn.AddTicks(diff)
	//	testFun(tsIn, tsOut, 10_000_000, 0, 0)
	//	testFun(tsIn, tsOut, 10_000_000, 1_000, 0)
	//})
}
