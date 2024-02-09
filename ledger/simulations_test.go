package ledger

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

var (
	SlotsPerDay  = 24 * time.Hour / SlotDuration()
	SlotsPerHour = int64(time.Hour / SlotDuration())
	TicksPerYear = int64(SlotsPerYear() * TicksPerSlot)
	TicksPerHour = int64(time.Hour / TickDuration())
)

const (
	PricePRXI = 1.0
)

func TestInflationCalculations1(t *testing.T) {
	t.Logf("MaxUint64: %s, require bits: %d", util.GoTh(uint64(math.MaxUint64)), requireBits(uint64(math.MaxUint64)))
	t.Logf("Proxima default supply: %s, require bits: %d", util.GoTh(DefaultInitialSupply), requireBits(DefaultInitialSupply))
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
		util.GoTh(DefaultInitialSupply),
		requireBits(DefaultInitialSupply),
		PricePRXI,
		util.GoTh(int(PricePRXI*InitialSupplyProxi)),
		SlotDuration(),
		TicksPerSlot,
		TickDuration(),
		util.GoTh(SlotsPerYear()),
		util.GoTh(int64(SlotsPerDay)),
		util.GoTh(TicksPerYear),
		util.GoTh(int64(SlotsPerHour)),
		util.GoTh(TicksPerHour),
	)
}

var (
	BranchInflationAnnual   = SlotsPerYear() * InitialBranchInflationBonus
	MaxAnnualInflationChain = MaxSlotInflationChain * SlotsPerYear()
)

const (
	ChainInflationFractionPerSlot = ChainInflationFractionPerTick / TicksPerSlot
	MinInflatableAmountPerTick    = ChainInflationFractionPerTick
	MinInflatableAmountPerSlot    = MinInflatableAmountPerTick / TicksPerSlot
	MaxSlotInflationChain         = (DefaultInitialSupply * TicksPerSlot) / ChainInflationFractionPerTick
)

func TestInflationCalculations2(t *testing.T) {
	const template2 = `
		InitialSupply						: %s (%s PRXI)
		InitialBranchInflationBonus				: %s (%s PRXI)
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
		util.GoTh(DefaultInitialSupply), util.GoTh(InitialSupplyProxi),
		util.GoTh(InitialBranchInflationBonus), util.GoTh(InitialBranchInflationBonus/DustPerProxi),
		util.GoTh(BranchInflationAnnual), util.GoTh(BranchInflationAnnual/DustPerProxi),
		percent(int(BranchInflationAnnual), DefaultInitialSupply),
		util.GoTh(ChainInflationFractionPerTick), requireBits(ChainInflationFractionPerTick),
		util.GoTh(MinInflatableAmountPerTick), util.GoTh(MinInflatableAmountPerTick/DustPerProxi),
		util.GoTh(MinInflatableAmountPerSlot), util.GoTh(MinInflatableAmountPerSlot/DustPerProxi),
		util.GoTh(MaxSlotInflationChain), util.GoTh(MaxSlotInflationChain/DustPerProxi),
		util.GoTh(MaxAnnualInflationChain), util.GoTh(MaxAnnualInflationChain/DustPerProxi),
		percent(int(MaxAnnualInflationChain), DefaultInitialSupply),
		percent(int(MaxAnnualInflationChain+BranchInflationAnnual), DefaultInitialSupply),
	)
}

var chainFractionSchedule = []int{
	ChainInflationFractionPerSlot,
	ChainInflationFractionPerSlot * 2,
	ChainInflationFractionPerSlot * 4,
	ChainInflationFractionPerSlot * 8,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
	ChainInflationFractionPerSlot * 14,
}

func TestInflationCalculations3(t *testing.T) {
	for i, fr := range chainFractionSchedule {
		t.Logf("    year %d : chain fraction %s", i, util.GoTh(fr))
	}
}

type (
	yearData struct {
		chainInflation  int
		branchInflation int
		slots           []int
	}
)

const capitalParticipatingShare = 100

func TestInflationProjections(t *testing.T) {
	years := make([]yearData, len(chainFractionSchedule))
	var chainSlotInflation int
	branchI := InitialBranchInflationBonus
	for y, year := range years {
		year.slots = make([]int, SlotsPerYear())
		for i := range year.slots {
			if i == 0 {
				if y == 0 {
					year.slots[0] = DefaultInitialSupply
					chainSlotInflation = 0
				} else {
					year.slots[0] = years[y-1].slots[SlotsPerYear()-1]
					chainSlotInflation = (years[y-1].slots[SlotsPerYear()-1] / chainFractionSchedule[y]) * capitalParticipatingShare / 100
				}
			} else {
				chainSlotInflation = (year.slots[i-1] / chainFractionSchedule[y]) * capitalParticipatingShare / 100
				year.slots[i] = year.slots[i-1] + chainSlotInflation + branchI
			}
			year.chainInflation += chainSlotInflation
			year.branchInflation += branchI
		}
		years[y] = year
		branchI = branchI + branchI*AnnualBranchInflationPromille/1000 // 4% indexing
	}

	for y, year := range years {
		initSupply := year.slots[0]
		finalSupply := year.slots[SlotsPerYear()-1]
		annualInflation := finalSupply - initSupply
		fmt.Printf("year %d (%s), supply: %s (%d bits) -> %s, inflation: %s, %.2f%%  chain inflation: %.2f%%, branch inflation %.2f%%\n",
			y, util.GoTh(chainFractionSchedule[y]),
			util.GoTh(initSupply), requireBits(uint64(initSupply)), util.GoTh(finalSupply),
			util.GoTh(annualInflation), percent(annualInflation, initSupply),
			percent(year.chainInflation, initSupply), percent(year.branchInflation, initSupply),
		)
	}
}
