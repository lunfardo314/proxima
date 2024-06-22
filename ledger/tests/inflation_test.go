package tests

import (
	"fmt"
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
	t.Logf("chain inflation per initial slot: %s", util.GoTh(ledger.L().ID.CalcChainInflationAmount(inTs, outTs, ledger.DefaultInitialSupply)))

	supply := ledger.DefaultInitialSupply
	for i := 0; i < ledger.L().ID.SlotsPerYear(); i++ {
		supply += supply / (ledger.DefaultChainInflationFractionPerTick / ledger.DefaultTicksPerSlot)
	}
	t.Logf("annual chain inflation: %s", util.GoTh(supply))
	t.Logf("annual chain inflation %% of initial supply: %.2f%%", (float64(supply-ledger.DefaultInitialSupply)*100)/float64(ledger.DefaultInitialSupply))

}

func TestInflation(t *testing.T) {
	t.Run("1", func(t *testing.T) {
		ledger.L().MustEqual("constGenesisTimeUnix", fmt.Sprintf("u64/%d", ledger.L().ID.GenesisTimeUnix))
		require.EqualValues(t, ledger.L().ID.TicksPerSlot(), ledger.TicksPerSlot())
	})
	t.Run("too small amount", func(t *testing.T) {
		tsIn := ledger.MustNewLedgerTime(0, 1)
		tsOut := ledger.MustNewLedgerTime(0, 51)
		src := fmt.Sprintf("calcChainInflationAmount(%s, %s, u64/100000)", tsIn.Source(), tsOut.Source())
		//lib.EvalFromSource(easyfl.NewGlobalDataTracePrint(nil), src)
		ledger.L().MustEqual(src, "u64/0")
		inflationDirect := ledger.L().ID.CalcChainInflationAmount(tsIn, tsOut, 100000)
		require.EqualValues(t, 0, inflationDirect)
	})
	t.Run("amount = fraction", func(t *testing.T) {
		tsIn := ledger.MustNewLedgerTime(0, 1)
		tsOut := ledger.MustNewLedgerTime(0, 51)

		amountIn := ledger.L().ID.ChainInflationPerTickFraction
		t.Logf("ChainInflationPerTickFraction const: %s", util.GoTh(amountIn))
		expectedInflation := ledger.L().ID.CalcChainInflationAmount(tsIn, tsOut, amountIn)
		src := fmt.Sprintf("calcChainInflationAmount(%s, %s, u64/%d)", tsIn.Source(), tsOut.Source(), amountIn)
		ledger.L().MustEqual(src, "u64/50")
		ledger.L().MustEqual(src, fmt.Sprintf("u64/%d", expectedInflation))

		src = fmt.Sprintf("calcChainInflationAmount(%s, %s, u64/%d)", tsIn.Source(), tsOut.Source(), amountIn)
		ledger.L().MustEqual(src, fmt.Sprintf("u64/%d", ledger.L().ID.CalcChainInflationAmount(tsIn, tsOut, amountIn)))
		t.Logf("inflationAmount: %s", util.GoTh(ledger.L().ID.CalcChainInflationAmount(tsIn, tsOut, amountIn)))

		//lib.EvalFromSource(easyfl.NewGlobalDataTracePrint(nil), src)
	})
	t.Run("in_out window 1", func(t *testing.T) {
		amountIn := ledger.L().ID.ChainInflationPerTickFraction * 10
		t.Logf("amount n: %s", util.GoTh(amountIn))

		tsIn := ledger.MustNewLedgerTime(0, 1)
		tsOut := ledger.MustNewLedgerTime(12, 99)

		expectedInflation := ledger.L().ID.CalcChainInflationAmount(tsIn, tsOut, amountIn)
		src := fmt.Sprintf("calcChainInflationAmount(%s, %s, u64/%d)", tsIn.Source(), tsOut.Source(), amountIn)
		ledger.L().MustEqual(src, fmt.Sprintf("u64/%d", expectedInflation))
		ledger.L().MustEqual(src, "u64/12980")
		//lib.EvalFromSource(easyfl.NewGlobalDataTracePrint(nil), src)
	})
	t.Run("in_out window 2", func(t *testing.T) {
		amountIn := ledger.L().ID.ChainInflationPerTickFraction * 10
		t.Logf("amount n: %s", util.GoTh(amountIn))

		tsIn := ledger.MustNewLedgerTime(0, 1)
		tsOut := ledger.MustNewLedgerTime(0, 55)

		expectedInflation := ledger.L().ID.CalcChainInflationAmount(tsIn, tsOut, amountIn)
		src := fmt.Sprintf("calcChainInflationAmount(%s, %s, u64/%d)", tsIn.Source(), tsOut.Source(), amountIn)
		ledger.L().MustEqual(src, fmt.Sprintf("u64/%d", expectedInflation))
		ledger.L().MustEqual(src, "u64/540")

		//lib.EvalFromSource(easyfl.NewGlobalDataTracePrint(nil), src)
	})
	t.Run("in_out window 3", func(t *testing.T) {
		amountIn := ledger.L().ID.ChainInflationPerTickFraction * 10
		t.Logf("amount n: %s", util.GoTh(amountIn))

		tsIn := ledger.MustNewLedgerTime(0, 1)
		tsOut := ledger.MustNewLedgerTime(100, 99)

		expectedInflation := ledger.L().ID.CalcChainInflationAmount(tsIn, tsOut, amountIn)
		src := fmt.Sprintf("calcChainInflationAmount(%s, %s, u64/%d)", tsIn.Source(), tsOut.Source(), amountIn)
		ledger.L().MustEqual(src, fmt.Sprintf("u64/%d", expectedInflation))
		ledger.L().MustEqual(src, "u64/0")

		//lib.EvalFromSource(easyfl.NewGlobalDataTracePrint(nil), src)
	})
}
