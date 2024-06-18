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
	t.Logf("MaxUint64: %s, require bits: %d", util.GoTh(uint64(math.MaxUint64)), requireBits(uint64(math.MaxUint64)))
	t.Logf("Proxima default initial supply: %s (%s PRXI), require bits: %d",
		util.GoTh(ledger.L().ID.InitialSupply), util.GoTh(ledger.L().ID.InitialSupply/ledger.PRXI), requireBits(ledger.L().ID.InitialSupply))
	const (
		SatoshiInBTC = 100_000_000
		MaxBTCApprox = 21_000_000
	)
	t.Logf("Max Bitcoin supply. BTC: %s", util.GoTh(MaxBTCApprox))
	t.Logf("Max Bitcoin supply. Satoshi: %s, , require bits: %d", util.GoTh(MaxBTCApprox*SatoshiInBTC), requireBits(MaxBTCApprox*SatoshiInBTC))

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
		util.GoTh(ledger.DustPerProxi),
		requireBits(ledger.DustPerProxi),
		util.GoTh(ledger.L().ID.InitialSupply/ledger.PRXI),
		requireBits(ledger.L().ID.InitialSupply/ledger.PRXI),
		util.GoTh(ledger.L().ID.InitialSupply),
		requireBits(ledger.L().ID.InitialSupply),
		PricePRXI,
		util.GoTh(int(PricePRXI*ledger.L().ID.InitialSupply)),
		ledger.SlotDuration(),
		ledger.TicksPerSlot(),
		ledger.TickDuration(),
		util.GoTh(int64(ledger.SlotsPerDay())),
		util.GoTh(ledger.SlotsPerHour()),
		util.GoTh(ledger.TicksPerHour()),
	)
}

func BranchInflationAnnual() int64 {
	return ledger.SlotsPerHalvingEpoch() * int64(ledger.L().ID.BranchInflationBonusBase)
}

func MaxInflationChainEpoch() int64 {
	return InitialSlotInflationChain() * ledger.SlotsPerHalvingEpoch()
}

func ChainInitialInflationFractionPerSlot() int64 {
	return int64(ledger.L().ID.ChainInflationPerTickFraction) / int64(ledger.TicksPerSlot())
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

func TestInflationCalculations2(t *testing.T) {
	const template2 = `
		InitialSupply						: %s (%s PRXI)
		DefaultMaxBranchInflationBonus	: %s (%s PRXI)
		BranchInflationAnnual				: %s (%s PRXI)
		BranchInflationAnnual %%				: %.2f%%
		InitialChainInflationFractionPerTick: %s
		MinInflatableAmountPerTick			: %s (%s PRXI)
		MinInflatableAmountPerSlot			: %s (%s PRXI)
		InitialSlotInflationChain				: %s (%s PRXI)
		MaxInflationChainEpoch				: %s (%s PRXI)
		MaxInflationChainEpoch %%			: %.2f%%
		MaxAnnualInflationTotal %%			: %.2f%%
`

	t.Logf(template2,
		util.GoTh(ledger.L().ID.InitialSupply), util.GoTh(ledger.L().ID.InitialSupply/ledger.PRXI),
		util.GoTh(ledger.L().ID.BranchInflationBonusBase), util.GoTh(ledger.L().ID.BranchInflationBonusBase/ledger.DustPerProxi),
		util.GoTh(BranchInflationAnnual()), util.GoTh(BranchInflationAnnual()/ledger.DustPerProxi),
		percent(int(BranchInflationAnnual()), int(ledger.L().ID.InitialSupply)),
		util.GoTh(ledger.L().ID.ChainInflationPerTickFraction),
		util.GoTh(MinInflatableAmountPerTick()), util.GoTh(MinInflatableAmountPerTick()/ledger.PRXI),
		util.GoTh(MinInflatableAmountPerSlot()), util.GoTh(MinInflatableAmountPerSlot()/ledger.DustPerProxi),
		util.GoTh(InitialSlotInflationChain()), util.GoTh(InitialSlotInflationChain()/ledger.DustPerProxi),
		util.GoTh(MaxInflationChainEpoch()), util.GoTh(MaxInflationChainEpoch()/ledger.DustPerProxi),
		percent(int(MaxInflationChainEpoch()), int(ledger.L().ID.InitialSupply)),
		percent(int(MaxInflationChainEpoch()+BranchInflationAnnual()), int(ledger.L().ID.InitialSupply)),
	)
}
