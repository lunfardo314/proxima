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
	t.Logf("init supply: %s", util.GoTh(ledger.DefaultInitialSupply))
	t.Logf("chain inflation fraction per tick: %s", util.GoTh(ledger.DefaultChainInflationFractionPerTick))
	t.Logf("branch inflation per slot: %s", util.GoTh(ledger.DefaultMaxBranchInflationBonus))
	t.Logf("slots per year: %s", util.GoTh(ledger.L().ID.SlotsPerYear()))
	t.Logf("ticks per year: %s", util.GoTh(ledger.L().ID.TicksPerYear()))
	branchInflationAnnual := ledger.L().ID.BranchInflationBonusBase * uint64(ledger.L().ID.SlotsPerYear())
	t.Logf("branch inflation per year: %s", util.GoTh(branchInflationAnnual))
	branchInflationAnnualPerc := float64(branchInflationAnnual*100) / float64(ledger.DefaultInitialSupply)
	t.Logf("branch inflation per year %% of initial supply: %.2f%%", branchInflationAnnualPerc)
	t.Logf("chain inflation fraction per slot: %s", util.GoTh(ledger.DefaultChainInflationFractionPerTick/ledger.DefaultTicksPerSlot))
	inTs := ledger.MustNewLedgerTime(0, 1)
	outTs := ledger.MustNewLedgerTime(1, 1)
	t.Logf("chain inflation per initial slot: %s", util.GoTh(ledger.L().CalcChainInflationAmount(inTs, outTs, ledger.DefaultInitialSupply, 0)))

	supply := ledger.DefaultInitialSupply
	for i := 0; i < ledger.L().ID.SlotsPerYear(); i++ {
		supply += supply / (ledger.DefaultChainInflationFractionPerTick / ledger.DefaultTicksPerSlot)
	}
	t.Logf("annual chain inflation: %s", util.GoTh(supply))
	t.Logf("annual chain inflation %% of initial supply: %.2f%%", (float64(supply-ledger.DefaultInitialSupply)*100)/float64(ledger.DefaultInitialSupply))

}

func TestInflationOpportunityWindow(t *testing.T) {
	testFun := func(ticks int, amount uint64, expect bool) {
		res := ledger.L().InsideInflationOpportunityWindow(ticks, amount)
		t.Logf("inside inflation is '%v' window for %d ticks, %s input amount", res, ticks, util.GoTh(amount))
		require.EqualValues(t, expect, res)
	}

	t.Run("1", func(t *testing.T) {
		util.RequirePanicOrErrorWith(t, func() error {
			ledger.L().InsideInflationOpportunityWindow(0, 0)
			return nil
		}, "divide by zero")
	})
	t.Run("2", func(t *testing.T) {
		util.RequirePanicOrErrorWith(t, func() error {
			ledger.L().InsideInflationOpportunityWindow(1, 0)
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
	testFun := func(inTs, outTs ledger.Time, inAmount, delayed uint64, expect uint64) {
		inflation := ledger.L().CalcChainInflationAmount(inTs, outTs, inAmount, delayed)
		t.Logf("inTs: %s, outTs: %s, diffTicks: %d, in: %s, delayd: %s -> inflation %s",
			inTs.String(), outTs.String(), ledger.DiffTicks(outTs, inTs), util.GoTh(inAmount), util.GoTh(delayed), util.GoTh(inflation))
		require.EqualValues(t, int(expect), int(inflation))
	}
	t.Run("1", func(t *testing.T) {
		ledger.L().MustEqual("constGenesisTimeUnix", fmt.Sprintf("u64/%d", ledger.L().ID.GenesisTimeUnix))
		require.EqualValues(t, ledger.L().ID.TicksPerSlot(), ledger.TicksPerSlot())
	})
	t.Run("amounts", func(t *testing.T) {
		frac := ledger.L().ID.ChainInflationFractionPerTick
		maxInflationTicks := int(ledger.L().ID.ChainInflationOpportunitySlots)*ledger.L().ID.TicksPerSlot() + int(ledger.L().ID.MaxTickValueInSlot)

		t.Logf("chain inflation fraction per tick: %s", util.GoTh(frac))

		tsIn := ledger.MustNewLedgerTime(0, 0)

		diff := 1
		tsOut := tsIn.AddTicks(diff)
		testFun(tsIn, tsOut, frac-1, 0, 0)
		testFun(tsIn, tsOut, frac, 0, 1)
		t.Logf("-------  minimum amount which can be inflated in %d tick(s): %s dust = %s PRXI", diff, util.GoTh(frac), util.GoTh(frac/ledger.PRXI))
		testFun(tsIn, tsOut, ledger.DefaultInitialSupply, 0, 400_000)
		t.Logf("-------- inflation amount of default initial supply in %d tick(s): %s dust", diff, util.GoTh(400_000))

		diff = 5
		tsOut = tsIn.AddTicks(diff)
		testFun(tsIn, tsOut, frac/uint64(diff)-1, 0, 0)
		testFun(tsIn, tsOut, frac/uint64(diff), 0, 1)
		t.Logf("-------- minimum amount which can be inflated in %d ticks: %s dust = %s PRXI", diff, util.GoTh(frac), util.GoTh(frac/ledger.PRXI))
		testFun(tsIn, tsOut, ledger.DefaultInitialSupply, 0, uint64(diff*400_000))
		t.Logf("-------- inflation amount of default initial supply in %d ticks(s): %s dust", diff, util.GoTh(diff*400_000))

		diff = ledger.L().ID.TicksPerSlot()
		tsOut = tsIn.AddTicks(diff)
		testFun(tsIn, tsOut, frac/uint64(diff)-1, 0, 0)
		testFun(tsIn, tsOut, frac/uint64(diff), 0, 1)
		t.Logf("-------- minimum amount which can be inflated in %d ticks: %s dust = %s PRXI", diff, util.GoTh(frac), util.GoTh(frac/ledger.PRXI))
		testFun(tsIn, tsOut, ledger.DefaultInitialSupply, 0, uint64(diff*400_000))
		t.Logf("-------- inflation amount of default initial supply in %d ticks(s): %s dust", diff, util.GoTh(diff*400_000))

		// TODO
		diff = maxInflationTicks
		diff = 1200
		tsOut = tsIn.AddTicks(diff)
		testFun(tsIn, tsOut, frac/uint64(diff)-1, 0, 0)
		testFun(tsIn, tsOut, frac/uint64(diff), 0, 1)
		t.Logf("-------- minimum amount which can be inflated in %d ticks: %s dust = %s PRXI", diff, util.GoTh(frac), util.GoTh(frac/ledger.PRXI))
		testFun(tsIn, tsOut, ledger.DefaultInitialSupply, 0, uint64(diff*400_000))
		t.Logf("-------- inflation amount of default initial supply in %d ticks(s): %s dust", diff, util.GoTh(diff*400_000))
	})
}
