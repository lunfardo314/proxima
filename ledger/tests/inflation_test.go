package tests

import (
	"fmt"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
)

func TestInflation(t *testing.T) {
	t.Run("1", func(t *testing.T) {
		ledger.L().MustEqual("constGenesisTimeUnix", fmt.Sprintf("u64/%d", ledger.L().ID.GenesisTimeUnix))
		require.EqualValues(t, ledger.L().ID.TicksPerSlot(), ledger.TicksPerSlot())
	})
	t.Run("2", func(t *testing.T) {
		tsIn := ledger.MustNewLedgerTime(0, 1)
		tsOut := ledger.MustNewLedgerTime(0, 51)
		src := fmt.Sprintf("maxChainInflationAmount(%s, %s, u64/100000)", tsIn.Source(), tsOut.Source())
		//lib.EvalFromSource(easyfl.NewGlobalDataTracePrint(nil), src)
		ledger.L().MustEqual(src, "u64/0")
		inflationDirect := ledger.L().ID.ChainInflationAmount(tsIn, tsOut, 100000)
		require.EqualValues(t, 0, inflationDirect)
	})
	t.Run("3", func(t *testing.T) {
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
}
