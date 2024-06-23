package simulations

import (
	"math"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
)

func requireBits(n uint64) (ret int) {
	for ; n != 0; ret++ {
		n >>= 1
	}
	return
}

func percent(n, d int) float32 {
	return (float32(n) * 100) / float32(d)
}

const (
	PricePRXI = 1.0
)

func TestInflationCalculations1(t *testing.T) {
	t.Logf("MaxUint64: %s, require bits: %d", util.Th(uint64(math.MaxUint64)), requireBits(uint64(math.MaxUint64)))
	t.Logf("Proxima default initial supply: %s (%s PRXI), require bits: %d",
		util.Th(ledger.L().ID.InitialSupply), util.Th(ledger.L().ID.InitialSupply/ledger.PRXI), requireBits(ledger.L().ID.InitialSupply))
	const (
		SatoshiInBTC = 100_000_000
		MaxBTCApprox = 21_000_000
	)
	t.Logf("Max Bitcoin supply. BTC: %s", util.Th(MaxBTCApprox))
	t.Logf("Max Bitcoin supply. Satoshi: %s, , require bits: %d", util.Th(MaxBTCApprox*SatoshiInBTC), requireBits(MaxBTCApprox*SatoshiInBTC))

	const template1 = `
		DustPerProxi (dust/PRXI) 	: %s (%d bits)
		InitialSupply (PRXI)		: %s (%d bits)
		InitialSupply (dust)		: %s dust (%d bits)
		Price						: %.2f USD
		Mcap						: %s USD
		SlotDuration        		: %v
		TicksPerSlot				: %d
		TickDuration				: %v
		SlotsPerDay 	       		: %s
		SlotsPerHour        		: %s
		TicksPerHour				: %s
`
	t.Logf(template1,
		util.Th(ledger.DustPerProxi),
		requireBits(ledger.DustPerProxi),
		util.Th(ledger.L().ID.InitialSupply/ledger.PRXI),
		requireBits(ledger.L().ID.InitialSupply/ledger.PRXI),
		util.Th(ledger.L().ID.InitialSupply),
		requireBits(ledger.L().ID.InitialSupply),
		PricePRXI,
		util.Th(int(PricePRXI*ledger.L().ID.InitialSupply)),
		ledger.SlotDuration(),
		ledger.TicksPerSlot(),
		ledger.TickDuration(),
		util.Th(int64(ledger.SlotsPerDay())),
		util.Th(ledger.SlotsPerHour()),
		util.Th(ledger.TicksPerHour()),
	)
}

func ChainInitialInflationFractionPerSlot() int64 {
	return int64(ledger.L().ID.ChainInflationFractionPerTick) / int64(ledger.TicksPerSlot())
}

func MinInflatableAmountPerTick() int64 {
	return ChainInitialInflationFractionPerSlot()
}

func MinInflatableAmountPerSlot() int64 {
	return ChainInitialInflationFractionPerSlot() / int64(ledger.TicksPerSlot())
}

func InitialSlotInflationChain() int64 {
	return int64(ledger.L().ID.InitialSupply) / ChainInitialInflationFractionPerSlot()
}

//
//func TestInflationCalculations2(t *testing.T) {
//	const template2 = `
//		InitialSupply						: %s (%s PRXI)
//		DefaultMaxBranchInflationBonus	: %s (%s PRXI)
//		BranchInflationAnnual				: %s (%s PRXI)
//		BranchInflationAnnual %%				: %.2f%%
//		InitialChainInflationFractionPerTick: %s
//		MinInflatableAmountPerTick			: %s (%s PRXI)
//		MinInflatableAmountPerSlot			: %s (%s PRXI)
//		InitialSlotInflationChain				: %s (%s PRXI)
//		MaxInflationChainEpoch				: %s (%s PRXI)
//		MaxInflationChainEpoch %%			: %.2f%%
//		MaxAnnualInflationTotal %%			: %.2f%%
//`
//
//	t.Logf(template2,
//		util.Th(ledger.L().ID.InitialSupply), util.Th(ledger.L().ID.InitialSupply/ledger.PRXI),
//		util.Th(ledger.L().ID.BranchInflationBonusBase), util.Th(ledger.L().ID.BranchInflationBonusBase/ledger.DustPerProxi),
//		util.Th(BranchInflationAnnual()), util.Th(BranchInflationAnnual()/ledger.DustPerProxi),
//		percent(int(BranchInflationAnnual()), int(ledger.L().ID.InitialSupply)),
//		util.Th(ledger.L().ID.ChainInflationFractionPerTick),
//		util.Th(MinInflatableAmountPerTick()), util.Th(MinInflatableAmountPerTick()/ledger.PRXI),
//		util.Th(MinInflatableAmountPerSlot()), util.Th(MinInflatableAmountPerSlot()/ledger.DustPerProxi),
//		util.Th(InitialSlotInflationChain()), util.Th(InitialSlotInflationChain()/ledger.DustPerProxi),
//		util.Th(MaxInflationChainEpoch()), util.Th(MaxInflationChainEpoch()/ledger.DustPerProxi),
//		percent(int(MaxInflationChainEpoch()), int(ledger.L().ID.InitialSupply)),
//		percent(int(MaxInflationChainEpoch()+BranchInflationAnnual()), int(ledger.L().ID.InitialSupply)),
//	)
//}
