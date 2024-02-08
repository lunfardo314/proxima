package genesis

import (
	"fmt"
	"math"
	"testing"
	"time"

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
	DustPerProxi       = 1_000_000
	InitialSupplyProxi = 1_000_000_000
	InitialSupply      = InitialSupplyProxi * DustPerProxi
	PricePRXI          = 1.0
	SlotDuration       = 10 * time.Second
	YearDuration       = 24 * 265 * time.Hour
	SlotsPerYear       = int(YearDuration) / int(SlotDuration)
	SlotsPerDay        = int(24*time.Hour) / int(SlotDuration)
	SlotsPerHour       = int(time.Hour) / int(SlotDuration)
	TicksPerSlot       = 100
	TickDuration       = SlotDuration / TicksPerSlot
	TicksPerYear       = SlotsPerYear * TicksPerSlot
	TicksPerHour       = int(time.Hour) / int(TickDuration)
)

func TestInflationCalculations1(t *testing.T) {
	t.Logf("MaxUint64: %s, require bits: %d", util.GoTh(uint64(math.MaxUint64)), requireBits(uint64(math.MaxUint64)))
	t.Logf("Proxima default supply: %s, require bits: %d", util.GoTh(DefaultSupply), requireBits(DefaultSupply))
	const (
		SatoshiInBTC = 100_000_000
		MaxBTCApprox = 21_000_000
	)
	t.Logf("Max Bitcoin supply. BTC: %s", util.GoTh(MaxBTCApprox))
	t.Logf("Max Bitcoin supply. Satoshi: %s, , require bits: %d", util.GoTh(MaxBTCApprox*SatoshiInBTC), requireBits(MaxBTCApprox*SatoshiInBTC))

	const template1 = `
		DustPerProxi (dust/PRXI) 	: %s (%d bits)
		InitialSupplyProxi (PRXI)	: %s (%d bits)
		InitialSupply (dust)		: %s dust (%d bits)
		Price						: %.2f USD
		Mcap						: %s USD
		SlotDuration        		: %v
		TicksPerSlot				: %d
		TickDuration				: %v
		SlotsPerYear        		: %s
		SlotsPerDay 	       		: %s
		TicksPerYear				: %s
		SlotsPerHour        		: %s
		TicksPerHour				: %s
`
	t.Logf(template1,
		util.GoTh(DustPerProxi),
		requireBits(DustPerProxi),
		util.GoTh(InitialSupplyProxi),
		requireBits(InitialSupplyProxi),
		util.GoTh(InitialSupply),
		requireBits(InitialSupply),
		PricePRXI,
		util.GoTh(int(PricePRXI*InitialSupplyProxi)),
		SlotDuration,
		TicksPerSlot,
		TickDuration,
		util.GoTh(SlotsPerYear),
		util.GoTh(SlotsPerDay),
		util.GoTh(TicksPerYear),
		util.GoTh(SlotsPerHour),
		util.GoTh(TicksPerHour),
	)
}

const (
	BranchInflationBonus  = 20_000_000
	BranchInflationAnnual = SlotsPerYear * BranchInflationBonus

	ChainInflationFractionPerTick = 400_000_000
	ChainInflationFractionPerSlot = ChainInflationFractionPerTick / TicksPerSlot
	MinInflatableAmountPerTick    = ChainInflationFractionPerTick
	MinInflatableAmountPerSlot    = MinInflatableAmountPerTick / TicksPerSlot
	MaxSlotInflationChain         = (InitialSupply * TicksPerSlot) / ChainInflationFractionPerTick
	MaxAnnualInflationChain       = MaxSlotInflationChain * SlotsPerYear
)

var chainFractionSchedule = []int{
	ChainInflationFractionPerTick,
	ChainInflationFractionPerTick / 2,
	ChainInflationFractionPerTick / 4,
	ChainInflationFractionPerTick / 8,
	ChainInflationFractionPerTick / 16,
}

func TestInflationCalculations2(t *testing.T) {
	const template2 = `
		InitialSupply						: %s (%s PRXI)
		BranchInflationBonus				: %s (%s PRXI)
		BranchInflationAnnual				: %s (%s PRXI)
		BranchInflationAnnual %%				: %.2f%%
		ChainInflationFractionPerTick		: %s (%d bits) 
		MinInflatableAmountPerTick			: %s (%s PRXI)
		MinInflatableAmountPerSlot			: %s (%s PRXI)
		MaxSlotInflationChain				: %s (%s PRXI)
		MaxAnnualInflationChain				: %s (%s PRXI)
		MaxAnnualInflationChain %%			: %.2f%%
		MaxAnnualInflationTotal %%			: %.2f%%
`

	t.Logf(template2,
		util.GoTh(InitialSupply), util.GoTh(InitialSupplyProxi),
		util.GoTh(BranchInflationBonus), util.GoTh(BranchInflationBonus/DustPerProxi),
		util.GoTh(BranchInflationAnnual), util.GoTh(BranchInflationAnnual/DustPerProxi),
		percent(BranchInflationAnnual, InitialSupply),
		util.GoTh(ChainInflationFractionPerTick), requireBits(ChainInflationFractionPerTick),
		util.GoTh(MinInflatableAmountPerTick), util.GoTh(MinInflatableAmountPerTick/DustPerProxi),
		util.GoTh(MinInflatableAmountPerSlot), util.GoTh(MinInflatableAmountPerSlot/DustPerProxi),
		util.GoTh(MaxSlotInflationChain), util.GoTh(MaxSlotInflationChain/DustPerProxi),
		util.GoTh(MaxAnnualInflationChain), util.GoTh(MaxAnnualInflationChain/DustPerProxi),
		percent(MaxAnnualInflationChain, InitialSupply),
		percent(MaxAnnualInflationChain+BranchInflationAnnual, InitialSupply),
	)
}

func TestInflationCalculations3(t *testing.T) {
	for i, fr := range chainFractionSchedule {
		t.Logf("    year %d : chain fraction %s", i, util.GoTh(fr))
	}
}

type (
	yearData struct {
		chainFraction int
		branchBonus   int
	}
	slotData struct {
		supply         int
		chainInflation int
	}
)

const capitalParticipatingPerc = 100

func TestInflationProjections(t *testing.T) {
	supply := make([]int, SlotsPerYear)
	totalChainInflation, totalBranchInflation := 0, 0
	for i := range supply {
		if i == 0 {
			supply[0] = InitialSupply
			continue
		}
		chainInflation := (supply[i-1] / ChainInflationFractionPerSlot) * capitalParticipatingPerc / 100
		supply[i] = supply[i-1] + chainInflation + BranchInflationBonus
		totalChainInflation += chainInflation
		totalBranchInflation += BranchInflationBonus
	}

	initSupply := supply[0]
	finalSupply := supply[len(supply)-1]
	annualInflation := finalSupply - initSupply
	fmt.Printf("final supply: %s\n", util.GoTh(finalSupply))
	fmt.Printf("annual inflation: %s = %s + %s\n", util.GoTh(annualInflation), util.GoTh(totalChainInflation), util.GoTh(totalBranchInflation))
	fmt.Printf("final inflation (%d slots) %%: %.2f%%\n", len(supply), percent(annualInflation, initSupply))
	fmt.Printf("total chain inflation %%: %.2f%%\n", percent(totalChainInflation, initSupply))
	fmt.Printf("total branch inflation %%: %.2f%%\n", percent(totalBranchInflation, initSupply))

}
