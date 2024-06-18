package tests

import (
	"fmt"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
)

// Validating and making sense of inflation-related constants

func TestFinalConst(t *testing.T) {
	const (
		inflationChainPerTick         = 400_000
		chainInflationFractionPerTick = ledger.DefaultInitialSupply / inflationChainPerTick
		inflationBranchPerSlot        = 12_000_000
		maxChainInflationPerTick      = ledger.DefaultInitialSupply / chainInflationFractionPerTick
		maxChainInflationPerSlot      = maxChainInflationPerTick * 100

		annualInflationChainReverse      = maxChainInflationPerSlot * 6 * 60 * 24 * 365
		annualInflationChainReversePerc  = float64((annualInflationChainReverse * 100) / ledger.DefaultInitialSupply)
		annualInflationBranchReverse     = inflationBranchPerSlot * 6 * 60 * 24 * 365
		annualInflationBranchReversePerc = float64(annualInflationBranchReverse*100) / ledger.DefaultInitialSupply
	)
	t.Logf("Final constants:")
	t.Logf("init supply: %s", util.GoTh(ledger.DefaultInitialSupply))
	t.Logf(">>> chain inflation fraction per tick: %s", util.GoTh(chainInflationFractionPerTick))
	t.Logf("chain inflation fraction per slot: %s", util.GoTh(chainInflationFractionPerTick/100))
	t.Logf("max chain inflation per slot: %s", util.GoTh(maxChainInflationPerSlot))
	t.Logf(">>> branch inflation per slot: %s", util.GoTh(inflationBranchPerSlot))
	t.Logf("annual chain inflation: %s", util.GoTh(annualInflationChainReverse))
	t.Logf("annual chain inflation: %0.2f%%", annualInflationChainReversePerc)
	t.Logf("annual branch inflation: %s", util.GoTh(annualInflationBranchReverse))
	t.Logf("annual branch inflation : %0.2f%%", annualInflationBranchReversePerc)
}

func TestInflation(t *testing.T) {
	t.Run("1", func(t *testing.T) {
		ledger.L().MustEqual("constGenesisTimeUnix", fmt.Sprintf("u64/%d", ledger.L().ID.GenesisTimeUnix))
		require.EqualValues(t, ledger.L().ID.TicksPerSlot(), ledger.TicksPerSlot())
	})
	t.Run("too small amount", func(t *testing.T) {
		tsIn := ledger.MustNewLedgerTime(0, 1)
		tsOut := ledger.MustNewLedgerTime(0, 51)
		src := fmt.Sprintf("maxChainInflationAmount(%s, %s, u64/100000)", tsIn.Source(), tsOut.Source())
		//lib.EvalFromSource(easyfl.NewGlobalDataTracePrint(nil), src)
		ledger.L().MustEqual(src, "u64/0")
		inflationDirect := ledger.L().ID.ChainInflationAmount(tsIn, tsOut, 100000)
		require.EqualValues(t, 0, inflationDirect)
	})
	t.Run("amount = fraction", func(t *testing.T) {
		tsIn := ledger.MustNewLedgerTime(0, 1)
		tsOut := ledger.MustNewLedgerTime(0, 51)

		amountIn := ledger.L().ID.ChainInflationPerTickFraction
		t.Logf("ChainInflationPerTickFraction const: %s", util.GoTh(amountIn))
		expectedInflation := ledger.L().ID.ChainInflationAmount(tsIn, tsOut, amountIn)
		src := fmt.Sprintf("maxChainInflationAmount(%s, %s, u64/%d)", tsIn.Source(), tsOut.Source(), amountIn)
		ledger.L().MustEqual(src, "u64/50")
		ledger.L().MustEqual(src, fmt.Sprintf("u64/%d", expectedInflation))

		src = fmt.Sprintf("maxChainInflationAmount(%s, %s, u64/%d)", tsIn.Source(), tsOut.Source(), amountIn)
		ledger.L().MustEqual(src, fmt.Sprintf("u64/%d", ledger.L().ID.ChainInflationAmount(tsIn, tsOut, amountIn)))
		t.Logf("inflationAmount: %s", util.GoTh(ledger.L().ID.ChainInflationAmount(tsIn, tsOut, amountIn)))

		//lib.EvalFromSource(easyfl.NewGlobalDataTracePrint(nil), src)
	})
	t.Run("in_out window 1", func(t *testing.T) {
		amountIn := ledger.L().ID.ChainInflationPerTickFraction * 10
		t.Logf("amount n: %s", util.GoTh(amountIn))

		tsIn := ledger.MustNewLedgerTime(0, 1)
		tsOut := ledger.MustNewLedgerTime(12, 99)

		expectedInflation := ledger.L().ID.ChainInflationAmount(tsIn, tsOut, amountIn)
		src := fmt.Sprintf("maxChainInflationAmount(%s, %s, u64/%d)", tsIn.Source(), tsOut.Source(), amountIn)
		ledger.L().MustEqual(src, fmt.Sprintf("u64/%d", expectedInflation))
		ledger.L().MustEqual(src, "u64/12980")
		//lib.EvalFromSource(easyfl.NewGlobalDataTracePrint(nil), src)
	})
	t.Run("in_out window 2", func(t *testing.T) {
		amountIn := ledger.L().ID.ChainInflationPerTickFraction * 10
		t.Logf("amount n: %s", util.GoTh(amountIn))

		tsIn := ledger.MustNewLedgerTime(0, 1)
		tsOut := ledger.MustNewLedgerTime(0, 55)

		expectedInflation := ledger.L().ID.ChainInflationAmount(tsIn, tsOut, amountIn)
		src := fmt.Sprintf("maxChainInflationAmount(%s, %s, u64/%d)", tsIn.Source(), tsOut.Source(), amountIn)
		ledger.L().MustEqual(src, fmt.Sprintf("u64/%d", expectedInflation))
		ledger.L().MustEqual(src, "u64/540")

		//lib.EvalFromSource(easyfl.NewGlobalDataTracePrint(nil), src)
	})
	t.Run("in_out window 3", func(t *testing.T) {
		amountIn := ledger.L().ID.ChainInflationPerTickFraction * 10
		t.Logf("amount n: %s", util.GoTh(amountIn))

		tsIn := ledger.MustNewLedgerTime(0, 1)
		tsOut := ledger.MustNewLedgerTime(100, 99)

		expectedInflation := ledger.L().ID.ChainInflationAmount(tsIn, tsOut, amountIn)
		src := fmt.Sprintf("maxChainInflationAmount(%s, %s, u64/%d)", tsIn.Source(), tsOut.Source(), amountIn)
		ledger.L().MustEqual(src, fmt.Sprintf("u64/%d", expectedInflation))
		ledger.L().MustEqual(src, "u64/0")

		//lib.EvalFromSource(easyfl.NewGlobalDataTracePrint(nil), src)
	})
}
