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
		t.Logf("genesis slot: %d", ledger.GenesisSlot())
		ledger.L().MustEqual("constGenesisSlot", fmt.Sprintf("u64/%d", ledger.GenesisSlot()))
		require.EqualValues(t, ledger.L().ID.TicksPerSlot(), ledger.TicksPerSlot())
	})
	t.Run("2", func(t *testing.T) {
		t.Logf("genesis slot: %d", ledger.GenesisSlot())
		tsIn := ledger.MustNewLedgerTime(ledger.GenesisSlot(), 1)
		tsOut := ledger.MustNewLedgerTime(ledger.GenesisSlot(), 51)
		src := fmt.Sprintf("chainInflationAmount(%s, %s, u64/100000)", tsIn.Source(), tsOut.Source())
		//lib.EvalFromSource(easyfl.NewGlobalDataTracePrint(nil), src)
		ledger.L().MustEqual(src, "u64/0")
		inflationDirect := ledger.L().ID.ChainInflationAmount(tsIn, tsOut, 100000)
		require.EqualValues(t, 0, inflationDirect)
	})
	t.Run("3", func(t *testing.T) {
		t.Logf("genesis slot: %d", ledger.GenesisSlot())
		tsIn := ledger.MustNewLedgerTime(ledger.GenesisSlot(), 1)
		tsOut := ledger.MustNewLedgerTime(ledger.GenesisSlot(), 51)

		amountIn := ledger.L().ID.ChainInflationPerTickFractionBase
		t.Logf("ChainInflationPerTickFractionBase const: %s", util.GoTh(amountIn))
		src := fmt.Sprintf("chainInflationAmount(%s, %s, u64/%d)", tsIn.Source(), tsOut.Source(), amountIn)
		ledger.L().MustEqual(src, "u64/50")
		ledger.L().MustEqual(src, fmt.Sprintf("u64/%d", ledger.L().ID.ChainInflationAmount(tsIn, tsOut, amountIn)))

		src = fmt.Sprintf("inflationAmount(%s, %s, u64/%d)", tsIn.Source(), tsOut.Source(), amountIn)
		ledger.L().MustEqual(src, fmt.Sprintf("u64/%d", ledger.L().ID.InflationAmount(tsIn, tsOut, amountIn)))
		t.Logf("inflationAmount: %s", util.GoTh(ledger.L().ID.InflationAmount(tsIn, tsOut, amountIn)))

		//lib.EvalFromSource(easyfl.NewGlobalDataTracePrint(nil), src)
	})
	t.Run("4", func(t *testing.T) {
		t.Logf("genesis slot: %d", ledger.GenesisSlot())
		t.Logf("halving epochs: %d", ledger.L().Const().HalvingEpochs())
		t.Logf("supplyInSlot per epoch: %d", ledger.L().Const().SlotsPerEpoch())
		t.Logf("seconds per year: %d", 24*365*60*60)

		// TODO wrong test. Does not fall into the opportunity window
		tsStart := ledger.MustNewLedgerTime(ledger.GenesisSlot(), 1)
		amountIn := ledger.L().Const().ChainInflationPerTickFractionBase() + 13370000
		t.Logf("amountIn: %s", util.GoTh(amountIn))
		for i := 0; i < 10; i++ {
			tsIn := tsStart.AddSlots(int(ledger.L().ID.SlotsPerLedgerEpoch) * i)
			tsOut1 := tsIn.AddSlots(3)
			src := fmt.Sprintf("inflationAmount(%s, %s, u64/%d)", tsIn.Source(), tsOut1.Source(), amountIn)
			ledger.L().MustEqual(src, fmt.Sprintf("u64/%d", ledger.L().ID.InflationAmount(tsIn, tsOut1, amountIn)))
			t.Logf("year %d: tsIn: %s, tsOut: %s, ledger epoch: %d, chainInflationDirect: %s, inflation: %s",
				i, tsIn.String(), tsOut1.String(), ledger.L().Const().HalvingEpoch(tsOut1),
				util.GoTh(ledger.L().ID.ChainInflationAmount(tsIn, tsOut1, amountIn)),
				util.GoTh(ledger.L().ID.InflationAmount(tsIn, tsOut1, amountIn)),
			)
			tsOut2 := tsIn.AddSlots(4)
			tsOut2 = ledger.MustNewLedgerTime(tsOut2.Slot(), 0)
			src = fmt.Sprintf("inflationAmount(%s, %s, u64/%d)", tsIn.Source(), tsOut2.Source(), amountIn)
			ledger.L().MustEqual(src, fmt.Sprintf("u64/%d", ledger.L().ID.InflationAmount(tsIn, tsOut2, amountIn)))
			t.Logf("year %d: tsIn: %s, tsOut: %s, ledger epoch: %d, chainInflationDirect: %s, inflation: %s",
				i, tsIn.String(), tsOut2.String(), ledger.L().Const().HalvingEpoch(tsOut2),
				util.GoTh(ledger.L().ID.ChainInflationAmount(tsIn, tsOut2, amountIn)),
				util.GoTh(ledger.L().ID.InflationAmount(tsIn, tsOut2, amountIn)),
			)
		}
	})
}
