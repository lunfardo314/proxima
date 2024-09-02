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
	t.Logf("branch inflation per slot: %s", util.Th(ledger.DefaultBranchInflationBonusBase))
	t.Logf("slots per year: %s", util.Th(ledger.L().ID.SlotsPerYear()))
	t.Logf("ticks per year: %s", util.Th(ledger.L().ID.TicksPerYear()))
	branchInflationAnnual := ledger.L().ID.BranchInflationBonusBase * uint64(ledger.L().ID.SlotsPerYear())
	t.Logf("branch inflation per year: %s", util.Th(branchInflationAnnual))
	branchInflationAnnualPerc := float64(branchInflationAnnual*100) / float64(ledger.DefaultInitialSupply)
	t.Logf("branch inflation per year %% of initial supply: %.2f%%", branchInflationAnnualPerc)
	t.Logf("max chain inflation per slot: %s", util.Th(ledger.DefaultChainInflationPerTickBase*int(ledger.TicksPerSlot)))
	inTs := ledger.NewLedgerTime(0, 1)
	outTs := ledger.NewLedgerTime(1, 1)
	t.Logf("chain inflation per initial slot: %s", util.Th(ledger.L().CalcChainInflationAmount(inTs, outTs, ledger.DefaultInitialSupply, 0)))

	supply := ledger.DefaultInitialSupply
	for i := 0; i < ledger.L().ID.SlotsPerYear(); i++ {
		supply += supply / (ledger.DefaultChainInflationPerTickBase / ledger.TicksPerSlot)
	}
	t.Logf("annual chain inflation: %s", util.Th(supply))
	t.Logf("annual chain inflation %% of initial supply: %.2f%%", (float64(supply-ledger.DefaultInitialSupply)*100)/float64(ledger.DefaultInitialSupply))

}

func TestInflation(t *testing.T) {
	t.Run("1", func(t *testing.T) {
		ledger.L().MustEqual("constGenesisTimeUnix", fmt.Sprintf("u64/%d", ledger.L().ID.GenesisTimeUnix))
	})
	t.Run("upper supply bound", func(t *testing.T) {
		tsIn := ledger.NewLedgerTime(0, 0)

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
		slotsPerInflationEpoch := int(ledger.L().ID.TicksPerInflationEpoch) / ledger.TicksPerSlot
		tsIn = ledger.NewLedgerTime(ledger.Slot(slotsPerInflationEpoch), 0)
		run(tsIn)
		tsIn = ledger.NewLedgerTime(ledger.Slot(10*slotsPerInflationEpoch), 0)
		run(tsIn)
		tsIn = ledger.NewLedgerTime(ledger.Slot(100*slotsPerInflationEpoch), 0)
		run(tsIn)
		t.Logf("max uint64: %s", util.Th(uint64(math.MaxUint64)))
	})
	t.Run("amount factor", func(t *testing.T) {
		run := func(ts ledger.Time, amount uint64) uint64 {
			af := ledger.L().AmountFactor(ts, amount)
			t.Logf("amountFactor(%s, %s) = %s", ts.String(), util.Th(amount), util.Th(af))
			return af
		}
		tsIn := ledger.NewLedgerTime(0, 0)
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
	t.Run("opportunity window", func(t *testing.T) {
		run := func(tsIn, tsOut ledger.Time) bool {
			yn := ledger.L().InsideInflationOpportunityWindow(ledger.DiffTicks(tsOut, tsIn))
			t.Logf("insideOpportunityWindow(%s, %s) = %v",
				tsIn.String(), tsOut.String(), yn)
			return yn
		}
		tsIn := ledger.NewLedgerTime(0, 0)
		require.True(t, run(tsIn, tsIn.AddTicks(1)))
		require.True(t, run(tsIn, tsIn.AddTicks(100)))
		require.True(t, run(tsIn, tsIn.AddTicks(1199)))
		require.True(t, run(tsIn, tsIn.AddTicks(1200)))
		require.True(t, run(tsIn, tsIn.AddTicks(ledger.DefaultChainInflationOpportunitySlots*ledger.TicksPerSlot+ledger.MaxTick)))
		require.False(t, run(tsIn, tsIn.AddTicks(ledger.DefaultChainInflationOpportunitySlots*ledger.TicksPerSlot+ledger.MaxTick+1)))
		require.False(t, run(tsIn, tsIn.AddTicks(5000)))
	})
	t.Run("chain inflation", func(t *testing.T) {
		run := func(tsIn, tsOut ledger.Time, amount, delayed uint64) uint64 {
			inflation := ledger.L().CalcChainInflationAmount(tsIn, tsOut, amount, delayed)
			t.Logf("chainInflation(%s, %s, %s, %s) = %s",
				tsIn.String(), tsOut.String(), util.Th(amount), util.Th(delayed), util.Th(inflation))
			return inflation
		}
		t.Logf("chain inflation per tick base: %s", util.Th(ledger.L().ID.ChainInflationPerTickBase))
		tsIn := ledger.NewLedgerTime(0, 0)
		tsOut := tsIn.AddTicks(1)
		run(tsIn, tsOut, 1, 0)
		run(tsIn, tsOut, ledger.DefaultInitialSupply, 0)
		run(tsIn, tsOut, ledger.DefaultInitialSupply/2, 0)
		run(tsIn, tsOut, ledger.DefaultInitialSupply/3, 0)
		run(tsIn, tsOut, ledger.DefaultInitialSupply/10, 0)
		run(tsIn, tsOut, ledger.DefaultInitialSupply/11, 0)
		run(tsIn, tsOut, ledger.DefaultInitialSupply/20, 0)
		run(tsIn, tsOut, ledger.DefaultInitialSupply/1000, 0)
		run(tsIn, tsOut, ledger.DefaultInitialSupply/100000, 0)
		run(tsIn, tsOut, uint64(ledger.DefaultInitialSupply/(150000+8549)), 0)
		run(tsIn, tsOut, uint64(ledger.DefaultInitialSupply/(150000+8550)), 0)
		run(tsIn, tsOut, uint64(ledger.DefaultInitialSupply/(150000+8550)), 1336)

		tsIn = ledger.NewLedgerTime(0, 0)
		tsOut = tsIn.AddTicks(1199)
		run(tsIn, tsOut, 1, 0)
		run(tsIn, tsOut, ledger.DefaultInitialSupply, 0)
		run(tsIn, tsOut, ledger.DefaultInitialSupply/2, 0)
		run(tsIn, tsOut, ledger.DefaultInitialSupply/3, 0)
		run(tsIn, tsOut, ledger.DefaultInitialSupply/10, 0)
		run(tsIn, tsOut, ledger.DefaultInitialSupply/11, 0)
		run(tsIn, tsOut, ledger.DefaultInitialSupply/20, 0)
		run(tsIn, tsOut, ledger.DefaultInitialSupply/1000, 0)
		run(tsIn, tsOut, ledger.DefaultInitialSupply/100000, 0)
		run(tsIn, tsOut, uint64(ledger.DefaultInitialSupply/(150000+8549)), 0)
		run(tsIn, tsOut, uint64(ledger.DefaultInitialSupply/(150000+8550)), 0)

		t.Logf("----- inflation opportunity window %d slots", ledger.L().ID.ChainInflationOpportunitySlots)
		tsIn = ledger.NewLedgerTime(0, 0)
		tsOut = tsIn.AddTicks(1300)
		run(tsIn, tsOut, 1, 0)
		run(tsIn, tsOut, ledger.DefaultInitialSupply, 0)
		run(tsIn, tsOut, ledger.DefaultInitialSupply/2, 0)
		run(tsIn, tsOut, ledger.DefaultInitialSupply/3, 0)
		run(tsIn, tsOut, ledger.DefaultInitialSupply/10, 0)
		run(tsIn, tsOut, ledger.DefaultInitialSupply/11, 0)
		run(tsIn, tsOut, ledger.DefaultInitialSupply/20, 0)
		run(tsIn, tsOut, ledger.DefaultInitialSupply/1000, 0)
		run(tsIn, tsOut, ledger.DefaultInitialSupply/100000, 0)
		run(tsIn, tsOut, uint64(ledger.DefaultInitialSupply/(150000+8549)), 0)
		run(tsIn, tsOut, uint64(ledger.DefaultInitialSupply/(150000+8550)), 0)
	})
}
